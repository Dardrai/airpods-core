package sim

import (
	"bytes"
	"encoding/binary"
	"math"
)

// More simulated attribute UUIDs, in wire order.
var (
	UUIDTransparency = printedUUID(0xd5621cc1, [12]byte{0xf7, 0xab, 0x45, 0xdb, 0x94, 0x03, 0x9e, 0xaf, 0x74, 0x4d, 0x53, 0x90})
	// UUIDLoudSound stands in for the attribute at the fixed Loud Sound Reduction handle.
	UUIDLoudSound = printedUUID(0xa41200ff, ahpsSuffix)
)

// Value offsets the simulated records share.
const (
	recordLeftAmp   = 36
	recordRightAmp  = 84
	recordOwnVoice  = 100
	recordRightConv = 92
)

// HearingRecord builds a 104 byte hearing profile record with a distinct value in every
// field, so a test can see that fields it did not edit survive a write byte for byte.
func HearingRecord() []byte {
	value := make([]byte, 104)
	copy(value, []byte{2, 2, 0x60, 0})
	put := func(offset int, v float32) { binary.LittleEndian.PutUint32(value[offset:], math.Float32bits(v)) }
	for i := range 8 {
		put(4+4*i, float32(10+i))
		put(52+4*i, float32(20+i))
	}
	put(recordLeftAmp, 0.25)
	put(40, 0.125)
	put(44, 0)
	put(48, 0.5)
	put(recordRightAmp, -0.25)
	put(88, -0.125)
	put(recordRightConv, 1)
	put(96, 0.75)
	put(recordOwnVoice, 0.625)
	return value
}

// TransparencyRecord builds a 104 byte Custom Transparency record.
func TransparencyRecord(enabled bool) []byte {
	value := make([]byte, 104)
	put := func(offset int, v float32) { binary.LittleEndian.PutUint32(value[offset:], math.Float32bits(v)) }
	if enabled {
		put(0, 1)
	}
	for i := range 8 {
		put(4+4*i, float32(30+i)) // Curves no edit may touch.
		put(52+4*i, float32(40+i))
	}
	put(recordLeftAmp, 0.5)
	put(recordRightAmp, 0.5)
	put(48, 0.25)
	put(96, 0.25)
	put(recordOwnVoice, 0.875)
	return value
}

// PMERecord builds a 140 byte media assist record with both enable flags off.
func PMERecord() []byte {
	packet := make([]byte, 140)
	copy(packet, []byte{4, 0, 4, 0, 0x53, 0, 0x84, 0, 2, 1, 2, 2})
	for i := 12; i < 140; i++ {
		packet[i] = byte(i)
	}
	return packet
}

// EarPacket builds an ear status report; status bytes are 0 in ear, 1 out, 2 in case.
func EarPacket(left, right byte) []byte { return []byte{4, 0, 4, 0, 6, 0, left, right} }

// HearingAirPods is enrolled AirPods with hearing assistance off. They follow AAP controls
// and publish reports into the dongle's kept slots the way a connected pair would. Its
// callbacks run while the device lock is held, so they must not call Device methods.
type HearingAirPods struct {
	Server *ATTServer
	Slots  map[byte]CachedReport
	// Silent accepts every control but never publishes a report for it.
	Silent bool
	// SilentSlots does the same for single slots.
	SilentSlots map[byte]bool
	// ANC is the last listening mode requested.
	ANC byte
	// Received holds every AAP packet, in order.
	Received [][]byte
	// Requests counts report requests.
	Requests int
	// BeforeExchange runs before each ATT request is answered.
	BeforeExchange func(pdu []byte)
}

// NewHearingAirPods builds a pair reporting a model and firmware.
func NewHearingAirPods(model, firmware string) *HearingAirPods {
	server := NewAirPodsServer()
	server.Attributes = append(server.Attributes,
		&Attribute{UUID: UUIDLoudSound, Props: 0x0a, Handle: 0x1b, Value: []byte{0}},
		&Attribute{UUID: UUIDCheck, Props: 0x08, Handle: 0x1d, Value: []byte{0}},
		&Attribute{UUID: UUIDHearing, Props: 0x0a, Handle: 0x1f, Value: HearingRecord()},
		&Attribute{UUID: UUIDTransparency, Props: 0x0a, Handle: 0x21, Value: TransparencyRecord(false)})
	a := &HearingAirPods{Server: server, Slots: map[byte]CachedReport{}}
	a.Slots[1] = CachedReport{Age: 1000, Revision: 1, Packet: Metadata(model, firmware, "SIMSERIAL123")}
	a.Slots[2] = CachedReport{Age: 1000, Revision: 1, Packet: []byte{4, 0, 4, 0, 9, 0, 0x2c, 1, 2, 0, 0}}
	a.Slots[3] = CachedReport{Age: 1000, Revision: 1, Packet: []byte{4, 0, 4, 0, 9, 0, 0x33, 2, 0, 0, 0}}
	a.Slots[4] = CachedReport{Age: 1000, Revision: 1, Packet: PMERecord()}
	a.Slots[5] = CachedReport{Age: 1000, Revision: 1, Packet: EarPacket(2, 2)}
	return a
}

