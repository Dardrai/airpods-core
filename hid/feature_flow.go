package hid

import "errors"

// Errors from one overlapped feature transfer.
var (
	// ErrFeatureTimeout is a Windows transfer exceeding its bound; it matches ErrTimeout.
	ErrFeatureTimeout error = &wrappedError{text: "USB feature transfer exceeded 400 ms", causes: []error{ErrTimeout}}
	errIncomplete           = errors.New("Incomplete USB feature report")
)

// featureWaitMillis bounds one overlapped transfer.
const featureWaitMillis = 400

// overlappedOps is the kernel side of one overlapped transfer. It is an interface
// so the cancel path, which cannot be provoked without a stalled device, has tests.
type overlappedOps interface {
	// issue starts the transfer; pending means the kernel still owns the buffers.
	issue() (pending bool, err error)
	// waitEvent reports whether the completion event was signalled in time.
	waitEvent(millis uint32) bool
	cancel()
	// result returns the transferred byte count, blocking until done when wait is set.
	result(wait bool) (uint32, error)
}

// runOverlapped runs one transfer. After a timeout, it blocks until cancellation
// completes because the kernel owns the buffer and OVERLAPPED until then.
func runOverlapped(ops overlappedOps, writing bool) error {
	pending, err := ops.issue()
	if err != nil {
		return err
	}
	if pending && !ops.waitEvent(featureWaitMillis) {
		ops.cancel()
		_, _ = ops.result(true)
		return ErrFeatureTimeout
	}
	count, err := ops.result(false)
	if err != nil {
		return err
	}
	if !writing && count != ReportSize {
		return errIncomplete
	}
	return nil
}
