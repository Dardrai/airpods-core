//go:build windows

package hid

import (
	"errors"
	"fmt"
	"strings"
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
	maxInterfaces         = 256
)

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

// winError formats an OS error as "[WinError N] text".
func winError(err error) error {
	var code windows.Errno
	if errors.As(err, &code) {
		return fmt.Errorf("[WinError %d] %s", uint32(code), code.Error())
	}
	return err
}

// Open finds exactly one vendor HID collection of the dongle, optionally by serial.
func Open(serial string) (Device, error) {
	var found []*device
	err := walkInterfaces(func(path string) error {
		if !pathMatches(path) {
			return nil
		}
		d, err := probe(path, serial)
		if err != nil {
			return err
		}
		if d != nil {
			found = append(found, d)
		}
		return nil
	})
	if err != nil {
		for _, d := range found {
			d.Close()
		}
		return nil, err
	}
	d, err := exactlyOne(found, func(d *device) { d.Close() }, "Expected exactly one matching Pico vendor HID collection")
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Enumerate lists every attached dongle without keeping any of them open. No dongle is
// an empty list, not an error.
func Enumerate() ([]Info, error) {
	var paths []string
	err := walkInterfaces(func(path string) error {
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return listInterfaces(paths, openQuery, openFull), nil
}

// walkInterfaces visits the path of every present HID interface until visit fails.
func walkInterfaces(visit func(path string) error) error {
	var guid windows.GUID
	procHidDGetHidGuid.Call(uintptr(unsafe.Pointer(&guid)))
	info, _, callErr := procSetupDiGetClassDevsW.Call(uintptr(unsafe.Pointer(&guid)), 0, 0, digcfPresentInterface)
	if info == uintptr(windows.InvalidHandle) {
		return winError(callErr)
	}
	defer procSetupDiDestroyInfoList.Call(info)

	for index := uintptr(0); index < maxInterfaces; index++ {
		var iface deviceInterfaceData
		iface.size = uint32(unsafe.Sizeof(iface))
		ok, _, callErr := procSetupDiEnumInterfaces.Call(info, 0, uintptr(unsafe.Pointer(&guid)), index, uintptr(unsafe.Pointer(&iface)))
		if ok == 0 {
			if errors.Is(callErr, errorNoMoreItems) {
				return nil
			}
			return winError(callErr)
		}
		path, err := interfacePath(info, &iface)
		if err != nil {
			return err
		}
		if err := visit(path); err != nil {
			return err
		}
	}
	return nil
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

// openFull tries the access Open asks for and lets go again.
func openFull(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return winError(err)
	}
	windows.CloseHandle(handle)
	return nil
}

func pathMatches(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, fmt.Sprintf("vid_cafe&pid_%04x", ProductAudio)) ||
		strings.Contains(lower, fmt.Sprintf("vid_cafe&pid_%04x", ProductControl))
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

// probe returns the opened device, or nil when this interface is not the one sought.
func probe(path, serial string) (*device, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, winError(err)
	}
	keep := false
	defer func() {
		if !keep {
			windows.CloseHandle(handle)
		}
	}()
	attr := hiddAttributes{}
	attr.size = uint32(unsafe.Sizeof(attr))
	ok, _, _ := procHidDGetAttributes.Call(uintptr(handle), uintptr(unsafe.Pointer(&attr)))
	if ok == 0 || !isProduct(attr.vid, attr.pid) {
		return nil, nil
	}
	match, err := capsMatch(handle)
	if err != nil || !match {
		return nil, err
	}
	var value [256]uint16
	ok, _, callErr := procHidDGetSerialNumberString.Call(uintptr(handle), uintptr(unsafe.Pointer(&value[0])), unsafe.Sizeof(value))
	if ok == 0 {
		return nil, winError(callErr)
	}
	found := windows.UTF16ToString(value[:])
	if serial != "" && found != serial {
		return nil, nil
	}
	keep = true
	return &device{handle: handle, serial: found}, nil
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
