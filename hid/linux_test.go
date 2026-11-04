//go:build linux

package hid

import (
	"errors"
	"os"
	"reflect"
	"syscall"
	"testing"
)

// Compile-time: each constant must equal its documented kernel value.
var (
	_ [0]struct{} = [hidiocGRawInfo - 0x80084803]struct{}{}
	_ [0]struct{} = [hidiocGRawUniq - 0x81004808]struct{}{}
	_ [0]struct{} = [hidiocGFeature - 0xC0404807]struct{}{}
	_ [0]struct{} = [hidiocSFeature - 0xC0404806]struct{}{}
)

func TestHidiocConstantsMatchIOCMacro(t *testing.T) {
	const read, write = 2, 1
	for _, tc := range []struct {
		name          string
		got, expected uintptr
	}{
		{"HIDIOCGRAWINFO", ioc(read, 'H', 0x03, 8), hidiocGRawInfo},
		{"HIDIOCGRAWUNIQ(256)", ioc(read, 'H', 0x08, 256), hidiocGRawUniq},
		{"HIDIOCGFEATURE(64)", ioc(read|write, 'H', 0x07, 64), hidiocGFeature},
		{"HIDIOCSFEATURE(64)", ioc(read|write, 'H', 0x06, 64), hidiocSFeature},
	} {
		if tc.got != tc.expected {
			t.Errorf("%s = %#x, want %#x", tc.name, tc.got, tc.expected)
		}
	}
}

type fakeNode struct {
	vid, pid uint16
	infoErr  error
	serial   string
	uniqErr  error
	closed   *int
}

func (n fakeNode) info() (uint16, uint16, error) { return n.vid, n.pid, n.infoErr }
func (n fakeNode) uniq() (string, error)         { return n.serial, n.uniqErr }
func (n fakeNode) close()                        { *n.closed++ }

func TestSelectNodes(t *testing.T) {
	const noMatch = "Expected exactly one matching Pico HID interface (CAFE:4015/4005)"
	boom := errors.New("boom")
	for _, tc := range []struct {
		name   string
		nodes  map[string]any // fakeNode or error from open
		serial string
		want   string // chosen serial, empty on failure
		closed int
	}{
		{"none", map[string]any{}, "", "", 0},
		{"open failure is skipped", map[string]any{"a": boom, "b": fakeNode{vid: 0xcafe, pid: 0x4015, serial: "S1"}}, "", "S1", 0},
		{"info failure is skipped", map[string]any{"a": fakeNode{infoErr: boom}, "b": fakeNode{vid: 0xcafe, pid: 0x4005, serial: "S1"}}, "", "S1", 1},
		{"other product is skipped", map[string]any{"a": fakeNode{vid: 0x1234, pid: 1}, "b": fakeNode{vid: 0xcafe, pid: 0x4005, serial: "S1"}}, "", "S1", 1},
		{"two matches fail", map[string]any{"a": fakeNode{vid: 0xcafe, pid: 0x4015}, "b": fakeNode{vid: 0xcafe, pid: 0x4005}}, "", "", 2},
		{"serial selects", map[string]any{"a": fakeNode{vid: 0xcafe, pid: 0x4015, serial: "S1"}, "b": fakeNode{vid: 0xcafe, pid: 0x4005, serial: "S2"}}, "S2", "S2", 1},
		{"serial uniq failure is skipped", map[string]any{"a": fakeNode{vid: 0xcafe, pid: 0x4015, uniqErr: boom}}, "S1", "", 1},
		{"uniq failure ignored unfiltered", map[string]any{"a": fakeNode{vid: 0xcafe, pid: 0x4015, uniqErr: boom, serial: ""}}, "", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closed := 0
			paths := make([]string, 0, len(tc.nodes))
			for p := range tc.nodes {
				paths = append(paths, p)
			}
			node, serial, err := selectNodes(paths, tc.serial, func(p string) (rawNode, error) {
				switch v := tc.nodes[p].(type) {
				case error:
					return nil, v
				case fakeNode:
					v.closed = &closed
					return v, nil
				}
				return nil, boom
			})
			if tc.name == "uniq failure ignored unfiltered" {
				if err != nil || node == nil {
					t.Fatalf("unfiltered open must succeed: %v", err)
				}
				return
			}
			if tc.want == "" {
				if err == nil || err.Error() != noMatch {
					t.Fatalf("err = %v", err)
				}
			} else if err != nil || serial != tc.want {
				t.Fatalf("serial = %q, err = %v", serial, err)
			}
			if closed != tc.closed {
				t.Fatalf("closed = %d, want %d", closed, tc.closed)
			}
		})
	}
}

func TestOpenWithoutDevicesReportsExpectedText(t *testing.T) {
	if _, err := Open("definitely-not-a-serial"); err == nil ||
		err.Error() != "Expected exactly one matching Pico HID interface (CAFE:4015/4005)" {
		t.Fatalf("err = %v", err)
	}
}

func TestOSErrorFormat(t *testing.T) {
	if got := osError(syscall.EIO).Error(); got != "[Errno 5] Input/output error" {
		t.Fatalf("got %q", got)
	}
}

