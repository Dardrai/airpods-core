//go:build windows

package hid

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modHID      = windows.NewLazySystemDLL("hid.dll")
	modSetupAPI = windows.NewLazySystemDLL("setupapi.dll")

	procHidDGetHidGuid             = modHID.NewProc("HidD_GetHidGuid")
	procHidDGetAttributes          = modHID.NewProc("HidD_GetAttributes")
	procHidDGetPreparsedData       = modHID.NewProc("HidD_GetPreparsedData")
	procHidDFreePreparsedData      = modHID.NewProc("HidD_FreePreparsedData")
	procHidPGetCaps                = modHID.NewProc("HidP_GetCaps")
	procHidDGetSerialNumberString  = modHID.NewProc("HidD_GetSerialNumberString")
	procSetupDiGetClassDevsW       = modSetupAPI.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumInterfaces      = modSetupAPI.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetInterfaceDetailW = modSetupAPI.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyInfoList     = modSetupAPI.NewProc("SetupDiDestroyDeviceInfoList")
)

const (
	digcfPresentInterface = 0x12
	errorNoMoreItems      = windows.Errno(259)
	hidpStatusSuccess     = 0x110000
	usagePageVendor       = 0xff31
	// maxInterfaces only guards against an enumeration that never ends. Reaching it is
	// an error, because a search that stopped early could miss the dongle.
	maxInterfaces = 4096
)

var errTooManyInterfaces = fmt.Errorf("Stopped after %d HID interfaces without reaching the end of the list", maxInterfaces)

type deviceInterfaceData struct {
	size     uint32
	guid     windows.GUID
	flags    uint32
	reserved uintptr
}

type hiddAttributes struct {
	size    uint32
	vid     uint16
	pid     uint16
	version uint16
}

// winErrorKind names the cross-platform condition of a Windows error, if it has one.
func winErrorKind(code windows.Errno) error {
	switch code {
	case windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND, windows.ERROR_DEV_NOT_EXIST,
		windows.ERROR_GEN_FAILURE, windows.ERROR_NO_SUCH_DEVICE, windows.ERROR_DEVICE_NOT_CONNECTED,
		windows.ERROR_DEVICE_REMOVED:
		return ErrDisconnected
	case windows.ERROR_SEM_TIMEOUT, windows.WAIT_TIMEOUT, windows.ERROR_TIMEOUT:
		return ErrTimeout
	}
	return nil
}

// winError formats an OS error as "[WinError N] text"; errors.Is still finds the code.
func winError(err error) error {
	var code windows.Errno
	if errors.As(err, &code) {
		return systemError(fmt.Sprintf("[WinError %d] %s", uint32(code), code.Error()), code, winErrorKind(code))
	}
	return err
}

// Open finds exactly one vendor HID collection of the dongle, optionally by serial.
func Open(serial string) (Device, error) {
	paths, skipped, err := interfacePaths()
	if err != nil {
		return nil, err
	}
	d, err := selectInterface(paths, serial, openQuery, func(info Info) (*device, error) {
		handle, err := openHandle(info.Path)
		if err != nil {
			return nil, err
		}
		return &device{handle: handle, serial: info.Serial}, nil
	}, func(d *device) { d.Close() }, "Expected exactly one matching Pico vendor HID collection")
	if err != nil {
		var mismatch *MatchError
		if errors.As(err, &mismatch) && mismatch.Found == 0 && skipped != nil {
			return nil, skipped
		}
		return nil, err
	}
	return d, nil
}

// Enumerate lists every attached dongle without keeping any of them open. No dongle is
// an empty list, not an error.
func Enumerate() ([]Info, error) {
	paths, _, err := interfacePaths()
	if err != nil {
		return nil, err
	}
	return listInterfaces(paths, openQuery, openFull), nil
}

