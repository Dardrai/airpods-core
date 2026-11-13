package dongle

import (
	"errors"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
)

func TestDecodeFirmwareInfo(t *testing.T) {
	for _, tc := range []struct {
		reply []byte
		want  FirmwareInfo
		ok    bool
	}{
		{[]byte{1, 0, 1, 66, 0, 1}, FirmwareInfo{0, 1, 66, true}, true},
		{[]byte{1, 1, 2, 3, 1, 0}, FirmwareInfo{1, 2, 259, false}, true},
		{[]byte{2, 0, 1, 66, 0, 0}, FirmwareInfo{}, false},
		{[]byte{1, 0, 1, 66, 0, 2}, FirmwareInfo{}, false},
		{[]byte{1, 0, 1, 66, 0}, FirmwareInfo{}, false},
		{[]byte{1, 0, 1, 66, 0, 0, 0}, FirmwareInfo{}, false},
	} {
		got, err := DecodeFirmwareInfo(tc.reply)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("% x: %+v %v", tc.reply, got, err)
		}
	}
	if (FirmwareInfo{0, 1, 66, true}).String() != "0.1.66" {
		t.Error("name")
	}
}

func TestFirmwareInfoFromTheDongle(t *testing.T) {
	cfg := sim.Default()
	cfg.Firmware = "0.1.67"
	client, device := newClient(cfg)
	got, err := client.FirmwareInfo()
	if err != nil || got != (FirmwareInfo{0, 1, 67, false}) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, payload := range device.Commits() {
		if len(payload) != 1 || payload[0] != kindFirmwareInfo {
			t.Errorf("sent % x", payload)
		}
	}
	cfg.Firmware = "0.1.67-experimental-radio0076"
	client, _ = newClient(cfg)
	if got, err := client.FirmwareInfo(); err != nil || !got.Experimental {
		t.Fatalf("%+v %v", got, err)
	}
}

// Firmware before the report refuses the request as unsupported.
func TestOlderFirmwareDoesNotReportItsVersion(t *testing.T) {
	client, _ := newClient(sim.Default())
	_, err := client.FirmwareInfo()
	var unsupported *Unsupported
	if !errors.As(err, &unsupported) || unsupported.Message != firmwareUnreportedText {
		t.Fatalf("%v", err)
	}
}
