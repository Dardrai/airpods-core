package dongle

import (
	"errors"
	"strconv"
)

// Unconfirmed means a write may have applied but no confirmation arrived (exit 3). It never
// matches ErrNotSent, even when Err does: Err is then a later request, such as a confirming
// read, that did not go out, which says nothing about the write. Whatever else Err matches,
// such as a context error or ErrGenerationChanged, the Unconfirmed matches too.
type Unconfirmed struct {
	Message string
	Err     error
}

func (e *Unconfirmed) Error() string { return e.Message }
func (e *Unconfirmed) Unwrap() error { return sent(e.Err) }

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
	// ErrNotSent means a request failed before any of it was handed to the device, or the
	// device refused its first report as closed, so that request cannot have applied. It
	// speaks for that one request only: status reads, lease traffic, the cancel of a stale
	// request and earlier requests of the same call may have gone out. A failure without it
	// is no proof of the opposite: some come from checks made before anything was sent.
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

// sentError keeps an error's text and what it matches, except ErrNotSent. It has no Unwrap,
// so errors.Is and errors.As see the chain only through its methods.
type sentError struct{ err error }

func (e *sentError) Error() string        { return e.err.Error() }
func (e *sentError) Is(target error) bool { return target != ErrNotSent && errors.Is(e.err, target) }
func (e *sentError) As(target any) bool   { return errors.As(e.err, target) }

// sent is for the cause of a failure that follows a write which went out: it takes ErrNotSent
// off an error and leaves the rest of its identity, since a later request that was not sent
// does not make the write unsent. Other errors, and nil, come back as they are.
func sent(err error) error {
	if !errors.Is(err, ErrNotSent) {
		return err
	}
	return &sentError{err}
}

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
