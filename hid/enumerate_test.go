package hid

import (
	"errors"
	"reflect"
	"testing"
)

type fakeQuery struct {
	vid, pid  uint16
	noAttr    bool
	vendor    bool
	vendorErr error
	serialNo  string
	serialErr error
	closed    *int
}

func (n fakeQuery) attributes() (uint16, uint16, bool) { return n.vid, n.pid, !n.noAttr }
func (n fakeQuery) isVendorCollection() (bool, error)  { return n.vendor, n.vendorErr }
func (n fakeQuery) serial() (string, error)            { return n.serialNo, n.serialErr }
func (n fakeQuery) close()                             { *n.closed++ }

func TestListInterfaces(t *testing.T) {
	const (
		audio   = `\\?\hid#vid_cafe&pid_4015&mi_03#7&1&0#{guid}`
		control = `\\?\HID#VID_CAFE&PID_4005#7&2&0#{guid}`
		other   = `\\?\hid#vid_046d&pid_c52b#7&3&0#{guid}`
		unknown = `\\?\hid#vid_cafe&pid_9999#7&4&0#{guid}`
	)
	denied := errors.New("[WinError 5] Access is denied.")
	queryFailed := errors.New("[WinError 2] gone")
	boom := errors.New("boom")
	for _, tc := range []struct {
		name   string
		paths  []string
		nodes  map[string]any // fakeQuery or the error from query
		full   map[string]error
		want   []Info
		closed int
	}{
		{name: "none"},
		{name: "another product is never queried", paths: []string{other, unknown}},
		{
			name:  "two dongles keep their own details",
			paths: []string{audio, control},
			nodes: map[string]any{
				audio:   fakeQuery{vid: VendorID, pid: ProductAudio, vendor: true, serialNo: "S1"},
				control: fakeQuery{vid: VendorID, pid: ProductControl, vendor: true, serialNo: "S2"},
			},
			want: []Info{
				{Path: audio, Serial: "S1", PID: ProductAudio, Openable: true},
				{Path: control, Serial: "S2", PID: ProductControl, Openable: true},
			},
			closed: 2,
		},
		{
			name:   "another collection of the dongle is skipped",
			paths:  []string{audio},
			nodes:  map[string]any{audio: fakeQuery{vid: VendorID, pid: ProductAudio}},
			closed: 1,
		},
		{
			name:   "attributes overrule the path",
			paths:  []string{audio},
			nodes:  map[string]any{audio: fakeQuery{vid: 0x1234, pid: 1, vendor: true}},
			closed: 1,
		},
		{
			name:   "held elsewhere is listed with its serial",
			paths:  []string{audio},
			nodes:  map[string]any{audio: fakeQuery{vid: VendorID, pid: ProductAudio, vendor: true, serialNo: "S1"}},
			full:   map[string]error{audio: denied},
			want:   []Info{{Path: audio, Serial: "S1", PID: ProductAudio, Err: denied}},
			closed: 1,
		},
		{
			name:  "no query handle goes by the path",
			paths: []string{control},
			nodes: map[string]any{control: queryFailed},
			want:  []Info{{Path: control, PID: ProductControl, Err: queryFailed}},
		},
		{
			name:   "no attributes is not openable",
			paths:  []string{control},
			nodes:  map[string]any{control: fakeQuery{noAttr: true}},
			want:   []Info{{Path: control, PID: ProductControl, Err: errNoAttributes}},
			closed: 1,
		},
		{
			name:   "no capabilities is not openable",
			paths:  []string{control},
			nodes:  map[string]any{control: fakeQuery{vid: VendorID, pid: ProductControl, vendorErr: boom}},
			want:   []Info{{Path: control, PID: ProductControl, Err: boom}},
			closed: 1,
		},
		{
			name:   "no serial stays openable, as Open needs it only to filter",
			paths:  []string{control},
			nodes:  map[string]any{control: fakeQuery{vid: VendorID, pid: ProductControl, vendor: true, serialErr: boom}},
			want:   []Info{{Path: control, PID: ProductControl, Openable: true}},
			closed: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closed := 0
			got := listInterfaces(tc.paths, func(path string) (queryNode, error) {
				switch v := tc.nodes[path].(type) {
				case error:
					return nil, v
				case fakeQuery:
					v.closed = &closed
					return v, nil
				}
				t.Fatalf("queried %s", path)
				return nil, nil
			}, func(path string) error { return tc.full[path] })
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
			if closed != tc.closed {
				t.Errorf("closed %d, want %d", closed, tc.closed)
			}
		})
	}
}