// interfacePaths lists the path of every present HID interface. An interface whose path cannot
// be read is left out, with the first such failure returned in skipped; err means the list
// as a whole could not be had.
func interfacePaths() (paths []string, skipped, err error) {
	var guid windows.GUID
	procHidDGetHidGuid.Call(uintptr(unsafe.Pointer(&guid)))
	info, _, callErr := procSetupDiGetClassDevsW.Call(uintptr(unsafe.Pointer(&guid)), 0, 0, digcfPresentInterface)
	if info == uintptr(windows.InvalidHandle) {
		return nil, nil, winError(callErr)
	}
	defer procSetupDiDestroyInfoList.Call(info)

	for index := uintptr(0); index < maxInterfaces; index++ {
		var iface deviceInterfaceData
		iface.size = uint32(unsafe.Sizeof(iface))
		ok, _, callErr := procSetupDiEnumInterfaces.Call(info, 0, uintptr(unsafe.Pointer(&guid)), index, uintptr(unsafe.Pointer(&iface)))
		if ok == 0 {
			if errors.Is(callErr, errorNoMoreItems) {
				return paths, skipped, nil
			}
			return nil, nil, winError(callErr)
		}
		path, err := interfacePath(info, &iface)
		if err != nil {
			if skipped == nil {
				skipped = err
			}
			continue
		}
		paths = append(paths, path)
	}
	return nil, nil, errTooManyInterfaces
}

// queryHandle is a handle without access rights, which is enough for asking about a
// device that another process holds.
type queryHandle windows.Handle

func openQuery(path string) (queryNode, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, winError(err)
	}
	return queryHandle(handle), nil
}

func (h queryHandle) attributes() (uint16, uint16, bool) {
	attr := hiddAttributes{}
	attr.size = uint32(unsafe.Sizeof(attr))
	ok, _, _ := procHidDGetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&attr)))
	return attr.vid, attr.pid, ok != 0
}

func (h queryHandle) isVendorCollection() (bool, error) { return capsMatch(windows.Handle(h)) }

func (h queryHandle) serial() (string, error) {
	var value [256]uint16
	ok, _, callErr := procHidDGetSerialNumberString.Call(uintptr(h), uintptr(unsafe.Pointer(&value[0])), unsafe.Sizeof(value))
	if ok == 0 {
		return "", winError(callErr)
	}
	return windows.UTF16ToString(value[:]), nil
}

func (h queryHandle) close() { windows.CloseHandle(windows.Handle(h)) }

// openHandle opens an interface with the access reports need.
func openHandle(path string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return 0, winError(err)
	}
	return handle, nil
}

// openFull tries the access Open asks for and lets go again.
func openFull(path string) error {
	handle, err := openHandle(path)
	if err != nil {
		return err
	}
	windows.CloseHandle(handle)
	return nil
}

// interfacePath reads the variable-size detail record: cbSize DWORD, then the UTF-16 path.
func interfacePath(info uintptr, iface *deviceInterfaceData) (string, error) {
	var required uint32
	procSetupDiGetInterfaceDetailW.Call(info, uintptr(unsafe.Pointer(iface)), 0, 0, uintptr(unsafe.Pointer(&required)), 0)
	if required < 8 || required > 65536 {
		return "", errors.New("Invalid HID interface detail size")
	}
	// A word-sized backing array gives the record its required alignment.
	storage := make([]uintptr, (uintptr(required)+unsafe.Sizeof(uintptr(0))-1)/unsafe.Sizeof(uintptr(0)))
	detail := unsafe.Pointer(&storage[0])
	cbSize := uint32(6)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		cbSize = 8
	}
	*(*uint32)(detail) = cbSize
	ok, _, callErr := procSetupDiGetInterfaceDetailW.Call(info, uintptr(unsafe.Pointer(iface)), uintptr(detail), uintptr(required), 0, 0)
	if ok == 0 {
		return "", winError(callErr)
	}
	return windows.UTF16PtrToString((*uint16)(unsafe.Add(detail, 4))), nil
}

// capsMatch checks HIDP_CAPS: usage@0 = 1, usage page@2 = 0xff31, feature length@8 = 64.
func capsMatch(handle windows.Handle) (bool, error) {
	var prep uintptr
	ok, _, callErr := procHidDGetPreparsedData.Call(uintptr(handle), uintptr(unsafe.Pointer(&prep)))
	if ok == 0 {
		return false, winError(callErr)
	}
	var caps [32]uint16 // HIDP_CAPS is 64 bytes.
	status, _, _ := procHidPGetCaps.Call(prep, uintptr(unsafe.Pointer(&caps[0])))
	procHidDFreePreparsedData.Call(prep)
	return uint32(status) == hidpStatusSuccess && caps[0] == 1 && caps[1] == usagePageVendor && caps[4] == ReportSize, nil
}
