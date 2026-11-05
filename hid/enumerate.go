package hid

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Info describes one attached dongle.
type Info struct {
	Path     string // /dev/hidrawN on Linux, the device interface path on Windows
	Serial   string // USB serial; empty if the system would not say
	PID      uint16 // ProductAudio or ProductControl
	Openable bool   // Open could use this dongle right now
	Err      error  // why it could not be opened; nil when Openable
}

var errNoAttributes = errors.New("could not read the HID attributes")

// queryNode is an interface handle that can answer questions but not carry reports.
type queryNode interface {
	attributes() (vid, pid uint16, ok bool)
	isVendorCollection() (bool, error)
	serial() (string, error)
	close()
}

// listInterfaces picks the dongle's vendor collections out of the HID interfaces. The
// query handle needs no access rights, so a dongle held exclusively elsewhere is still
// identified; openFull then tells whether reports could be exchanged. An interface
// that cannot even be queried is listed as unopenable rather than hidden, going by the
// product in its path. An unreadable serial stays openable, as Open only needs it when
// it filters by serial.
func listInterfaces(paths []string, query func(string) (queryNode, error), openFull func(string) error) []Info {
	var found []Info
	for _, path := range paths {
		pid, ok := pathPID(path)
		if !ok {
			continue
		}
		node, err := query(path)
		if err != nil {
			found = append(found, Info{Path: path, PID: pid, Err: err})
			continue
		}
		info, _, keep := describeInterface(path, pid, node)
		node.close()
		if !keep {
			continue
		}
		if info.Err == nil {
			info.Err = openFull(path)
		}
		info.Openable = info.Err == nil
		found = append(found, info)
	}
	return found
}

// describeInterface asks what Open would ask. An unanswered question about the device leaves
// the dongle listed but not openable, since Open would fail on it too. The serial's own error
// comes back apart from that, because it only matters to an Open that filters by serial.
func describeInterface(path string, pid uint16, node queryNode) (info Info, serialErr error, keep bool) {
	info = Info{Path: path, PID: pid}
	vid, actual, ok := node.attributes()
	if !ok {
		info.Err = errNoAttributes
		return info, nil, true
	}
	if !isProduct(vid, actual) {
		return info, nil, false
	}
	info.PID = actual
	vendor, err := node.isVendorCollection()
	if err != nil {
		info.Err = err
		return info, nil, true
	}
	if !vendor {
		return info, nil, false
	}
	info.Serial, serialErr = node.serial()
	return info, serialErr, true
}

// selectInterface is Open's search. A failure on one interface only rules out that interface,
// so a collection Windows refuses to open cannot hide the one that works. The first such failure
// is the answer when nothing matched, since it says more than "no dongle". The serial has to be
// readable only when filtering by it. Interfaces are queried before the full open, because the
// dongle's other collections may refuse read and write access.
func selectInterface[T any](paths []string, serial string, query func(string) (queryNode, error), open func(Info) (T, error), closeFn func(T), msg string) (T, error) {
	var found []T
	var firstErr error
	remember := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for _, path := range paths {
		pid, ok := pathPID(path)
		if !ok {
			continue
		}
		node, err := query(path)
		if err != nil {
			remember(err)
			continue
		}
		info, serialErr, keep := describeInterface(path, pid, node)
		node.close()
		switch {
		case !keep:
			continue
		case info.Err != nil:
			remember(info.Err)
			continue
		case serial != "" && serialErr != nil:
			remember(serialErr)
			continue
		case serial != "" && info.Serial != serial:
			continue
		}
		device, err := open(info)
		if err != nil {
			remember(err)
			continue
		}
		found = append(found, device)
	}
	if len(found) == 0 && firstErr != nil {
		var zero T
		return zero, firstErr
	}
	return exactlyOne(found, closeFn, msg)
}

// pathPID reads the dongle's product from a Windows device interface path, which holds
// vid_cafe&pid_4015 in any case.
func pathPID(path string) (uint16, bool) {
	_, rest, found := strings.Cut(strings.ToLower(path), fmt.Sprintf("vid_%04x&pid_", VendorID))
	if !found || len(rest) < 4 {
		return 0, false
	}
	pid, err := strconv.ParseUint(rest[:4], 16, 16)
	if err != nil || !isProduct(VendorID, uint16(pid)) {
		return 0, false
	}
	return uint16(pid), true
}
