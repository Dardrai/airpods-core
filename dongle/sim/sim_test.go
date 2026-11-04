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
