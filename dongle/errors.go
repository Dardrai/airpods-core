package dongle

import (
	"errors"
	"strconv"
)

// Unconfirmed means a write may have applied but no confirmation arrived (exit 3).
type Unconfirmed struct {
	Message string
	Err     error
}

func (e *Unconfirmed) Error() string { return e.Message }
func (e *Unconfirmed) Unwrap() error { return e.Err }

// Unsupported means the device refused a request as unsupported (phase 4, code 7).
type Unsupported struct{ Message string }

func (e *Unsupported) Error() string { return e.Message }

// RequestRejected is a phase 4 failure of a typed request, or a busy channel.
type RequestRejected struct{ Code byte }

func (e *RequestRejected) Error() string {
	message := ""
	switch e.Code {
	case 1:
		message = "malformed request"
	case 2:
		message = "busy; close competing companion controls"
	case 3:
		message = "stale session"
	case 4:
		message = "AirPods disconnected"
	case 5:
		message = "device timeout"
	case 6:
		message = "transport failure"
	case 8:
		message = "cancelled"
	default:
		message = strconv.Itoa(int(e.Code))
	}
	return "RPC failure: " + message
}

// ErrRPCTimeout is the bounded wait expiring.
var ErrRPCTimeout = errors.New("Pico diagnostic read timed out")

// Conditions a caller can branch on with errors.Is. Errors may word them differently, and
// keep their own text, while still matching.
var (
	// ErrGenerationChanged means the connection changed under a request pinned to it.
	ErrGenerationChanged = errors.New(generationChangedText)
	// ErrNotReady means the AirPods settings connection is not up yet.
	ErrNotReady = errors.New("AirPods settings connection is not ready")
	// ErrMicrophoneActive means settings cannot change while the microphone is in use.
	ErrMicrophoneActive = errors.New("Stop microphone use before changing AirPods settings")
	// ErrNotSent means a request failed before anything was handed to the device, so it
	// cannot have applied. Any failure not matching it may have reached the dongle.
	ErrNotSent = errors.New("The request was not sent")
)

// rewordedError shows its own text and matches the condition it wraps.
type rewordedError struct {
	text string
	err  error
}

func (e *rewordedError) Error() string { return e.text }
func (e *rewordedError) Unwrap() error { return e.err }

// notSentError keeps an error's text and identity and adds ErrNotSent.
type notSentError struct{ err error }

func (e *notSentError) Error() string   { return e.err.Error() }
func (e *notSentError) Unwrap() []error { return []error{e.err, ErrNotSent} }

// NotSent marks an error as one from before anything was sent, for code that checks
// preconditions around a Session. A nil error stays nil.
func NotSent(err error) error {
	if err == nil || errors.Is(err, ErrNotSent) {
		return err
	}
	return &notSentError{err}
}

// Control receipt results (Status.ReceiptResult).
const (
	ResultOK           byte = 1
	ResultBusy         byte = 2
	ResultStale        byte = 3
	ResultUnsupported  byte = 4
	ResultDisconnected byte = 5
	ResultMalformed    byte = 6
)

// ControlRejected is a control command refused by the dongle, or refused locally
// before sending because the same check would fail on the dongle.
type ControlRejected struct {
	Opcode byte
	Result byte
}

func (e *ControlRejected) Error() string {
	message := "malformed"
	switch e.Result {
	case ResultBusy:
		message = "busy; stop USB audio, microphone and phone use first"
	case ResultStale:
		message = "stale session"
	case ResultUnsupported:
		message = "not supported by this dongle"
	case ResultDisconnected:
		message = "AirPods disconnected"
	}
	return "Control failure: " + message
}
