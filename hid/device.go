// Package hid is the transport to the dongle's vendor HID collection: 64-byte
// feature reports 2 and 3 with the header report,'A','P','D','G',1,64.
package hid

import "errors"

const (
	// ReportSize is the length of every feature report, including the report ID.
	ReportSize = 64
	// VendorID is the dongle's USB vendor ID.
	VendorID = 0xcafe
	// ProductAudio is the audio composite mode's product ID.
	ProductAudio = 0x4015
	// ProductControl is the control-only mode's product ID.
	ProductControl = 0x4005
)

// Device is one opened dongle. Get verifies the report identity. The devices Open returns
// let Close wait for a transfer in flight and fail later transfers with ErrClosed, but they
// do not keep multi-report protocol transactions together: use one dongle.Client for that.
//
// One transfer is bounded by the system rather than by a context: 400 ms on Windows
// (ErrFeatureTimeout), and on Linux by the kernel's USB control timeout of about 5 s, which
// hidraw offers no way to shorten or interrupt. It surfaces as ErrTimeout.
type Device interface {
	Get(report byte) ([ReportSize]byte, error)
	Set(data [ReportSize]byte) error
	Serial() string
	Close() error
}

// ErrUnsupportedReport is returned by Get when the report identity or protocol
// version is not the one this tool speaks.
var ErrUnsupportedReport = errors.New("Unsupported HID report identity/version")

// Conditions that apply on every platform. errors.Is finds them in the errors of Get, Set
// and Open, which keep their own text and still wrap the OS error underneath.
var (
	// ErrDisconnected means the device went away; a new Open is the only way back.
	ErrDisconnected = errors.New("HID device disconnected")
	// ErrTimeout means one transfer ran out of time. It may still have reached the device.
	ErrTimeout = errors.New("HID transfer timed out")
	// ErrClosed means the transfer was asked for after Close, and nothing was sent.
	ErrClosed = errors.New("HID device is closed")
)

// wrappedError shows its own text and wraps the OS error and the condition behind it.
type wrappedError struct {
	text   string
	causes []error
}

func (e *wrappedError) Error() string   { return e.text }
func (e *wrappedError) Unwrap() []error { return e.causes }

// systemError gives an OS error the text the apps show, keeping its identity for errors.Is
// and adding kind, the cross-platform condition, when there is one.
func systemError(text string, cause, kind error) error {
	causes := []error{cause}
	if kind != nil {
		causes = append(causes, kind)
	}
	return &wrappedError{text: text, causes: causes}
}

// Header returns the seven identity bytes of a report.
func Header(report byte) [7]byte {
	return [7]byte{report, 'A', 'P', 'D', 'G', 1, ReportSize}
}

// CheckReport verifies data[:7] against the report's identity and version.
func CheckReport(report byte, data [ReportSize]byte) error {
	if [7]byte(data[:7]) != Header(report) {
		return ErrUnsupportedReport
	}
	return nil
}

func isProduct(vid, pid uint16) bool {
	return vid == VendorID && (pid == ProductAudio || pid == ProductControl)
}

// MatchError is Open failing the exactly-one-match rule. Found tells no dongle from
// several.
type MatchError struct {
	Found int
	Text  string
}

func (e *MatchError) Error() string { return e.Text }

// exactlyOne keeps the single match or closes every candidate and fails with msg.
func exactlyOne[T any](found []T, closeFn func(T), msg string) (T, error) {
	if len(found) != 1 {
		for _, item := range found {
			closeFn(item)
		}
		var zero T
		return zero, &MatchError{Found: len(found), Text: msg}
	}
	return found[0], nil
}