func TestUeventIDs(t *testing.T) {
	vid, pid, ok := ueventIDs("DRIVER=hid-generic\nHID_ID=0003:0000CAFE:00004015\nHID_NAME=x\n")
	if !ok || vid != 0xcafe || pid != 0x4015 {
		t.Errorf("%x %x %v", vid, pid, ok)
	}
	for _, bad := range []string{"", "HID_ID=0003:CAFE", "HID_ID=0003:zz:00004015", "HID_ID=0003:FFFFFFFF:00004015"} {
		if _, _, ok := ueventIDs(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestDeniedDongleIsNamedOnlyWhenSysfsSaysItIsTheDongle(t *testing.T) {
	uevents := map[string]string{
		"hidraw0": "HID_ID=0003:0000046D:0000C52B\n",
		"hidraw3": "HID_ID=0003:0000CAFE:00004005\n",
	}
	read := func(node string) (string, error) {
		if text, ok := uevents[node]; ok {
			return text, nil
		}
		return "", os.ErrNotExist
	}
	if got := deniedDongle([]string{"/dev/hidraw0", "/dev/hidraw9", "/dev/hidraw3"}, read); got != "/dev/hidraw3" {
		t.Errorf("found %q", got)
	}
	if got := deniedDongle([]string{"/dev/hidraw0"}, read); got != "" {
		t.Errorf("another vendor's device was blamed: %q", got)
	}
	if got := deniedDongle(nil, read); got != "" {
		t.Errorf("no denied nodes: %q", got)
	}
	want := "found /dev/hidraw3 but no permission to open it; install the udev rule 70-airpods-hid.rules"
	if got := permissionError("/dev/hidraw3").Error(); got != want {
		t.Errorf("text %q", got)
	}
}

func TestListNodes(t *testing.T) {
	boom := errors.New("boom")
	denied := permissionError("/dev/hidraw1")
	dongle := "HID_ID=0003:0000CAFE:00004005\nHID_NAME=x\nHID_UNIQ=SYS1\n"
	for _, tc := range []struct {
		name    string
		nodes   map[string]any // fakeNode or error from open
		uevents map[string]string
		want    []Info
		closed  int
	}{
		{name: "none"},
		{
			name: "two dongles are both listed",
			nodes: map[string]any{
				"/dev/hidraw0": fakeNode{vid: 0xcafe, pid: 0x4015, serial: "S1"},
				"/dev/hidraw1": fakeNode{vid: 0xcafe, pid: 0x4005, serial: "S2"},
			},
			want: []Info{
				{Path: "/dev/hidraw0", Serial: "S1", PID: 0x4015, Openable: true},
				{Path: "/dev/hidraw1", Serial: "S2", PID: 0x4005, Openable: true},
			},
			closed: 2,
		},
		{
			name:   "another device is left out and closed",
			nodes:  map[string]any{"/dev/hidraw0": fakeNode{vid: 0x046d, pid: 0xc52b}},
			closed: 1,
		},
		{
			name:    "denied dongle comes from sysfs with its serial",
			nodes:   map[string]any{"/dev/hidraw1": os.ErrPermission},
			uevents: map[string]string{"hidraw1": dongle},
			want:    []Info{{Path: "/dev/hidraw1", Serial: "SYS1", PID: 0x4005, Err: denied}},
		},
		{
			name:    "denied device of another vendor is left out",
			nodes:   map[string]any{"/dev/hidraw1": os.ErrPermission},
			uevents: map[string]string{"hidraw1": "HID_ID=0003:0000046D:0000C52B\n"},
		},
		{
			name:  "denied node without sysfs is left out",
			nodes: map[string]any{"/dev/hidraw1": boom},
		},
		{
			name:    "serial falls back to sysfs",
			nodes:   map[string]any{"/dev/hidraw1": fakeNode{vid: 0xcafe, pid: 0x4005, uniqErr: boom}},
			uevents: map[string]string{"hidraw1": dongle},
			want:    []Info{{Path: "/dev/hidraw1", Serial: "SYS1", PID: 0x4005, Openable: true}},
			closed:  1,
		},
		{
			name:    "a node Open would skip is not openable",
			nodes:   map[string]any{"/dev/hidraw1": fakeNode{infoErr: syscall.EIO}},
			uevents: map[string]string{"hidraw1": dongle},
			want:    []Info{{Path: "/dev/hidraw1", Serial: "SYS1", PID: 0x4005, Err: osError(syscall.EIO)}},
			closed:  1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closed := 0
			var paths []string
			for _, path := range []string{"/dev/hidraw0", "/dev/hidraw1"} {
				if _, ok := tc.nodes[path]; ok {
					paths = append(paths, path)
				}
			}
			got := listNodes(paths, func(path string) (rawNode, error) {
				switch v := tc.nodes[path].(type) {
				case error:
					return nil, v
				case fakeNode:
					v.closed = &closed
					return v, nil
				}
				return nil, boom
			}, func(node string) (string, error) {
				if text, ok := tc.uevents[node]; ok {
					return text, nil
				}
				return "", os.ErrNotExist
			})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
			if closed != tc.closed {
				t.Errorf("closed %d, want %d", closed, tc.closed)
			}
		})
	}
}

func TestEnumerateWithoutDevices(t *testing.T) {
	if got, err := Enumerate(); err != nil || len(got) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}
