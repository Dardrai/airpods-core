package dongle

import (
	"encoding/binary"
	"errors"
	"strconv"
)

// kindFirmwareInfo is the typed request kind of the firmware version report.
const kindFirmwareInfo = 24

// FirmwareInfo is the version of the firmware the dongle runs.
type FirmwareInfo struct {
	Major, Minor, Patch int
	// Experimental marks the experimental radio0076 build, which upstream releases ship.
	Experimental bool
}

// String is the version as the firmware's releases number it, such as 0.1.66.
func (f FirmwareInfo) String() string {
	return strconv.Itoa(f.Major) + "." + strconv.Itoa(f.Minor) + "." + strconv.Itoa(f.Patch)
}

// firmwareUnreportedText explains an Unsupported answer to the version request.
const firmwareUnreportedText = "the dongle firmware does not report its version; it is older than the release that adds the report"

// DecodeFirmwareInfo reads a version report: format 1, major, minor, patch (little-endian
// u16) and variant (0 standard, 1 experimental radio0076).
func DecodeFirmwareInfo(reply []byte) (FirmwareInfo, error) {
	if len(reply) != 6 || reply[0] != 1 || reply[5] > 1 {
		return FirmwareInfo{}, errors.New("Unsupported dongle firmware version response.")
	}
	return FirmwareInfo{
		Major: int(reply[1]), Minor: int(reply[2]), Patch: int(binary.LittleEndian.Uint16(reply[3:5])),
		Experimental: reply[5] == 1,
	}, nil
}

// FirmwareInfo reads the running firmware's version (kind 24). It is read-only and allowed
// at any time. Unsupported means firmware older than the release that adds the report.
func (c *Client) FirmwareInfo() (FirmwareInfo, error) {
	reply, err := c.TypedRequest([]byte{kindFirmwareInfo}, nil)
	var unsupported *Unsupported
	if errors.As(err, &unsupported) {
		return FirmwareInfo{}, &Unsupported{Message: firmwareUnreportedText}
	}
	if err != nil {
		return FirmwareInfo{}, err
	}
	return DecodeFirmwareInfo(reply)
}
