// Package hearing reads and changes the AirPods' hearing data through the dongle. Every
// change here is read first and confirmed by reading again; nothing is ever retried.
//
// UNVERIFIED ON HARDWARE: the mode, profile, in-case check and experimental code has only
// run against the simulator in dongle/sim. Packet formats come from the documented
// behaviour of the protocol, not from observing real AirPods through this dongle.
package hearing

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Dardrai/airpods-core/dongle"
)

// Link is the part of a dongle session this package talks through.
type Link interface {
	Exchange(pdu []byte) ([]byte, error)
	SendAAP(pdu []byte) error
	Cached(slot byte) (*dongle.Cached, error)
}

// UUID is an ATT UUID in wire order, which is reversed from its printed form.
type UUID [16]byte

// ParseUUID reads the printed hex form, without dashes.
func ParseUUID(printed string) UUID {
	raw, err := hex.DecodeString(printed)
	if err != nil || len(raw) != 16 {
		panic("bad UUID: " + printed)
	}
	var out UUID
	for i, b := range raw {
		out[15-i] = b
	}
	return out
}

// Service is the AirPods' settings service.
var Service = UUID{0x4e, 0xe0, 0xef, 0x92, 0x34, 0x56, 0x33, 0x99, 0xa6, 0x45, 0xb7, 0xb7, 0x2b, 0x08, 0x98, 0x77}

// Characteristic is one discovered attribute.
type Characteristic struct {
	Handle uint16
	// WriteOpcode is 0x12 (with response), 0x52 (without), or 0 when not writable.
	WriteOpcode byte
}

const (
	attError    = 0x01
	attFindType = 0x06
	attFound    = 0x07
	attByType   = 0x08
	attByTypeR  = 0x09
	attRead     = 0x0a
	attReadR    = 0x0b
	attWrite    = 0x12
	attWriteR   = 0x13
	attCommand  = 0x52
	propRead    = 0x02
	propCommand = 0x04
	propWrite   = 0x08
	errNotFound = 0x0a
)

// noAttribute recognises the end of a search. Some AirPods name the last handle they
// searched rather than the start of the request, so anywhere in the range counts.
func noAttribute(response, request []byte) bool {
	if len(request) < 5 || (request[0] != attFindType && request[0] != attByType) || len(response) != 5 {
		return false
	}
	if response[0] != attError || response[1] != request[0] || response[4] != errNotFound {
		return false
	}
	le := binary.LittleEndian
	start, end, handle := le.Uint16(request[1:]), le.Uint16(request[3:]), le.Uint16(response[2:])
	return start != 0 && handle >= start && handle <= end
}

// Exchanger sends one ATT request and returns the response.
type Exchanger func(pdu []byte) ([]byte, error)

func discover(exchange Exchanger, wanted []UUID, name string, required byte) ([]*Characteristic, error) {
	found := make([]*Characteristic, len(wanted))
	request := append([]byte{attFindType, 0x01, 0x00, 0xff, 0xff, 0x00, 0x28}, Service[:]...)
	service, err := exchange(request)
	if err != nil {
		return nil, err
	}
	if noAttribute(service, request) {
		return found, nil
	}
	if len(service) != 5 || service[0] != attFound {
		return nil, attFailure(name+" service discovery failed.", request, service)
	}
	le := binary.LittleEndian
	start, end := le.Uint16(service[1:]), le.Uint16(service[3:])
	if start == 0 || start >= end {
		return nil, fmt.Errorf("%s service range is invalid.", name)
	}
	var previousValue uint16
	seen := map[UUID]bool{} // Any characteristic listed twice makes the whole service untrustworthy.
	for range 16 {
		request := []byte{attByType, byte(start), byte(start >> 8), byte(end), byte(end >> 8), 0x03, 0x28}
		response, err := exchange(request)
		if err != nil {
			return nil, err
		}
		if noAttribute(response, request) {
			return found, nil
		}
		if len(response) < 2 || response[0] != attByTypeR || (response[1] != 7 && response[1] != 21) {
			return nil, attFailure(name+" characteristic discovery failed.", request, response)
		}
		each, entries := int(response[1]), response[2:]
		if len(entries) == 0 || len(entries)%each != 0 {
			return nil, fmt.Errorf("%s characteristic list is incomplete.", name)
		}
		previous := start - 1
		for ; len(entries) > 0; entries = entries[each:] {
			entry := entries[:each]
			declaration, handle := le.Uint16(entry), le.Uint16(entry[3:])
			if declaration <= previous || declaration <= previousValue || declaration > end || handle <= declaration || handle > end {
				return nil, fmt.Errorf("%s characteristic range is invalid.", name)
			}
			if each == 21 {
				if seen[UUID(entry[5:])] {
					return nil, fmt.Errorf("%s discovery found a characteristic listed twice.", name)
				}
				seen[UUID(entry[5:])] = true
			}
			for index, uuid := range wanted {
				if !bytes.Equal(entry[5:], uuid[:]) {
					continue
				}
				if found[index] != nil || entry[2]&required != required {
					access := "readable"
					if required != propRead {
						access = "write-request"
					}
					return nil, fmt.Errorf("%s does not expose a unique %s setting.", name, access)
				}
				c := &Characteristic{Handle: handle}
				switch {
				case entry[2]&propWrite != 0:
					c.WriteOpcode = attWrite
				case entry[2]&propCommand != 0:
					c.WriteOpcode = attCommand
				}
				found[index] = c
			}
			previous, previousValue = declaration, handle
		}
		if previous == end {
			return found, nil
		}
		start = previous + 1
	}
	return nil, fmt.Errorf("%s discovery exceeded the service limit.", name)
}

