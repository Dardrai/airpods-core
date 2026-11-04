// Package dongle implements the AirPods dongle protocol on top of a hid.Device:
// status (report 2), control commands, typed requests (report 3) and settings.
package dongle

import (
	"encoding/binary"
	"fmt"

	"github.com/Dardrai/airpods-core/hid"
)

// Capability bits of Status.Capabilities.
const (
	CapControl      uint16 = 0x01 // opcodes 1 and 2
	CapListening    uint16 = 0x02 // opcode 3
	CapPairPhone    uint16 = 0x04 // opcode 4
	CapRPC          uint16 = 0x08
	CapSettings     uint16 = 0x10
	CapLease        uint16 = 0x20
	CapPerComponent uint16 = 0x40
)

// BatteryUnknown is the raw battery value the dongle reports for "no reading".
const BatteryUnknown = 255

// Status is a decoded report 2. Raw keeps the undecoded bytes.
type Status struct {
	Raw [hid.ReportSize]byte

	Flags            byte
	Connected        bool
	ControlsReady    bool
	Streaming        bool
	PhoneConnected   bool
	MicrophoneActive bool
	PhonePairing     bool
	USBPlayback      bool

	Generation uint32
	Peer       [6]byte
	ANC        byte // 1 off, 2 ANC, 3 transparency, 4 adaptive

	BatteryLeft, BatteryRight, BatteryCase byte
	ChargingMask                           byte
	Codec                                  byte
	AgeLeft, AgeRight, AgeCase             uint32

	ReceiptSequence uint32
	ReceiptResult   byte
	ReceiptOpcode   byte

	Capabilities   uint16
	PresentMask    byte
	CaseSourceMask byte
}

// DecodeStatus decodes a report 2 whose identity hid.Device.Get already verified.
func DecodeStatus(raw [hid.ReportSize]byte) Status {
	flags := raw[7]
	le := binary.LittleEndian
	return Status{
		Raw:              raw,
		Flags:            flags,
		Connected:        flags&1 != 0,
		ControlsReady:    flags&2 != 0,
		Streaming:        flags&4 != 0,
		PhoneConnected:   flags&8 != 0,
		MicrophoneActive: flags&16 != 0,
		PhonePairing:     flags&32 != 0,
		USBPlayback:      flags&64 != 0,
		Generation:       le.Uint32(raw[8:]),
		Peer:             [6]byte(raw[12:18]),
		ANC:              raw[18],
		BatteryLeft:      raw[19],
		BatteryRight:     raw[20],
		BatteryCase:      raw[21],
		ChargingMask:     raw[22],
		Codec:            raw[23],
		AgeLeft:          le.Uint32(raw[24:]),
		AgeRight:         le.Uint32(raw[28:]),
		AgeCase:          le.Uint32(raw[32:]),
		ReceiptSequence:  le.Uint32(raw[36:]),
		ReceiptResult:    raw[40],
		ReceiptOpcode:    raw[41],
		Capabilities:     le.Uint16(raw[42:]),
		PresentMask:      raw[44],
		CaseSourceMask:   raw[45],
	}
}

// Has reports whether every given capability bit is advertised.
func (s Status) Has(capability uint16) bool { return s.Capabilities&capability == capability }

// Selected reports whether the dongle has a saved AirPods pair to reconnect to.
func (s Status) Selected() bool { return s.Peer != [6]byte{} }

// CodecName names the codec byte; anything unmapped is "unknown".
func (s Status) CodecName() string {
	switch s.Codec {
	case 1:
		return "SBC"
	case 2:
		return "AAC"
	case 3:
		return "AAC-ELD"
	}
	return "unknown"
}

// BatteryText formats a raw battery byte: "N%" up to 100, otherwise "unknown".
func BatteryText(raw byte) string {
	if raw <= 100 {
		return fmt.Sprintf("%d%%", raw)
	}
	return "unknown"
}
