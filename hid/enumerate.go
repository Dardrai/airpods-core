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
// product in its path.
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
		info, keep := describeInterface(path, pid, node)
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

// describeInterface asks what Open would ask. An unanswered question leaves the dongle
// listed but not openable, since Open would fail on it too.
func describeInterface(path string, pid uint16, node queryNode) (Info, bool) {
	info := Info{Path: path, PID: pid}
	vid, actual, ok := node.attributes()
	if !ok {
		info.Err = errNoAttributes
		return info, true
	}
	if !isProduct(vid, actual) {
		return info, false
	}
	info.PID = actual
	vendor, err := node.isVendorCollection()
	if err != nil {
		info.Err = err
		return info, true
	}
	if !vendor {
		return info, false
	}
	info.Serial, info.Err = node.serial()
	return info, true
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