// DiscoverRead finds readable characteristics; a nil entry was not found.
func DiscoverRead(exchange Exchanger, name string, wanted ...UUID) ([]*Characteristic, error) {
	return discover(exchange, wanted, name, propRead)
}

// answers reports whether an ATT error reply belongs to request: it must name the request's
// opcode, and its handle must be the request's handle or, for a search, lie inside its range.
func answers(request, response []byte) bool {
	if len(response) != 5 || response[0] != attError || len(request) < 3 || response[1] != request[0] {
		return false
	}
	le := binary.LittleEndian
	start, handle := le.Uint16(request[1:]), le.Uint16(response[2:])
	end := start
	if (request[0] == attFindType || request[0] == attByType) && len(request) >= 5 {
		end = le.Uint16(request[3:])
	}
	return handle >= start && handle <= end
}

// attFailure words a reply to request that is not the one expected. An ATT error is quoted
// only when it answers this request; one that names another request or handle says so.
func attFailure(context string, request, response []byte) error {
	if len(response) == 0 || response[0] != attError {
		return errors.New(context)
	}
	if !answers(request, response) {
		return fmt.Errorf("%s The AirPods answered a different ATT request.", context)
	}
	return fmt.Errorf("%s ATT request 0x%02X, handle 0x%04X, error 0x%02X.", context, response[1], binary.LittleEndian.Uint16(response[2:]), response[4])
}

func readRequest(handle uint16) []byte { return []byte{attRead, byte(handle), byte(handle >> 8)} }

// maxReadValue is the longest attribute value a read may return; a read must return at least
// one byte.
const maxReadValue = 510

// readValue reads an attribute's value, which must be 1 to maxReadValue bytes.
func readValue(exchange Exchanger, handle uint16, context string) ([]byte, error) {
	request := readRequest(handle)
	response, err := exchange(request)
	if err != nil {
		return nil, err
	}
	if len(response) < 1 || response[0] != attReadR {
		return nil, attFailure(context, request, response)
	}
	if len(response) < 2 || len(response) > maxReadValue+1 {
		return nil, fmt.Errorf("%s The AirPods returned a value of %d bytes.", context, len(response)-1)
	}
	return response[1:], nil
}

// writeRequest builds a write of value with the opcode the characteristic advertises.
func writeRequest(c Characteristic, value []byte) []byte {
	return append([]byte{c.WriteOpcode, byte(c.Handle), byte(c.Handle >> 8)}, value...)
}

// writeAcknowledged checks the reply to a write: a write request must answer with exactly the
// write response, a write command with nothing at all.
func writeAcknowledged(context string, request, response []byte) error {
	if request[0] == attWrite && bytes.Equal(response, []byte{attWriteR}) || request[0] == attCommand && len(response) == 0 {
		return nil
	}
	return attFailure(context, request, response)
}
