package dongle

import (
	"encoding/binary"
	"testing"

	"github.com/Dardrai/airpods-core/hid"
)

func TestDecodeStatus(t *testing.T) {
	var raw [hid.ReportSize]byte
	header := hid.Header(2)
	copy(raw[:], header[:])
	le := binary.LittleEndian
	raw[7] = 0b1010101
	le.PutUint32(raw[8:], 0xdeadbeef)
	copy(raw[12:18], []byte{1, 2, 3, 4, 5, 6})
	raw[18] = 3
	raw[19], raw[20], raw[21] = 100, 101, 255
	raw[22] = 5
	raw[23] = 3
	le.PutUint32(raw[24:], 11)
	le.PutUint32(raw[28:], 22)
	le.PutUint32(raw[32:], 33)
	le.PutUint32(raw[36:], 44)
	raw[40], raw[41] = 2, 4
	le.PutUint16(raw[42:], 0x18)
	raw[44], raw[45] = 7, 6
	s := DecodeStatus(raw)
	if s.Raw != raw || s.Flags != 0b1010101 {
		t.Fatal("raw access lost")
	}
	if !s.Connected || s.ControlsReady || !s.Streaming || s.PhoneConnected || !s.MicrophoneActive || s.PhonePairing || !s.USBPlayback {
		t.Errorf("flags: %+v", s)
	}
	if s.Generation != 0xdeadbeef || s.Peer != [6]byte{1, 2, 3, 4, 5, 6} || s.ANC != 3 {
		t.Errorf("identity: %+v", s)
	}
	if s.BatteryLeft != 100 || s.BatteryRight != 101 || s.BatteryCase != 255 || s.ChargingMask != 5 {
		t.Errorf("battery: %+v", s)
	}
	if s.Codec != 3 || s.CodecName() != "AAC-ELD" {
		t.Errorf("codec: %d %s", s.Codec, s.CodecName())
	}
	if s.AgeLeft != 11 || s.AgeRight != 22 || s.AgeCase != 33 {
		t.Errorf("ages: %+v", s)
	}
	if s.ReceiptSequence != 44 || s.ReceiptResult != 2 || s.ReceiptOpcode != 4 {
		t.Errorf("receipt: %+v", s)
	}
	if s.Capabilities != 0x18 || !s.Has(CapSettings) || !s.Has(CapRPC) || s.Has(CapLease) || s.PresentMask != 7 || s.CaseSourceMask != 6 {
		t.Errorf("capabilities: %+v", s)
	}
}

func TestBatteryAndCodecText(t *testing.T) {
	for raw, want := range map[byte]string{0: "0%", 100: "100%", 101: "unknown", 255: "unknown"} {
		if got := BatteryText(raw); got != want {
			t.Errorf("battery %d = %q", raw, got)
		}
	}
	for codec, want := range map[byte]string{0: "unknown", 1: "SBC", 2: "AAC", 3: "AAC-ELD", 4: "unknown"} {
		if got := (Status{Codec: codec}).CodecName(); got != want {
			t.Errorf("codec %d = %q", codec, got)
		}
	}
}
