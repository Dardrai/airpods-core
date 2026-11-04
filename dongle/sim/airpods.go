package sim

import (
	"encoding/binary"
)

// Attribute is one ATT characteristic the simulated AirPods expose.
type Attribute struct {
	UUID    [16]byte
	Props   byte
	Handle  uint16
	Value   []byte
	OnWrite func(value []byte)
	// Pending holds a value that replaces Value after this many reads.
	AppliesAfterReads int
	next              []byte
}

// ATTServer answers ATT requests from a list of attributes. Handles must ascend, with
// the attribute's declaration at Handle-1.
type ATTServer struct {
	Service    [16]byte
	Attributes []*Attribute
	// RefuseWrites answers writes with an error instead of applying them.
	RefuseWrites bool
	// Unreachable makes every request fail, as a dongle that passes nothing through would.
	Unreachable bool
	// LastWrite is the opcode and value of the last write, for tests.
	LastWrite []byte
	Writes    int
}

func attErrorReply(op byte, handle uint16, code byte) []byte {
	return []byte{1, op, byte(handle), byte(handle >> 8), code}
}

// Find returns the attribute with a UUID.
func (s *ATTServer) Find(uuid [16]byte) *Attribute {
	for _, a := range s.Attributes {
		if a.UUID == uuid {
			return a
		}
	}
	return nil
}

func (s *ATTServer) range16() (uint16, uint16) {
	first, last := uint16(0x10), uint16(0x11)
	if n := len(s.Attributes); n > 0 {
		first, last = s.Attributes[0].Handle-2, s.Attributes[n-1].Handle
	}
	return first, last
}

// Handle answers one request.
func (s *ATTServer) Handle(pdu []byte) []byte {
	if len(pdu) == 0 {
		return nil
	}
	if s.Unreachable {
		return attErrorReply(pdu[0], 0, 0x0e)
	}
	le := binary.LittleEndian
	first, last := s.range16()
	switch pdu[0] {
	case 0x06:
		if len(pdu) >= 23 && [16]byte(pdu[7:23]) == s.Service {
			out := []byte{0x07}
			out = le.AppendUint16(out, first)
			return le.AppendUint16(out, last)
		}
		return attErrorReply(0x06, le.Uint16(pdu[1:]), 0x0a)
	case 0x08:
		start := le.Uint16(pdu[1:])
		out := []byte{0x09, 21}
		for _, a := range s.Attributes {
			if a.Handle-1 >= start {
				out = le.AppendUint16(out, a.Handle-1)
				out = append(out, a.Props)
				out = le.AppendUint16(out, a.Handle)
				out = append(out, a.UUID[:]...)
			}
		}
		if len(out) == 2 {
			return attErrorReply(0x08, start, 0x0a)
		}
		return out
	case 0x0a:
		handle := le.Uint16(pdu[1:])
		for _, a := range s.Attributes {
			if a.Handle == handle {
				if a.AppliesAfterReads > 0 {
					a.AppliesAfterReads--
					if a.AppliesAfterReads == 0 && a.next != nil {
						a.Value, a.next = a.next, nil
					}
				}
				return append([]byte{0x0b}, a.Value...)
			}
		}
		return attErrorReply(0x0a, handle, 0x01)
	case 0x12, 0x52:
		handle := le.Uint16(pdu[1:])
		if s.RefuseWrites {
			return attErrorReply(pdu[0], handle, 0x03)
		}
		for _, a := range s.Attributes {
			if a.Handle != handle {
				continue
			}
			s.Writes++
			s.LastWrite = append([]byte{pdu[0]}, pdu[3:]...)
			value := append([]byte(nil), pdu[3:]...)
			if a.AppliesAfterReads > 0 {
				a.next = value
			} else {
				a.Value = value
			}
			if a.OnWrite != nil {
				a.OnWrite(value)
			}
			if pdu[0] == 0x12 {
				return []byte{0x13}
			}
			return nil
		}
		return attErrorReply(pdu[0], handle, 0x01)
	}
	return attErrorReply(pdu[0], 0, 0x06)
}

// DiagnosticRecord builds a 71 byte saved self-check record.
func DiagnosticRecord(leftTime, rightTime uint64, leftWord, rightWord uint32) []byte {
	le := binary.LittleEndian
	value := make([]byte, 71)
	value[0] = 8
	le.PutUint64(value[1:], leftTime)
	le.PutUint64(value[9:], rightTime)
	le.PutUint32(value[17:], leftWord)
	le.PutUint32(value[21:], rightWord)
	le.PutUint32(value[33:], 1650000000)
	le.PutUint32(value[37:], 1650000000)
	return value
}

func reverse16(in [16]byte) [16]byte {
	var out [16]byte
	for i := range in {
		out[15-i] = in[i]
	}
	return out
}

func printedUUID(prefix uint32, rest [12]byte) [16]byte {
	var printed [16]byte
	binary.BigEndian.PutUint32(printed[:], prefix)
	copy(printed[4:], rest[:])
	return reverse16(printed)
}

var ahpsSuffix = [12]byte{0x95, 0xc5, 0x4d, 0x6f, 0x90, 0x98, 0x0f, 0x0b, 0x41, 0x45, 0x7e, 0x0a}

// Well-known simulated attribute UUIDs, in wire order.
var (
	UUIDTestMode    = printedUUID(0xa4120001, ahpsSuffix)
	UUIDDiagnostics = printedUUID(0xa4120002, ahpsSuffix)
	UUIDCheck       = printedUUID(0xa4120004, ahpsSuffix)
	UUIDHearing     = printedUUID(0xa4120005, ahpsSuffix)
	UUIDSensitivity = printedUUID(0x82f6bc0a, [12]byte{0x1b, 0xca, 0x48, 0x73, 0xaf, 0xc9, 0xec, 0x58, 0x90, 0xe3, 0x46, 0x9a})
	UUIDCurve       = printedUUID(0xd5f96afa, [12]byte{0x2f, 0x2c, 0x41, 0xbb, 0xa7, 0xe6, 0xf5, 0x4a, 0xbe, 0x62, 0x35, 0xb4})
)

// ServiceUUID is the AirPods settings service in wire order.
var ServiceUUID = [16]byte{0x4e, 0xe0, 0xef, 0x92, 0x34, 0x56, 0x33, 0x99, 0xa6, 0x45, 0xb7, 0xb7, 0x2b, 0x08, 0x98, 0x77}

// NewAirPodsServer builds the demo AirPods' attributes.
func NewAirPodsServer() *ATTServer {
	curve := make([]byte, 17*4)
	for i := range 17 {
		binary.LittleEndian.PutUint32(curve[i*4:], 0x3f000000) // 0.5
	}
	return &ATTServer{Service: ServiceUUID, Attributes: []*Attribute{
		{UUID: UUIDTestMode, Props: 0x02, Handle: 0x13, Value: []byte{0}},
		{UUID: UUIDDiagnostics, Props: 0x02, Handle: 0x15, Value: DiagnosticRecord(1750000000, 1750000300, 0, 1<<9)},
		{UUID: UUIDSensitivity, Props: 0x02, Handle: 0x17, Value: []byte{0, 0, 0x80, 0x3f}},
		{UUID: UUIDCurve, Props: 0x02, Handle: 0x19, Value: curve},
	}}
}