// Config is a connected dongle holding these AirPods.
func (a *HearingAirPods) Config() Config {
	cfg := Default()
	cfg.Cached = a.Slots
	cfg.OnAAP = a.onAAP
	cfg.Exchange = func(pdu []byte) []byte {
		if a.BeforeExchange != nil {
			a.BeforeExchange(pdu)
		}
		return a.Server.Handle(pdu)
	}
	return cfg
}

// Report publishes a packet into a slot with a new reception revision.
func (a *HearingAirPods) Report(slot byte, packet []byte) {
	report := a.Slots[slot]
	a.Slots[slot] = CachedReport{Revision: report.Revision + 1, Packet: bytes.Clone(packet)}
}

func (a *HearingAirPods) onAAP(pdu []byte) {
	a.Received = append(a.Received, bytes.Clone(pdu))
	if len(pdu) < 7 || !bytes.Equal(pdu[:4], []byte{4, 0, 4, 0}) {
		return
	}
	slot := byte(0)
	switch {
	case pdu[4] == 0x0f:
		a.Requests++
	case pdu[4] == 9 && pdu[6] == 0x0d && len(pdu) > 7:
		a.ANC = pdu[7]
	case pdu[4] == 9 && pdu[6] == 0x2c:
		slot = 2
	case pdu[4] == 9 && pdu[6] == 0x33:
		slot = 3
	case pdu[4] == 0x53:
		slot = 4
	}
	if slot != 0 && !a.Silent && !a.SilentSlots[slot] {
		a.Report(slot, pdu)
	}
}

var requestNotifications = []byte{4, 0, 4, 0, 0x0f, 0, 0xff, 0xff, 0xff, 0xff}

// aapAllowed is the dongle's fixed list of AAP packets a host may send: the hearing and
// assist switches, ANC or transparency, the media assist record, the report request and
// the host clock. Anything else is refused without being sent.
func aapAllowed(p []byte) bool {
	n := len(p)
	if n < 6 || n > 512 || !bytes.Equal(p[:4], []byte{4, 0, 4, 0}) || p[5] != 0 {
		return false
	}
	switch {
	case p[4] == 9 && n == 11 && p[9] == 0 && p[10] == 0 && p[6] == 0x2c:
		return p[7] <= 2 && (p[8] == 1 || p[8] == 2)
	case p[4] == 9 && n == 11 && p[9] == 0 && p[10] == 0 && p[6] == 0x33:
		return (p[7] == 1 || p[7] == 2) && p[8] == 0
	case p[4] == 9 && n == 11 && p[9] == 0 && p[10] == 0 && p[6] == 0x0d:
		return (p[7] == 2 || p[7] == 3) && p[8] == 0
	case p[4] == 0x53:
		return n == 140 && p[6] == 0x84 && p[7] == 0 && p[8] == 2 && p[9] <= 2 &&
			(p[10] == 1 || p[10] == 2) && (p[11] == 1 || p[11] == 2)
	case p[4] == 0x0f:
		return bytes.Equal(p, requestNotifications)
	case p[4] == 0x1b:
		return hostClockAllowed(p)
	}
	return false
}

// hostClockAllowed checks a host clock packet: 41 bytes, a 25-byte UTC timestamp
// "YYYY-MM-DDTHH:MM:SS+00:00" at byte 16, and microseconds below one second.
func hostClockAllowed(p []byte) bool {
	le := binary.LittleEndian
	if len(p) != 41 || le.Uint16(p[14:]) != 25 || le.Uint32(p[10:]) >= 1000000 || string(p[35:41]) != "+00:00" {
		return false
	}
	for i, c := range p[16:35] {
		switch i {
		case 4, 7:
			if c != '-' {
				return false
			}
		case 10:
			if c != 'T' {
				return false
			}
		case 13, 16:
			if c != ':' {
				return false
			}
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}
