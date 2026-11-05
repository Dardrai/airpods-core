//go:build linux

package hid

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
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

// noNodes matches nothing, and the seams fail the test if they are ever reached: these
// tests must not touch a real /dev/hidraw node, whatever is plugged in.
func noNodes(t *testing.T) (string, func(string) (rawNode, error), func(string) (string, error)) {
	t.Helper()
	return filepath.Join(t.TempDir(), "hidraw*"),
		func(path string) (rawNode, error) {
			t.Errorf("opened %s", path)
			return nil, os.ErrNotExist
		},
		func(node string) (string, error) {
			t.Errorf("read uevent of %s", node)
			return "", os.ErrNotExist
		}
}

func TestOpenWithoutDevicesReportsExpectedText(t *testing.T) {
	pattern, open, read := noNodes(t)
	_, _, err := openNodes(pattern, "definitely-not-a-serial", open, read)
	var mismatch *MatchError
	if !errors.As(err, &mismatch) || mismatch.Found != 0 ||
		err.Error() != "Expected exactly one matching Pico HID interface (CAFE:4015/4005)" {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenNamesADeniedDongle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hidraw3")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := openNodes(filepath.Join(dir, "hidraw*"), "", func(string) (rawNode, error) {
		return nil, syscall.EACCES
	}, func(node string) (string, error) {
		return "HID_ID=0003:0000CAFE:00004005\n", nil
	})
	if err == nil || err.Error() != permissionError(path).Error() {
		t.Fatalf("err = %v", err)
	}
}

// fakeDevice stands in for the kernel: it copies reply into the buffer and returns result.
func fakeDevice(result int, err error, reply [ReportSize]byte) *device {
	d := newDevice(99, "S1")
	d.ioctl = func(_ int, _ uintptr, arg unsafe.Pointer) (int, error) {
		*(*[ReportSize]byte)(arg) = reply
		return result, err
	}
	d.closeFD = func(int) error { return nil }
	return d
}

func TestGetRequiresTheWholeReport(t *testing.T) {
	var good [ReportSize]byte
	header := Header(2)
	copy(good[:], header[:])
	if got, err := fakeDevice(ReportSize, nil, good).Get(2); err != nil || got != good {
		t.Fatalf("full report: %v", err)
	}
	if _, err := fakeDevice(ReportSize-1, nil, good).Get(2); !errors.Is(err, errIncomplete) {
		t.Fatalf("short report: %v", err)
	}
	if _, err := fakeDevice(0, nil, good).Get(2); !errors.Is(err, errIncomplete) {
		t.Fatalf("empty report: %v", err)
	}
	if _, err := fakeDevice(ReportSize, nil, [ReportSize]byte{}).Get(2); !errors.Is(err, ErrUnsupportedReport) {
		t.Fatalf("wrong identity: %v", err)
	}
	if _, err := fakeDevice(0, syscall.ENODEV, good).Get(2); !errors.Is(err, ErrDisconnected) || !errors.Is(err, syscall.ENODEV) {
		t.Fatalf("gone: %v", err)
	}
	if _, err := fakeDevice(0, syscall.ETIMEDOUT, good).Get(2); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	if err := fakeDevice(1, nil, good).Set(good); err != nil {
		t.Fatalf("a Set needs no full count, as on Windows: %v", err)
	}
}

func TestGetRetriesAnInterruptedRead(t *testing.T) {
	var good [ReportSize]byte
	header := Header(3)
	copy(good[:], header[:])
	calls := 0
	d := newDevice(99, "S1")
	d.ioctl = func(_ int, _ uintptr, arg unsafe.Pointer) (int, error) {
		if calls++; calls == 1 {
			return 0, syscall.EINTR
		}
		*(*[ReportSize]byte)(arg) = good
		return ReportSize, nil
	}
	if _, err := d.Get(3); err != nil || calls != 2 {
		t.Fatalf("calls %d: %v", calls, err)
	}
}

func TestCloseWaitsForTheTransferInFlight(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []string
	note := func(what string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, what)
	}
	d := newDevice(99, "S1")
	d.ioctl = func(fd int, _ uintptr, _ unsafe.Pointer) (int, error) {
		close(started)
		<-finish
		note("transfer done")
		return ReportSize, nil
	}
	closes := 0
	d.closeFD = func(fd int) error {
		closes++
		note("closed")
		return nil
	}
	transfer := make(chan error)
	go func() { transfer <- d.Set([ReportSize]byte{}) }()
	<-started
	closed := make(chan error)
	go func() { closed <- d.Close() }()
	select {
	case <-closed:
		t.Fatal("Close did not wait for the transfer")
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	if err := <-transfer; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"transfer done", "closed"}) {
		t.Fatalf("order %v", order)
	}

	d.ioctl = func(int, uintptr, unsafe.Pointer) (int, error) {
		t.Error("a transfer used the descriptor after Close")
		return 0, nil
	}
	if _, err := d.Get(2); !errors.Is(err, ErrClosed) {
		t.Errorf("Get after Close: %v", err)
	}
	if err := d.Set([ReportSize]byte{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Set after Close: %v", err)
	}
	if err := d.Close(); err != nil || closes != 1 {
		t.Errorf("second Close: %v, %d closes", err, closes)
	}
}

func TestOSErrorFormat(t *testing.T) {
	if got := osError(syscall.EIO).Error(); got != "[Errno 5] Input/output error" {
		t.Fatalf("got %q", got)
	}
}

func TestOSErrorKeepsIdentity(t *testing.T) {
	for _, tc := range []struct {
		errno syscall.Errno
		text  string
		kind  error
	}{
		{syscall.ENODEV, "[Errno 19] No such device", ErrDisconnected},
		{syscall.ESHUTDOWN, "[Errno 108] Cannot send after transport endpoint shutdown", ErrDisconnected},
		{syscall.ENXIO, "[Errno 6] No such device or address", ErrDisconnected},
		{syscall.ETIMEDOUT, "[Errno 110] Connection timed out", ErrTimeout},
		{syscall.EIO, "[Errno 5] Input/output error", nil},
		{syscall.EACCES, "[Errno 13] Permission denied", nil},
	} {
		err := osError(tc.errno)
		if err.Error() != tc.text {
			t.Errorf("%d: text %q", tc.errno, err)
		}
		if !errors.Is(err, tc.errno) {
			t.Errorf("%d: lost the errno", tc.errno)
		}
		var errno syscall.Errno
		if !errors.As(err, &errno) || errno != tc.errno {
			t.Errorf("%d: errors.As found %d", tc.errno, errno)
		}
		for _, kind := range []error{ErrDisconnected, ErrTimeout} {
			if errors.Is(err, kind) != (kind == tc.kind) {
				t.Errorf("%d: errors.Is(%v) = %v", tc.errno, kind, errors.Is(err, kind))
			}
		}
	}
	if !errors.Is(osError(syscall.EACCES), os.ErrPermission) {
		t.Error("EACCES should still match os.ErrPermission")
	}
	plain := errors.New("plain")
	if osError(plain) != plain {
		t.Error("an error without an errno passes through")
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
	want := "found /dev/hidraw3 but no permission to open it; install the AirPods udev rule; see the docs"
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
	pattern, open, read := noNodes(t)
	if got, err := enumerateNodes(pattern, open, read); err != nil || len(got) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}
