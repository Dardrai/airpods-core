//go:build !windows && !linux

package hid

import "errors"

// Open is unavailable on this platform; only Windows and Linux are supported.
func Open(serial string) (Device, error) {
	return nil, errors.New("HID transport is only implemented for Windows and Linux")
}

// Enumerate is unavailable on this platform; only Windows and Linux are supported.
func Enumerate() ([]Info, error) {
	return nil, errors.New("HID transport is only implemented for Windows and Linux")
}
