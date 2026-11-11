package sim

import "encoding/binary"

// routingState is the dongle's routing handover state behind kinds 20 and 21.
type routingState struct {
	revision   uint32 // intent revision, never 0
	nonce      uint32 // nonce of the last accepted handover
	action     byte   // last accepted handover: 0 none, 1 release, 2 acquire
	released   bool   // a release holds until the next takeover request
	closing    bool   // the release is accepted but the connection is still open
	closeIn    int
	takeover   bool // an acquire is pending
	takeoverIn int
}

func (r *routingState) advance(nonce uint32, action byte) {
	r.revision++
	if r.revision == 0 {
		r.revision = 1
	}
	r.nonce, r.action = nonce, action
}

// routingRequest serves kind 20 (status) and kind 21 (handover) with revision and nonce fencing.
func (d *Device) routingRequest(request []byte) ([]byte, byte) {
	r := &d.routing
	if request[0] == 20 {
		if len(request) != 1 {
			return nil, 1
		}
		d.routingTick()
		snapshot := d.routingSnapshot()
		d.automaticAttempt()
		return snapshot, 0
	}
	le := binary.LittleEndian
	if len(request) != 10 || request[1] > 1 || le.Uint32(request[2:]) == 0 || le.Uint32(request[6:]) == 0 {
		return nil, 1
	}
	action, revision, nonce := request[1], le.Uint32(request[2:]), le.Uint32(request[6:])
	switch {
	case d.leaseHeld:
		return nil, 2 // Any settings lease blocks routing writes, the owner's too.
	case revision != r.revision || nonce == r.nonce:
		return nil, 3
	case !d.hasPeer():
		return nil, 4
	case d.cfg.RoutingBusy || r.takeover || r.closing || d.cfg.Flags&16 != 0:
		return nil, 2
	}
	if action == 1 {
		r.advance(nonce, 2)
		r.released, r.closing = false, false
		r.takeover, r.takeoverIn = true, d.cfg.RoutingTakeoverDelay
		if r.takeoverIn == 0 {
			d.finishTakeover()
		}
	} else {
		r.advance(nonce, 1)
		r.released, r.closing, r.closeIn = true, true, d.cfg.RoutingCloseDelay
		if r.closeIn == 0 {
			d.finishClose()
		}
	}
	return d.routingSnapshot(), 0
}

// takeoverRequested is opcode 1: a new manual choice that ends a release.
func (d *Device) takeoverRequested() {
	r := &d.routing
	r.advance(0, 0)
	if r.released {
		r.released, r.closing = false, false
		d.connect()
	}
}

// automaticAttempt is the Automatic reconnect policy: while the AirPods are not connected and
// not released, the dongle starts a takeover by itself, which moves the routing revision. It
// runs right after a routing read, so the revision that read returned is already stale.
func (d *Device) automaticAttempt() {
	r := &d.routing
	if d.cfg.ReconnectPolicy != 1 || d.policyFailed || d.cfg.Flags&1 != 0 || r.released || r.takeover || !d.hasPeer() {
		return
	}
	r.advance(0, 0)
	r.takeover, r.takeoverIn = true, d.cfg.RoutingTakeoverDelay // A connection takes a while.
}

func (d *Device) routingTick() {
	r := &d.routing
	if r.closing {
		if r.closeIn > 0 {
			r.closeIn--
		} else {
			d.finishClose()
		}
	}
	if r.takeover {
		if r.takeoverIn > 0 {
			r.takeoverIn--
		} else {
			d.finishTakeover()
		}
	}
}

// finishClose ends the AirPods connection, which starts a new generation.
func (d *Device) finishClose() {
	d.routing.closing = false
	if d.cfg.Flags&1 != 0 {
		d.cfg.Flags &^= 0x07
		d.cfg.Generation++
	}
}

func (d *Device) finishTakeover() {
	d.routing.takeover = false
	d.connect()
}

func (d *Device) connect() {
	if d.cfg.Flags&1 == 0 {
		d.cfg.Flags |= 0x03
		d.cfg.Generation++
	}
}

func (d *Device) hasPeer() bool {
	return len(d.cfg.Peer) == 6 && string(d.cfg.Peer) != string(make([]byte, 6))
}

func (d *Device) routingSnapshot() []byte {
	r := d.routing
	connected, ready := d.cfg.Flags&1 != 0, d.cfg.Flags&2 != 0
	var flags byte
	for bit, set := range map[byte]bool{
		0x01: ready,
		0x02: connected && !r.released && !r.takeover,
		0x04: r.takeover,
		0x08: r.released && !r.closing,
		0x10: r.released && r.closing,
		0x20: connected,
		0x40: ready,
		0x80: d.cfg.Flags&16 != 0,
	} {
		if set {
			flags |= bit
		}
	}
	le := binary.LittleEndian
	out := make([]byte, 32)
	out[0], out[1], out[2] = 1, 32, flags
	le.PutUint32(out[4:], r.revision)
	le.PutUint32(out[8:], d.cfg.Generation)
	copy(out[12:18], d.cfg.Peer)
	out[18] = r.action
	le.PutUint32(out[28:], r.nonce)
	return out
}
