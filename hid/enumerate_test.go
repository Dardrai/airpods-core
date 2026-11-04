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
			name:   "no serial is not openable",
			paths:  []string{control},
			nodes:  map[string]any{control: fakeQuery{vid: VendorID, pid: ProductControl, vendor: true, serialErr: boom}},
			want:   []Info{{Path: control, PID: ProductControl, Err: boom}},
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
