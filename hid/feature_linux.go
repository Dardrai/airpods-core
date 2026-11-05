//go:build linux

package hid

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// Hidraw ioctl numbers from linux/hidraw.h; amd64 and arm64 share the generic encoding.
const (
	hidiocGRawInfo = 0x80084803 // HIDIOCGRAWINFO
	hidiocGRawUniq = 0x81004808 // HIDIOCGRAWUNIQ(256)
	hidiocGFeature = 0xC0404807 // HIDIOCGFEATURE(64)
	hidiocSFeature = 0xC0404806 // HIDIOCSFEATURE(64)
)

// ioc is the kernel's _IOC macro; the unit test checks the constants against it.
func ioc(dir, typ, nr, size uintptr) uintptr {
	return dir<<30 | size<<16 | typ<<8 | nr
}

// ioctl returns the call's result, which for the feature requests is the transferred length.
func ioctl(fd int, request uintptr, arg unsafe.Pointer) (int, error) {
	result, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(arg))
	if errno != 0 {
		return 0, errno
	}
	return int(result), nil
}

// errnoKind names the cross-platform condition of an errno, if it has one.
func errnoKind(errno syscall.Errno) error {
	switch errno {
	case syscall.ENODEV, syscall.ENXIO, syscall.ESHUTDOWN:
		return ErrDisconnected
	case syscall.ETIMEDOUT, syscall.ETIME:
		return ErrTimeout
	}
	return nil
}

// osError formats an errno as "[Errno N] Text"; errors.Is still finds the errno.
func osError(err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		text := errno.Error()
		return systemError(fmt.Sprintf("[Errno %d] %s%s", int(errno), strings.ToUpper(text[:1]), text[1:]), errno, errnoKind(errno))
	}
	return err
}

// device owns one hidraw descriptor. mu is held across each transfer, so Close cannot release
// the number while a transfer may still use it: the kernel could hand it to an unrelated file
// the moment it is free, and a report would then go there.
type device struct {
	mu      sync.Mutex
	fd      int
	closed  bool
	serial  string
	ioctl   func(fd int, request uintptr, arg unsafe.Pointer) (int, error)
	closeFD func(fd int) error
}

func newDevice(fd int, serial string) *device {
	return &device{fd: fd, serial: serial, ioctl: ioctl, closeFD: syscall.Close}
}

// Get requires the whole report, as the Windows transport does. Each ioctl waits at most for
// the kernel's USB control timeout; see Device.
func (d *device) Get(report byte) ([ReportSize]byte, error) {
	buf := new([ReportSize]byte)
	buf[0] = report
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return *buf, ErrClosed
	}
	for {
		count, err := d.ioctl(d.fd, hidiocGFeature, unsafe.Pointer(buf))
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return *buf, osError(err)
		}
		if count != ReportSize {
			return *buf, errIncomplete
		}
		return *buf, CheckReport(report, *buf)
	}
}

// Set never retries: a request may already have reached the device.
func (d *device) Set(data [ReportSize]byte) error {
	buf := new([ReportSize]byte)
	*buf = data
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	if _, err := d.ioctl(d.fd, hidiocSFeature, unsafe.Pointer(buf)); err != nil {
		return osError(err)
	}
	return nil
}

func (d *device) Serial() string { return d.serial }

// Close waits for a transfer in flight. Closing twice is harmless.
func (d *device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return d.closeFD(d.fd)
}
