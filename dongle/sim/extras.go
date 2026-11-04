package sim

import "encoding/binary"

// Update changes the running device's config, for example its batteries.
func (d *Device) Update(change func(*Config)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	change(&d.cfg)
}

// Counts returns how many report 2 reads, report 3 reads and writes the device saw.
func (d *Device) Counts() (status, rpc, sets int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, line := range d.trace {
		switch {
		case line == "G2":
			status++
		case line == "G3":
			rpc++
		case line[0] == 'S':
			sets++
		}
	}
	return status, rpc, sets
}

// Demo is a fully populated dongle for trying the UI without hardware.
func Demo() Config {
	cfg := Default()
	cfg.Flags = 0x07
	cfg.Peer = Hex{0x4c, 0x56, 0x9d, 0x12, 0x34, 0x56}
	cfg.Battery = [3]byte{80, 75, 40}
	cfg.Ages = [3]uint32{1000, 1000, 5000}
	cfg.Present = 7
	cfg.Settings = nil
	for _, s := range []struct {
		control byte
		value   uint32
	}{
		{0x0d, 2}, {0x01, 0}, {0x0a, 2}, {0x1b, 1}, {0x28, 1}, {0x26, 2}, {0x34, 2},
		{0x35, 1}, {0x31, 1}, {0x3b, 1}, {0x25, 1}, {0x17, 0}, {0x18, 0}, {0x23, 0},
		{0x24, 0x0300}, {0x1c, 2}, {0x1f, 0x5032}, {0x40, 70}, {0x2e, 50}, {0x1a, 6},
		{0x16, 0x0501}, {0x20, 1},
	} {
		cfg.Settings = append(cfg.Settings, Setting{Control: s.control, Age: 900, Revision: 3, Value: s.value})
	}
	le := binary.LittleEndian
	nearby := make([]byte, 24)
	nearby[0], nearby[1], nearby[2], nearby[3] = 1, 0x81, 0xc6, 1 // active, verified, -58 dBm, stronger
	le.PutUint32(nearby[4:], 800)
	q8 := int32(-58 * 256)
	le.PutUint32(nearby[20:], uint32(q8))
	performance := make([]byte, 64)
	performance[0], performance[1], performance[2] = 1, 1, 2
	for offset, value := range map[int]uint32{
		4: 3725000, 8: 960, 12: 4000, 16: 2210, 20: 3, 24: 0, 28: 182400, 32: 412, 36: 1890,
		40: 0, 44: 1, 48: 262144, 52: 120832, 56: 141312, 60: 196608,
	} {
		le.PutUint32(performance[offset:], value)
	}
	audio := defaultAudio()
	audio[15] = 8 // game mode supported
	// The hearing pair makes the experimental sections work too. Slots 2 to 4 hold its mode
	// reports; slot 5 is the ear status auto-pause follows, both buds in the ears here.
	pair := NewHearingAirPods("A3063", "9A348")
	pair.Slots[1] = CachedReport{Age: 4000, Revision: 2, Packet: Metadata("A3063", "9A348", "SIMSERIAL123")}
	pair.Slots[5] = CachedReport{Age: 1000, Revision: 1, Packet: EarPacket(0, 0)}
	cfg.Exchange = pair.Server.Handle
	cfg.Cached = pair.Slots
	cfg.OnAAP = pair.onAAP
	cfg.Replies = map[byte]Hex{3: audio, 4: audio, 5: performance, 7: nearby, 9: audio}
	return cfg
}

// Metadata builds the AirPods' information packet the way it arrives over the air.
func Metadata(model, firmware, serial string) []byte {
	packet := []byte{4, 0, 4, 0, 0x1d, 0, 0, 0, 0, 0, 0}
	fields := []string{"AirPods", model, "Apple Inc.", serial, firmware, "", "", "", "", "", firmware}
	for _, field := range fields {
		packet = append(append(packet, field...), 0)
	}
	return packet
}

// Exchanges returns every ATT request the AirPods received, in order.
func (d *Device) Exchanges() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]byte(nil), d.exchanges...)
}

// AAPSent returns every AAP packet the AirPods received, in order.
func (d *Device) AAPSent() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]byte(nil), d.aapSent...)
}

// LeaseEvents lists what happened to the settings lease, in order.
func (d *Device) LeaseEvents() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.leaseEvents...)
}

// LeaseHeld reports whether a client holds the settings lease.
func (d *Device) LeaseHeld() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.leaseHeld
}
