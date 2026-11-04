package hearing

import (
	"strings"
	"testing"
	"time"

	"github.com/Dardrai/airpods-core/dongle/sim"
)

func TestUUIDOrderMatchesTheSim(t *testing.T) {
	if diagnosticsUUID != UUID(sim.UUIDDiagnostics) || curveUUID != UUID(sim.UUIDCurve) || Service != UUID(sim.ServiceUUID) {
		t.Error("wire order differs")
	}
}

func TestDiscoveryFindsHandlesAndWriteKinds(t *testing.T) {
	server := sim.NewAirPodsServer()
	server.Attributes = append(server.Attributes,
		&sim.Attribute{UUID: sim.UUIDCheck, Props: 0x08, Handle: 0x1b, Value: []byte{0}},
		&sim.Attribute{UUID: sim.UUIDHearing, Props: 0x06, Handle: 0x1d, Value: []byte{0}})
	exchange := func(pdu []byte) ([]byte, error) { return server.Handle(pdu), nil }
	found, err := DiscoverRead(exchange, "x", testModeUUID, UUID(sim.UUIDHearing), ParseUUID("00000000000000000000000000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if found[0].Handle != 0x13 || found[0].WriteOpcode != 0 {
		t.Errorf("first %+v", found[0])
	}
	if found[1].Handle != 0x1d || found[1].WriteOpcode != attCommand {
		t.Errorf("second %+v", found[1])
	}
	if found[2] != nil {
		t.Errorf("third %+v", found[2])
	}
}

func TestDiscoveryRejectsMalformedAnswers(t *testing.T) {
	cases := map[string]func(pdu []byte) []byte{
		"service error": func(pdu []byte) []byte { return []byte{1, 6, 0, 0, 3} },
		"bad range":     func(pdu []byte) []byte { return []byte{7, 5, 0, 5, 0} },
		"backwards entry": func(pdu []byte) []byte {
			if pdu[0] == 6 {
				return []byte{7, 0x10, 0, 0x20, 0}
			}
			return append([]byte{9, 7}, 0x12, 0, 2, 0x11, 0, 0, 0)
		},
	}
	for name, answer := range cases {
		if _, err := DiscoverRead(func(pdu []byte) ([]byte, error) { return answer(pdu), nil }, "x", testModeUUID); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	notFound := func(pdu []byte) ([]byte, error) { return []byte{1, 6, 1, 0, 0x0a}, nil }
	if found, err := DiscoverRead(notFound, "x", testModeUUID); err != nil || found[0] != nil {
		t.Errorf("missing service: %v %v", found, err)
	}
}

func TestParseDiagnostics(t *testing.T) {
	left, right, err := ParseDiagnostics(sim.DiagnosticRecord(100, 200, 0, 1|1<<9))
	if err != nil {
		t.Fatal(err)
	}
	if left.Text() != "Passed" || left.MeasuredAtSeconds != 100 {
		t.Errorf("left %+v", left)
	}
	if right.Text() != "Saved result reports a failure (speaker)" {
		t.Errorf("right %q", right.Text())
	}
	for word, want := range map[uint32]string{2 << 3: "lid", 3 << 3: "moved", 4 << 3: "temperature", 5 << 3: "noisy", 6 << 3: "interrupted", 2: "No saved", 4: "No saved"} {
		ear, _, err := ParseDiagnostics(sim.DiagnosticRecord(100, 200, word, 0))
		if err != nil || !strings.Contains(ear.Text(), want) {
			t.Errorf("word %x: %q %v", word, ear.Text(), err)
		}
	}
	if none, _, _ := ParseDiagnostics(sim.DiagnosticRecord(0, 0, 0, 0)); none.Text() != "No saved result" {
		t.Errorf("no time %q", none.Text())
	}
	if _, _, err := ParseDiagnostics(make([]byte, 70)); err == nil {
		t.Error("short")
	}
	if _, _, err := ParseDiagnostics(make([]byte, 71)); err == nil {
		t.Error("version")
	}
}

func TestReadDiagnosticsIsModelGatedAndReadOnly(t *testing.T) {
	server := sim.NewAirPodsServer()
	exchange := func(pdu []byte) ([]byte, error) { return server.Handle(pdu), nil }
	if _, err := ReadDiagnostics(exchange, "A2564", time.Now()); err == nil {
		t.Error("AirPods 3 has none")
	}
	record, err := ReadDiagnostics(exchange, "A3063", time.Now())
	if err != nil || record == nil || record.Left.MeasuredAtSeconds != 1750000000 {
		t.Fatalf("%+v %v", record, err)
	}
	if server.Writes != 0 {
		t.Error("a read wrote something")
	}
	server.Attributes = nil
	server.Service = [16]byte{}
	if record, err := ReadDiagnostics(exchange, "A3063", time.Now()); record != nil || err != nil {
		t.Errorf("no service: %v %v", record, err)
	}
}

func TestReadDeviceData(t *testing.T) {
	server := sim.NewAirPodsServer()
	exchange := func(pdu []byte) ([]byte, error) { return server.Handle(pdu), nil }
	data, err := ReadDeviceData(exchange, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if data.TestMode == nil || *data.Sensitivity != 1 || len(data.VolumeCurve) != 17 || data.VolumeCurve[3] != 0.5 {
		t.Errorf("%+v", data)
	}
	server.Find(sim.UUIDSensitivity).Value = []byte{0, 0, 0xc0, 0x7f} // NaN
	if _, err := ReadDeviceData(exchange, time.Now()); err == nil {
		t.Error("NaN accepted")
	}
}
