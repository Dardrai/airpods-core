//go:build windows

package hid

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWinErrorKeepsIdentity(t *testing.T) {
	for _, tc := range []struct {
		code windows.Errno
		kind error
	}{
		{windows.ERROR_DEVICE_NOT_CONNECTED, ErrDisconnected},
		{windows.ERROR_GEN_FAILURE, ErrDisconnected},
		{windows.ERROR_SEM_TIMEOUT, ErrTimeout},
		{windows.ERROR_ACCESS_DENIED, nil},
	} {
		err := winError(tc.code)
		if !errors.Is(err, tc.code) {
			t.Errorf("%d: lost the code", tc.code)
		}
		for _, kind := range []error{ErrDisconnected, ErrTimeout} {
			if errors.Is(err, kind) != (kind == tc.kind) {
				t.Errorf("%d: errors.Is(%v) = %v", tc.code, kind, errors.Is(err, kind))
			}
		}
	}
}
