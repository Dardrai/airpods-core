package hid

import (
	"errors"
	"testing"
)

func TestCheckReport(t *testing.T) {
	var data [ReportSize]byte
	copy(data[:], []byte{2, 'A', 'P', 'D', 'G', 1, 64})
	if err := CheckReport(2, data); err != nil {
		t.Fatalf("valid report: %v", err)
	}
	if err := CheckReport(3, data); !errors.Is(err, ErrUnsupportedReport) {
		t.Fatalf("wrong report id: %v", err)
	}
	if ErrUnsupportedReport.Error() != "Unsupported HID report identity/version" {
		t.Fatalf("text changed: %q", ErrUnsupportedReport)
	}
	for _, offset := range []int{0, 1, 4, 5, 6} {
		bad := data
		bad[offset] ^= 0xff
		if err := CheckReport(2, bad); !errors.Is(err, ErrUnsupportedReport) {
			t.Fatalf("offset %d accepted", offset)
		}
	}
}

func TestExactlyOne(t *testing.T) {
	var closed []int
	closeFn := func(v int) { closed = append(closed, v) }
	if v, err := exactlyOne([]int{7}, closeFn, "x"); err != nil || v != 7 || len(closed) != 0 {
		t.Fatalf("one match: %d %v %v", v, err, closed)
	}
	if _, err := exactlyOne(nil, closeFn, "none"); err == nil || err.Error() != "none" {
		t.Fatalf("no match: %v", err)
	}
	if _, err := exactlyOne([]int{1, 2}, closeFn, "many"); err == nil || err.Error() != "many" {
		t.Fatalf("two matches: %v", err)
	}
	if len(closed) != 2 {
		t.Fatalf("candidates not closed: %v", closed)
	}
}

func TestIsProduct(t *testing.T) {
	for _, tc := range []struct {
		vid, pid uint16
		want     bool
	}{{0xcafe, 0x4015, true}, {0xcafe, 0x4005, true}, {0xcafe, 0x4006, false}, {0xcaff, 0x4015, false}} {
		if got := isProduct(tc.vid, tc.pid); got != tc.want {
			t.Errorf("%04x:%04x = %v", tc.vid, tc.pid, got)
		}
	}
}
