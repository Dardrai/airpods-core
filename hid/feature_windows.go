//go:build windows

package hid

import (
	"errors"
	"runtime"

	"golang.org/x/sys/windows"
)

const (
	ioctlHIDGetFeature = 0x000b0192
	ioctlHIDSetFeature = 0x000b0191
)

// transfer holds everything the kernel may touch while a request is pending. It is
// heap-allocated and pinned for the whole transfer, including the cancel wait.
type transfer struct {
	overlapped windows.Overlapped
	count      uint32
	buffer     [ReportSize]byte
}

type device struct {
	handle windows.Handle
	serial string
}

type windowsOps struct {
	handle  windows.Handle
	event   windows.Handle
	xfer    *transfer
	writing bool
}

func (o *windowsOps) issue() (bool, error) {
	var in, out *byte
	var inSize, outSize uint32
	code := uint32(ioctlHIDGetFeature)
	if o.writing {
		code = ioctlHIDSetFeature
		in, inSize = &o.xfer.buffer[0], ReportSize
	} else {
		out, outSize = &o.xfer.buffer[0], ReportSize
	}
	err := windows.DeviceIoControl(o.handle, code, in, inSize, out, outSize, &o.xfer.count, &o.xfer.overlapped)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		return true, nil
	}
	return false, winError(err)
}

func (o *windowsOps) waitEvent(millis uint32) bool {
	event, _ := windows.WaitForSingleObject(o.event, millis)
	return event == windows.WAIT_OBJECT_0
}

func (o *windowsOps) cancel() {
	_ = windows.CancelIoEx(o.handle, &o.xfer.overlapped)
}

func (o *windowsOps) result(wait bool) (uint32, error) {
	if err := windows.GetOverlappedResult(o.handle, &o.xfer.overlapped, &o.xfer.count, wait); err != nil {
		return 0, winError(err)
	}
	return o.xfer.count, nil
}

func (d *device) feature(writing bool, data *[ReportSize]byte) error {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return winError(err)
	}
	defer windows.CloseHandle(event)
	xfer := new(transfer)
	var pin runtime.Pinner
	pin.Pin(xfer)
	defer pin.Unpin()
	xfer.overlapped.HEvent = event
	xfer.buffer = *data
	err = runOverlapped(&windowsOps{handle: d.handle, event: event, xfer: xfer, writing: writing}, writing)
	if err == nil && !writing {
		*data = xfer.buffer
	}
	runtime.KeepAlive(xfer)
	return err
}

func (d *device) Get(report byte) ([ReportSize]byte, error) {
	var data [ReportSize]byte
	data[0] = report
	if err := d.feature(false, &data); err != nil {
		return data, err
	}
	return data, CheckReport(report, data)
}

func (d *device) Set(data [ReportSize]byte) error {
	return d.feature(true, &data)
}

func (d *device) Serial() string { return d.serial }

func (d *device) Close() error {
	if d.handle == windows.InvalidHandle {
		return nil
	}
	err := windows.CloseHandle(d.handle)
	d.handle = windows.InvalidHandle
	return err
}