func TestSelectInterface(t *testing.T) {
	const (
		audio   = `\\?\hid#vid_cafe&pid_4015&mi_03#7&1&0#{guid}`
		mouse   = `\\?\hid#vid_cafe&pid_4015&mi_01#7&5&0#{guid}`
		control = `\\?\HID#VID_CAFE&PID_4005#7&2&0#{guid}`
		other   = `\\?\hid#vid_046d&pid_c52b#7&3&0#{guid}`
		noMatch = "no match"
	)
	dongle := func(serial string) fakeQuery {
		return fakeQuery{vid: VendorID, pid: ProductAudio, vendor: true, serialNo: serial}
	}
	first, second := errors.New("first"), errors.New("second")
	for _, tc := range []struct {
		name    string
		paths   []string
		serial  string
		nodes   map[string]any // fakeQuery or the error from query
		openErr map[string]error
		want    string // chosen path; empty on failure
		err     error  // the failure's identity; nil means the MatchError below
		found   int
		opened  []string // handles closed again by exactlyOne
	}{
		{name: "no interfaces", found: 0},
		{name: "other products are never queried", paths: []string{other}, found: 0},
		{
			name:  "one bad interface does not hide the good one",
			paths: []string{mouse, audio},
			nodes: map[string]any{mouse: first, audio: dongle("S1")},
			want:  audio,
		},
		{
			name:    "an interface that refuses the full open is skipped",
			paths:   []string{mouse, audio},
			nodes:   map[string]any{mouse: dongle("S0"), audio: dongle("S1")},
			openErr: map[string]error{mouse: first},
			want:    audio,
		},
		{
			name:  "another collection is not even opened",
			paths: []string{mouse, audio},
			nodes: map[string]any{mouse: fakeQuery{vid: VendorID, pid: ProductAudio}, audio: dongle("S1")},
			want:  audio,
		},
		{
			name:  "unanswered questions rule out only that interface",
			paths: []string{mouse, control, audio},
			nodes: map[string]any{
				mouse:   fakeQuery{noAttr: true},
				control: fakeQuery{vid: VendorID, pid: ProductControl, vendorErr: first},
				audio:   dongle("S1"),
			},
			want: audio,
		},
		{
			name:  "the serial is optional without a filter",
			paths: []string{audio},
			nodes: map[string]any{audio: fakeQuery{vid: VendorID, pid: ProductAudio, vendor: true, serialErr: first}},
			want:  audio,
		},
		{
			name:   "the serial is required with a filter",
			paths:  []string{audio},
			serial: "S1",
			nodes:  map[string]any{audio: fakeQuery{vid: VendorID, pid: ProductAudio, vendor: true, serialErr: first}},
			err:    first,
		},
		{
			name:   "the filter picks one",
			paths:  []string{mouse, audio},
			serial: "S1",
			nodes:  map[string]any{mouse: dongle("S0"), audio: dongle("S1")},
			want:   audio,
		},
		{
			name:   "a filter that matches nothing is no match",
			paths:  []string{audio},
			serial: "S9",
			nodes:  map[string]any{audio: dongle("S1")},
			found:  0,
		},
		{
			name:  "with no match the first failure is the answer",
			paths: []string{mouse, audio},
			nodes: map[string]any{mouse: first, audio: second},
			err:   first,
		},
		{
			name:    "a failed open is remembered too",
			paths:   []string{audio},
			nodes:   map[string]any{audio: dongle("S1")},
			openErr: map[string]error{audio: first},
			err:     first,
		},
		{
			name:   "two matches close both",
			paths:  []string{audio, control},
			nodes:  map[string]any{audio: dongle("S1"), control: fakeQuery{vid: VendorID, pid: ProductControl, vendor: true, serialNo: "S2"}},
			found:  2,
			opened: []string{audio, control},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queryClosed, queried := 0, 0
			var closed []string
			got, err := selectInterface(tc.paths, tc.serial, func(path string) (queryNode, error) {
				switch v := tc.nodes[path].(type) {
				case error:
					return nil, v
				case fakeQuery:
					queried++
					v.closed = &queryClosed
					return v, nil
				}
				t.Fatalf("queried %s", path)
				return nil, nil
			}, func(info Info) (string, error) {
				if err := tc.openErr[info.Path]; err != nil {
					return "", err
				}
				return info.Path, nil
			}, func(path string) { closed = append(closed, path) }, noMatch)
			switch {
			case tc.want != "":
				if err != nil || got != tc.want {
					t.Fatalf("got %q, err %v", got, err)
				}
			case tc.err != nil:
				if !errors.Is(err, tc.err) {
					t.Fatalf("err %v, want %v", err, tc.err)
				}
			default:
				var mismatch *MatchError
				if !errors.As(err, &mismatch) || mismatch.Found != tc.found || err.Error() != noMatch {
					t.Fatalf("err %v", err)
				}
			}
			if queryClosed != queried {
				t.Errorf("%d query handles left open", queried-queryClosed)
			}
			if !reflect.DeepEqual(closed, tc.opened) {
				t.Errorf("closed %v, want %v", closed, tc.opened)
			}
		})
	}
}

func TestPathPID(t *testing.T) {
	for path, want := range map[string]uint16{
		`\\?\hid#vid_cafe&pid_4015&mi_03`: ProductAudio,
		`\\?\HID#VID_CAFE&PID_4005#7`:     ProductControl,
		`\\?\hid#vid_cafe&pid_4016`:       0,
		`\\?\hid#vid_cafe&pid_40`:         0,
		`\\?\hid#vid_cafe&pid_zzzz`:       0,
		`\\?\hid#vid_046d&pid_4015`:       0,
		`/dev/hidraw0`:                    0,
	} {
		got, ok := pathPID(path)
		if got != want || ok != (want != 0) {
			t.Errorf("%s: %x %v", path, got, ok)
		}
	}
}
