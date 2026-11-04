package dongle

import (
	"encoding/binary"
	"errors"
)

// Report slots of the dongle's kept AirPods reports.
const (
	SlotMetadata     byte = 1
	SlotHearingAid   byte = 2
	SlotHearingMedia byte = 3
	SlotHearingPME   byte = 4
	SlotEarStatus    byte = 5
)

// Cached is one kept report. Age and revision are separate: the same report can age
// without a new reception.
type Cached struct {
	AgeMs    uint32
	Revision uint32
	Packet   []byte
}

const maxCached = 512

// DecodeCached parses a kept-report reply; an empty reply means the slot holds nothing.
func DecodeCached(reply []byte) (*Cached, error) {
	if len(reply) == 0 {
		return nil, nil
	}
	if len(reply) < 9 || len(reply) > maxCached {
		return nil, errors.New("Incomplete dongle report.")
	}
	le := binary.LittleEndian
	revision := le.Uint32(reply[4:])
	if revision == 0 {
		return nil, errors.New("Dongle report has no reception revision.")
	}
	return &Cached{AgeMs: le.Uint32(reply), Revision: revision, Packet: append([]byte(nil), reply[8:]...)}, nil
}

// Cached reads a kept report; nil means the slot holds nothing.
func (c *Client) Cached(slot byte) (*Cached, error) {
	if slot < SlotMetadata || slot > SlotEarStatus {
		return nil, errors.New("Unknown dongle report.")
	}
	c.mu.Lock()
	reply, err := typedRequest(c.dev, c.clock, opCached, []byte{slot}, nil)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return DecodeCached(reply)
}
