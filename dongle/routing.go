package dongle

import (
	"encoding/binary"
	"errors"
)

// Typed request kinds of the routing handover (capability CapRouting).
const (
	kindRouting  = 20
	kindHandover = 21
)

// Routing flag bits (Routing.Flags).
const (
	RoutingKnown           byte = 0x01 // the AirPods have said who owns them
	RoutingOwned           byte = 0x02 // the dongle owns the AirPods' audio
	RoutingTakeoverPending byte = 0x04
	RoutingReleased        byte = 0x08 // released and fully closed
	RoutingReleasePending  byte = 0x10 // release accepted, connection not closed yet
	RoutingA2DP            byte = 0x20
	RoutingControlsReady   byte = 0x40
	RoutingMicrophoneBusy  byte = 0x80 // microphone or call in use
)

// Handover actions of kind 21.
const (
	HandoverRelease byte = 0
	HandoverAcquire byte = 1
)

// Routing is the kind 20 routing snapshot, also the reply to an accepted kind 21.
type Routing struct {
	Flags          byte
	TakeoverStage  byte
	Revision       uint32 // the intent revision a handover must quote; never 0
	ControlSession uint32
	Peer           [6]byte
	LastAction     byte // last accepted handover: 0 none, 1 release, 2 acquire
	UptimeMs       uint32
	TakeoverAgeMs  uint32 // 0 unless a takeover is pending
	LastNonce      uint32 // nonce of the last accepted handover, 0 if none
}

// Has reports whether every given routing flag is set.
func (r Routing) Has(flag byte) bool { return r.Flags&flag == flag }

// DecodeRouting validates and decodes a 32-byte routing snapshot.
func DecodeRouting(b []byte) (Routing, error) {
	le := binary.LittleEndian
	if len(b) != 32 || b[0] != 1 || b[1] != 32 || b[18] > 2 || le.Uint32(b[4:]) == 0 {
		return Routing{}, errors.New("Unsupported dongle routing snapshot.")
	}
	return Routing{
		Flags:          b[2],
		TakeoverStage:  b[3],
		Revision:       le.Uint32(b[4:]),
		ControlSession: le.Uint32(b[8:]),
		Peer:           [6]byte(b[12:18]),
		LastAction:     b[18],
		UptimeMs:       le.Uint32(b[20:]),
		TakeoverAgeMs:  le.Uint32(b[24:]),
		LastNonce:      le.Uint32(b[28:]),
	}, nil
}

// Routing reads the routing snapshot (kind 20). It is read-only and safe to repeat.
func (c *Client) Routing() (Routing, error) {
	reply, err := c.TypedRequest([]byte{kindRouting}, nil)
	if err != nil {
		return Routing{}, err
	}
	return DecodeRouting(reply)
}

// HandoverRequest builds a kind 21 request. Revision is the one from a fresh snapshot;
// nonce must be nonzero and differ from that snapshot's LastNonce.
func HandoverRequest(action byte, revision, nonce uint32) []byte {
	request := []byte{kindHandover, action, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(request[2:], revision)
	binary.LittleEndian.PutUint32(request[6:], nonce)
	return request
}

// Handover sends one kind 21 request and returns the snapshot of its acceptance. Code 3
// means the intent changed since the revision was read: never send it again as is.
func (c *Client) Handover(action byte, revision, nonce uint32) (Routing, error) {
	if action > HandoverAcquire || revision == 0 || nonce == 0 {
		return Routing{}, errors.New("Invalid routing handover request.")
	}
	reply, err := c.TypedRequest(HandoverRequest(action, revision, nonce), nil)
	if err != nil {
		return Routing{}, err
	}
	return DecodeRouting(reply)
}
