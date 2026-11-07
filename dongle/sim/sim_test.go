package sim

import (
	"encoding/binary"
	"testing"

	"github.com/Dardrai/airpods-core/hid"
)

func frame(op byte, generation, sequence uint32, total, offset uint16, payload ...byte) [hid.ReportSize]byte {
	var data [hid.ReportSize]byte
	header := hid.Header(3)
	copy(data[:], header[:])
	data[7] = op
	le := binary.LittleEndian
	le.PutUint32(data[8:], generation)
	le.PutUint32(data[12:], sequence)
	le.PutUint16(data[16:], total)
	le.PutUint16(data[18:], offset)
	data[20] = byte(len(payload))
	copy(data[22:], payload)
	return data
}

func mustGet(t *testing.T, d *Device, report byte) [hid.ReportSize]byte {
	t.Helper()
	data, err := d.Get(report)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTypedStateMachine(t *testing.T) {
	cfg := Default()
	cfg.Replies = map[byte]Hex{9: make([]byte, 50)}
	cfg.PageSize = 30
	d := New(cfg)
	if err := d.Set(frame(7, 7, 1, 1, 0, 9)); err != nil {
		t.Fatal(err)
	}
	reply := mustGet(t, d, 3)
	if reply[21] != 1 || binary.LittleEndian.Uint16(reply[18:]) != 1 {
		t.Fatalf("stage reply % x", reply[:24])
	}
	d.Set(frame(6, 7, 1, 0, 0))
	reply = mustGet(t, d, 3)
	if reply[21] != 3 || binary.LittleEndian.Uint16(reply[16:]) != 50 || reply[20] != 30 {
		t.Fatalf("first page % x", reply[:24])
	}
	d.Set(frame(4, 7, 1, 0, 30))
	reply = mustGet(t, d, 3)
	if binary.LittleEndian.Uint16(reply[18:]) != 30 || reply[20] != 20 {
		t.Fatalf("second page % x", reply[:24])
	}
	d.Set(frame(5, 7, 1, 0, 0))
	reply = mustGet(t, d, 3)
	if reply[21] != 4 || reply[22] != 8 {
		t.Fatalf("cancel % x", reply[:24])
	}
}

func TestStaleGenerationFails(t *testing.T) {
	d := New(Default())
	d.Set(frame(7, 6, 1, 1, 0, 9))
	if reply := mustGet(t, d, 3); reply[21] != 4 || reply[22] != 3 {
		t.Fatalf("% x", reply[:24])
	}
}

func TestCommitDelayShowsProcessing(t *testing.T) {
	cfg := Default()
	cfg.CommitDelay = 2
	d := New(cfg)
	d.Set(frame(7, 7, 1, 1, 0, 8))
	d.Set(frame(6, 7, 1, 0, 0))
	if reply := mustGet(t, d, 3); reply[21] != 2 {
		t.Fatalf("phase %d", reply[21])
	}
	if reply := mustGet(t, d, 3); reply[21] != 3 {
		t.Fatalf("phase %d", reply[21])
	}
}

func TestStaleControlGetsStaleReceipt(t *testing.T) {
	d := New(Default())
	var data [hid.ReportSize]byte
	header := hid.Header(2)
	copy(data[:], header[:])
	data[7] = 1
	binary.LittleEndian.PutUint32(data[8:], 6)
	copy(data[12:18], []byte{1, 2, 3, 4, 5, 6})
	binary.LittleEndian.PutUint32(data[18:], 9)
	d.Set(data)
	status := mustGet(t, d, 2)
	if binary.LittleEndian.Uint32(status[36:]) != 9 || status[40] != 3 || status[41] != 1 {
		t.Fatalf("receipt % x", status[36:42])
	}
}

func TestParseConfigOverridesDefault(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"flags":1,"replies":{"9":"0102"},"fail":{"2":5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Flags != 1 || cfg.Generation != 7 || string(cfg.Replies[9]) != "\x01\x02" || cfg.Fail[2] != 5 {
		t.Fatalf("%+v", cfg)
	}
}

func TestOpenRespectsSerial(t *testing.T) {
	d := New(Default())
	if _, err := d.Open("SIM0001"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Open(""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Open("OTHER"); err == nil {
		t.Fatal("wrong serial accepted")
	}
}

func TestClosedDeviceFails(t *testing.T) {
	d := New(Default())
	d.Close()
	if _, err := d.Get(2); err == nil {
		t.Fatal("Get after Close succeeded")
	}
	if err := d.Set([hid.ReportSize]byte{}); err == nil {
		t.Fatal("Set after Close succeeded")
	}
}

func TestVirtualClock(t *testing.T) {
	c := NewVirtualClock()
	start := c.Now()
	c.Sleep(1500)
	if c.Now().Sub(start) != 1500 {
		t.Fatal("clock did not advance")
	}
}

func typedReport(op byte, generation uint32, sequence uint32, total, offset uint16, size byte) [hid.ReportSize]byte {
	var data [hid.ReportSize]byte
	header := hid.Header(3)
	copy(data[:], header[:])
	data[7] = op
	le := binary.LittleEndian
	le.PutUint32(data[8:], generation)
	le.PutUint32(data[12:], sequence)
	le.PutUint16(data[16:], total)
	le.PutUint16(data[18:], offset)
	data[20] = size
	return data
}

// TestBadReportsAnswerWithAFailurePhase sends what no client should, which used to panic.
func TestBadReportsAnswerWithAFailurePhase(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   byte
		off  uint16
		size byte
	}{
		{"fragment above the limit", 7, 0, 43},
		{"fragment past the report", 7, 0, 255},
		{"page past the response", 4, 9, 0},
		{"page far past the response", 4, 0xffff, 0},
		{"stage at a wrong offset", 7, 5, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := New(Default())
			if err := device.Set(typedReport(tc.op, 7, 1, 1, tc.off, tc.size)); err != nil {
				t.Fatal(err)
			}
			reply, err := device.Get(3)
			if err != nil {
				t.Fatal(err)
			}
			if reply[21] != 4 || reply[22] != 1 {
				t.Errorf("phase %d code %d", reply[21], reply[22])
			}
		})
	}
}

// TestArbitraryReportsNeverPanic walks every op, size and a spread of offsets, on a device
// that has a response ready to page through.
func TestArbitraryReportsNeverPanic(t *testing.T) {
	device := New(Default())
	for op := 0; op < 256; op++ {
		for size := 0; size < 256; size += 5 {
			for _, offset := range []uint16{0, 1, 42, 43, 512, 4096, 0xffff} {
				if err := device.Set(typedReport(byte(op), 7, 1, uint16(size), offset, byte(size))); err != nil {
					t.Fatal(err)
				}
				if _, err := device.Get(3); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestCancelDropsACommitInProgress(t *testing.T) {
	cfg := Default()
	cfg.CommitDelay = 3
	device := New(cfg)
	stage := typedReport(7, 7, 1, 1, 0, 1)
	stage[22] = 2
	for _, report := range [][hid.ReportSize]byte{stage, typedReport(6, 7, 1, 0, 0, 0), typedReport(5, 7, 1, 0, 0, 0)} {
		if err := device.Set(report); err != nil {
			t.Fatal(err)
		}
	}
	for read := 0; read < 5; read++ {
		reply, err := device.Get(3)
		if err != nil {
			t.Fatal(err)
		}
		if reply[21] != 4 || reply[22] != 8 {
			t.Fatalf("read %d: phase %d code %d", read, reply[21], reply[22])
		}
	}
}

func control(opcode byte, sequence uint32) [hid.ReportSize]byte {
	var data [hid.ReportSize]byte
	header := hid.Header(2)
	copy(data[:], header[:])
	data[7] = opcode
	binary.LittleEndian.PutUint32(data[8:], 7)
	copy(data[12:18], []byte{1, 2, 3, 4, 5, 6})
	binary.LittleEndian.PutUint32(data[18:], sequence)
	return data
}

func TestUnknownOpcodeIsMalformedWithOpcodeZero(t *testing.T) {
	cfg := Default()
	cfg.ControlResults = map[byte]byte{5: 1}
	d := New(cfg)
	d.Set(control(5, 9))
	status := mustGet(t, d, 2)
	if binary.LittleEndian.Uint32(status[36:]) != 9 || status[40] != 6 || status[41] != 0 {
		t.Fatalf("receipt % x", status[36:42])
	}
}

func TestDefaultLooksLikeCurrentFirmware(t *testing.T) {
	status := mustGet(t, New(Default()), 2)
	if got := binary.LittleEndian.Uint16(status[42:]); got != 0x3ff {
		t.Fatalf("capabilities %#x", got)
	}
}

// commitTyped runs one request through stage and commit and returns report 3 after it.
func commitTyped(t *testing.T, d *Device, op byte, sequence uint32, payload ...byte) [hid.ReportSize]byte {
	t.Helper()
	d.Set(frame(op, 7, sequence, uint16(len(payload)), 0, payload...))
	d.Set(frame(6, 7, sequence, 0, 0))
	return mustGet(t, d, 3)
}

func TestCachedSlotSixIsTheSettingsSnapshot(t *testing.T) {
	d := New(Default())
	reply := commitTyped(t, d, 3, 1, 6)
	if reply[21] != 3 || reply[22] != 1 || reply[23] != 4 || binary.LittleEndian.Uint16(reply[16:]) != 2+4*13 {
		t.Fatalf("% x", reply[:30])
	}
	d.SetFlags(0x01)
	if reply := commitTyped(t, d, 3, 2, 6); reply[21] != 3 || binary.LittleEndian.Uint16(reply[16:]) != 0 {
		t.Fatalf("not ready: % x", reply[:24])
	}
	if reply := commitTyped(t, d, 3, 3, 7); reply[21] != 4 || reply[22] != 1 {
		t.Fatalf("slot 7: % x", reply[:24])
	}
}

func TestAAPOutsideTheListIsRefused(t *testing.T) {
	clock := []byte{4, 0, 4, 0, 0x1b, 0, 0, 0, 0, 0, 0x3f, 0x42, 0x0f, 0, 25, 0} // 999999 us
	clock = append(clock, "2026-11-07T12:34:56+00:00"...)
	for _, tc := range []struct {
		name    string
		packet  []byte
		allowed bool
	}{
		{"hearing", []byte{4, 0, 4, 0, 9, 0, 0x2c, 1, 2, 0, 0}, true},
		{"hearing out of range", []byte{4, 0, 4, 0, 9, 0, 0x2c, 3, 2, 0, 0}, false},
		{"assist", []byte{4, 0, 4, 0, 9, 0, 0x33, 1, 0, 0, 0}, true},
		{"transparency", []byte{4, 0, 4, 0, 9, 0, 0x0d, 3, 0, 0, 0}, true},
		{"mode off", []byte{4, 0, 4, 0, 9, 0, 0x0d, 1, 0, 0, 0}, false},
		{"media assist", PMERecord(), true},
		{"report request", requestNotifications, true},
		{"host clock", clock, true},
		{"host clock in a zone", append(append([]byte(nil), clock[:35]...), "+01:00"...), false},
		{"eq", []byte{4, 0, 4, 0, 0x63, 0, 5, 0, 1, 2, 10, 50, 100}, false},
		{"rename", []byte{4, 0, 4, 0, 0x1a, 0, 1, 4, 0, 'A', 'l', 'e', 'x'}, false},
	} {
		if got := aapAllowed(tc.packet); got != tc.allowed {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	if len(clock) != 41 {
		t.Fatalf("clock is %d bytes", len(clock))
	}
}
