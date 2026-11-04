package dongle

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var aapHeader = []byte{4, 0, 4, 0}

// Metadata is what the AirPods say about themselves.
type Metadata struct {
	Model    string
	Firmware string
	Serial   string
}

// FirmwareMajor reads the major version of a public AirPods firmware name such as
// 9A348 or 6F21a, or a legacy dotted one such as 6.8.8. Anything else is 0, so an
// internal build never passes a version gate.
func FirmwareMajor(firmware string) int {
	value := strings.TrimSpace(firmware)
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 || end == len(value) {
		return 0
	}
	major, err := strconv.Atoi(value[:end])
	if err != nil || major < 1 || major > 99 {
		return 0
	}
	if value[end] == '.' {
		parts := strings.Split(value, ".")
		if len(parts) != 3 {
			return 0
		}
		for _, part := range parts {
			number, err := strconv.ParseUint(part, 10, 8)
			if err != nil || number > 255 || strings.ContainsAny(part, "+-") {
				return 0
			}
		}
		return major
	}
	if value[end] < 'A' || value[end] > 'Z' {
		return 0
	}
	build := value[end+1:]
	digits := 0
	for digits < len(build) && build[digits] >= '0' && build[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0
	}
	if digits == len(build) || (digits+1 == len(build) && build[digits] >= 'a' && build[digits] <= 'z') {
		return major
	}
	return 0
}

// packedFirmware decodes release firmware reported as a packed decimal number. Values
// with a nonzero low nibble are prereleases and are deliberately left undecoded.
func packedFirmware(value string) string {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return ""
	}
	packed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return ""
	}
	major := packed >> 20
	if major < 1 || major > 99 || packed&0xf != 0 {
		return ""
	}
	return fmt.Sprintf("%d%c%d", major, 'A'+rune(packed>>16&0xf), packed>>4&0xfff)
}

// ParseMetadata reads the information packet: a five byte header, then eleven
// NUL-terminated UTF-8 fields. It returns false for anything else.
func ParseMetadata(packet []byte) (Metadata, bool) {
	if len(packet) < 11 || !bytes.Equal(packet[:4], aapHeader) || packet[4] != 0x1d || packet[5] != 0 {
		return Metadata{}, false
	}
	rest := packet[11:]
	fields := make([]string, 0, 11)
	for range 11 {
		end := bytes.IndexByte(rest, 0)
		if end < 0 || !utf8.Valid(rest[:end]) {
			return Metadata{}, false
		}
		field := string(rest[:end])
		if strings.ContainsFunc(field, unicode.IsControl) {
			return Metadata{}, false
		}
		fields = append(fields, field)
		rest = rest[end+1:]
	}
	meta := Metadata{Model: fields[1], Serial: fields[3]}
	switch {
	case FirmwareMajor(fields[10]) > 0:
		meta.Firmware = fields[10]
	case FirmwareMajor(fields[4]) > 0:
		meta.Firmware = fields[4]
	default:
		meta.Firmware = packedFirmware(fields[10])
	}
	if meta.Model == "" && meta.Firmware == "" {
		return Metadata{}, false
	}
	return meta, true
}
