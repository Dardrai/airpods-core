//go:build linux

package hid

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// rawNode is the part of a hidraw node that enumeration needs.
type rawNode interface {
	info() (vid, pid uint16, err error)
	uniq() (string, error)
	close()
}

type fdNode int

func (n fdNode) info() (uint16, uint16, error) {
	var raw struct {
		bustype  uint32
		vid, pid uint16
	}
	if err := ioctl(int(n), hidiocGRawInfo, unsafe.Pointer(&raw)); err != nil {
		return 0, 0, err
	}
	return raw.vid, raw.pid, nil
}

func (n fdNode) uniq() (string, error) {
	var buf [256]byte
	if err := ioctl(int(n), hidiocGRawUniq, unsafe.Pointer(&buf)); err != nil {
		return "", err
	}
	value, _, _ := bytes.Cut(buf[:], []byte{0})
	return strings.ToValidUTF8(string(value), "�"), nil
}

func (n fdNode) close() { syscall.Close(int(n)) }

// selectNodes keeps matching nodes: a node that cannot be opened or queried is
// skipped silently; the serial is only required to be readable when filtering by it.
func selectNodes(paths []string, serial string, open func(string) (rawNode, error)) (rawNode, string, error) {
	type match struct {
		node   rawNode
		serial string
	}
	var found []match
	for _, path := range paths {
		node, err := open(path)
		if err != nil {
			continue
		}
		vid, pid, err := node.info()
		if err != nil || !isProduct(vid, pid) {
			node.close()
			continue
		}
		unique, err := node.uniq()
		if serial != "" && (err != nil || unique != serial) {
			node.close()
			continue
		}
		found = append(found, match{node, unique})
	}
	chosen, err := exactlyOne(found, func(m match) { m.node.close() },
		"Expected exactly one matching Pico HID interface (CAFE:4015/4005)")
	if err != nil {
		return nil, "", err
	}
	return chosen.node, chosen.serial, nil
}

// ueventIDs reads the vendor and product from a hidraw node's sysfs uevent text,
// whose HID_ID line reads like HID_ID=0003:0000CAFE:00004015.
func ueventIDs(uevent string) (vid, pid uint16, ok bool) {
	for _, line := range strings.Split(uevent, "\n") {
		value, found := strings.CutPrefix(line, "HID_ID=")
		if !found {
			continue
		}
		parts := strings.Split(value, ":")
		if len(parts) != 3 {
			return 0, 0, false
		}
		v, errV := strconv.ParseUint(parts[1], 16, 32)
		p, errP := strconv.ParseUint(parts[2], 16, 32)
		if errV != nil || errP != nil || v > 0xffff || p > 0xffff {
			return 0, 0, false
		}
		return uint16(v), uint16(p), true
	}
	return 0, 0, false
}

// deniedDongle returns the first node that could not be opened but that sysfs, which
// needs no permission on the node, identifies as the dongle.
func deniedDongle(denied []string, readUevent func(node string) (string, error)) string {
	for _, path := range denied {
		uevent, err := readUevent(filepath.Base(path))
		if err != nil {
			continue
		}
		if vid, pid, ok := ueventIDs(uevent); ok && isProduct(vid, pid) {
			return path
		}
	}
	return ""
}

func readSysfsUevent(node string) (string, error) {
	data, err := os.ReadFile(filepath.Join("/sys/class/hidraw", node, "device", "uevent"))
	return string(data), err
}

func permissionError(node string) error {
	return fmt.Errorf("found %s but no permission to open it; install the AirPods udev rule; see the docs", node)
}

// ueventUniq reads the USB serial from a hidraw node's sysfs uevent text.
func ueventUniq(uevent string) string {
	for _, line := range strings.Split(uevent, "\n") {
		if value, found := strings.CutPrefix(line, "HID_UNIQ="); found {
			return value
		}
	}
	return ""
}

// listNodes finds every node of a dongle. Sysfs needs no permission on the node, so it
// still names a dongle that cannot be opened. A node counts as openable when Open would
// accept it.
func listNodes(paths []string, open func(string) (rawNode, error), readUevent func(node string) (string, error)) []Info {
	var found []Info
	for _, path := range paths {
		info, ok := describeNode(path, open, readUevent)
		if ok {
			found = append(found, info)
		}
	}
	return found
}

func describeNode(path string, open func(string) (rawNode, error), readUevent func(node string) (string, error)) (Info, bool) {
	node, openErr := open(path)
	if openErr == nil {
		defer node.close()
		vid, pid, err := node.info()
		if err == nil {
			if !isProduct(vid, pid) {
				return Info{}, false
			}
			info := Info{Path: path, PID: pid, Openable: true}
			serial, err := node.uniq()
			if err != nil {
				uevent, _ := readUevent(filepath.Base(path))
				serial = ueventUniq(uevent)
			}
			info.Serial = serial
			return info, true
		}
		openErr = osError(err)
	} else if errors.Is(openErr, os.ErrPermission) {
		openErr = permissionError(path)
	} else {
		openErr = osError(openErr)
	}
	uevent, err := readUevent(filepath.Base(path))
	if err != nil {
		return Info{}, false
	}
	vid, pid, ok := ueventIDs(uevent)
	if !ok || !isProduct(vid, pid) {
		return Info{}, false
	}
	return Info{Path: path, Serial: ueventUniq(uevent), PID: pid, Err: openErr}, true
}

func openNode(path string) (int, error) {
	return syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
}

// Enumerate lists every attached dongle without keeping any of them open. No dongle is
// an empty list, not an error.
func Enumerate() ([]Info, error) {
	paths, err := filepath.Glob("/dev/hidraw*")
	if err != nil {
		return nil, err
	}
	return listNodes(paths, func(path string) (rawNode, error) {
		fd, err := openNode(path)
		if err != nil {
			return nil, err
		}
		return fdNode(fd), nil
	}, readSysfsUevent), nil
}

// Open finds exactly one hidraw node of the dongle, optionally by USB serial.
func Open(serial string) (Device, error) {
	paths, err := filepath.Glob("/dev/hidraw*")
	if err != nil {
		return nil, err
	}
	var denied []string
	node, unique, err := selectNodes(paths, serial, func(path string) (rawNode, error) {
		fd, err := openNode(path)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				denied = append(denied, path)
			}
			return nil, err
		}
		return fdNode(fd), nil
	})
	if err != nil {
		if found := deniedDongle(denied, readSysfsUevent); found != "" {
			return nil, permissionError(found)
		}
		return nil, err
	}
	return &device{fd: int(node.(fdNode)), serial: unique}, nil
}
