package dongle

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
)

func TestDecoderGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/diag_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Decoder string `json:"decoder"`
		Name    string `json:"name"`
		Hex     string `json:"hex"`
		JSON    string `json:"json"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	decoders := map[string]func([]byte) (Object, error){
		"trace": DecodeTrace, "inspect": DecodeInspect, "phone": DecodePhone, "radio": DecodeRadio,
	}
	seen := map[string]int{}
	for _, tc := range cases {
		t.Run(tc.Decoder+"/"+tc.Name, func(t *testing.T) {
			seen[tc.Decoder]++
			raw, err := hex.DecodeString(tc.Hex)
			if err != nil {
				t.Fatal(err)
			}
			object, err := decoders[tc.Decoder](raw)
			if tc.Error != "" {
				if err == nil || err.Error() != tc.Error {
					t.Fatalf("err = %v, want %q", err, tc.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := object.String(); got != tc.JSON {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.JSON)
			}
			if !json.Valid([]byte(object.String())) {
				t.Fatal("invalid JSON")
			}
		})
	}
	for name := range decoders {
		if seen[name] < 5 {
			t.Errorf("only %d fixtures for %s", seen[name], name)
		}
	}
}

// TestRadioFixtureByHand checks offsets independently of the golden file.
func TestRadioFixtureByHand(t *testing.T) {
	data := make([]byte, 128)
	data[0], data[1], data[2], data[3] = 1, 128, 1, 0b00001
	data[16] = 0xd6 // -42
	data[64], data[65], data[66] = 1, 1, 2
	binary.LittleEndian.PutUint32(data[32:], 0xffffffff)
	binary.LittleEndian.PutUint32(data[36:], 9)
	object, err := DecodeRadio(data)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"rssi_relative_db": -42, "quality": nil, "afh_mode": nil, "afh_map": nil, "connected": true} {
		if got, ok := object.Get(key); !ok || got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	ages, _ := object.Get("age_ms")
	if rssi, _ := ages.(Object).Get("rssi"); rssi != nil {
		t.Errorf("age rssi = %v", rssi)
	}
	if quality, _ := ages.(Object).Get("quality"); quality != uint32(9) {
		t.Errorf("age quality = %v", quality)
	}
	adaptive, _ := object.Get("adaptive")
	if _, present := adaptive.(Object).Get("reserved"); present {
		t.Error("reserved leaked")
	}
	if reason, _ := adaptive.(Object).Get("reason"); reason != "clean" {
		t.Errorf("reason = %v", reason)
	}
}

func TestObjectJSON(t *testing.T) {
	object := Object{{"time", 1700000000.25}, {"whole", 2.0}, {"none", nil}, {"list", []Object{{{"a", uint32(1)}}, {}}}, {"empty", []Object{}}, {"on", true}, {"s", "x"}}
	want := `{"time": 1700000000.25, "whole": 2.0, "none": null, "list": [{"a": 1}, {}], "empty": [], "on": true, "s": "x"}`
	if got := object.String(); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestReadGuards(t *testing.T) {
	client, device := newClient(sim.Default())
	for _, payload := range [][]byte{{1}, {2}, {7}, {14}, {4, 2}, {9, 0}, {5, 5, 5}, {1, 13, 0, 0, 0, 0}} {
		_, err := client.Transaction(payload)
		if err == nil || err.Error() != "Only diagnostic reads and explicit audio profiling controls are allowed" {
			t.Errorf("%v: %v", payload, err)
		}
	}
	for _, command := range []byte{0, 1, 2, 4, 14, 18} {
		_, err := client.Feature(command)
		if err == nil || err.Error() != "Collector permits audio/performance/trace reads only" {
			t.Errorf("feature %d: %v", command, err)
		}
	}
	if ops, _ := device.TraceSummary(); ops != 0 {
		t.Fatalf("guarded requests reached the device: %d ops", ops)
	}
	if _, err := client.Transaction([]byte{14, 1}); err != nil {
		t.Fatal(err)
	}
	if !device.Inspecting() {
		t.Fatal("lease not taken")
	}
	if _, err := client.Transaction([]byte{14, 0}); err != nil || device.Inspecting() {
		t.Fatalf("lease not released: %v", err)
	}
}

func TestReadRejectsEmptyResponse(t *testing.T) {
	cfg := sim.Default()
	cfg.Replies = map[byte]sim.Hex{5: {}}
	client, _ := newClient(cfg)
	if _, err := client.Feature(5); err == nil || err.Error() != "Invalid diagnostic length" {
		t.Fatalf("err = %v", err)
	}
}

func TestSampleUnsupportedFlags(t *testing.T) {
	cfg := sim.Default()
	cfg.Fail = map[byte]byte{11: 7, 15: 7}
	client, _ := newClient(cfg)
	sample, err := client.Sample(SampleOptions{Trace: true, Phone: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"trace_supported", "phone_supported"} {
		if value, ok := sample.Get(key); !ok || value != false {
			t.Errorf("%s = %v", key, value)
		}
	}
	cfg.Fail = map[byte]byte{13: 7}
	client, _ = newClient(cfg)
	var unsupported *Unsupported
	if _, err := client.Sample(SampleOptions{Inspect: true}); !errors.As(err, &unsupported) {
		t.Fatalf("inspect must propagate: %v", err)
	}
}

func TestSampleRunsAgainstTheSimulator(t *testing.T) {
	client, _ := newClient(sim.Default())
	sample, err := client.Sample(SampleOptions{Trace: true, Inspect: true, Phone: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"trace", "inspect", "phone"} {
		if _, ok := sample.Get(key); !ok {
			t.Errorf("no %s in %s", key, sample)
		}
	}
	radio, err := client.Sample(SampleOptions{Radio: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := radio.Get("radio"); !ok {
		t.Errorf("no radio in %s", radio)
	}
}
