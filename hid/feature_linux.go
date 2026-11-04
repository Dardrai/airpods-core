//go:build linux

package hid

import (
	"errors"
	"fmt"
	"strings"
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

func ioctl(fd int, request uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// osError formats an errno as "[Errno N] Text".
func osError(err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		text := errno.Error()
		return fmt.Errorf("[Errno %d] %s%s", int(errno), strings.ToUpper(text[:1]), text[1:])
	}
	return err
}

type device struct {
	fd     int
	serial string
}

func (d *device) Get(report byte) ([ReportSize]byte, error) {
	buf := new([ReportSize]byte)
	buf[0] = report
	for {
		err := ioctl(d.fd, hidiocGFeature, unsafe.Pointer(buf))
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return *buf, osError(err)
		}
		return *buf, CheckReport(report, *buf)
	}
}

// Set never retries: a request may already have reached the device.
func (d *device) Set(data [ReportSize]byte) error {
	buf := new([ReportSize]byte)
	*buf = data
	if err := ioctl(d.fd, hidiocSFeature, unsafe.Pointer(buf)); err != nil {
		return osError(err)
	}
	return nil
}

func (d *device) Serial() string { return d.serial }

func (d *device) Close() error {
	if d.fd < 0 {
		return nil
	}
	err := syscall.Close(d.fd)
	d.fd = -1
	return err
}
