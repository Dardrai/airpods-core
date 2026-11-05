package dongle

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"github.com/Dardrai/airpods-core/hid"
)

// Clock is the time source for protocol waits; tests substitute a virtual one.
type Clock interface {
	Now() time.Time
	Sleep(time.Duration)
}

type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }
func (realClock) SleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RealClock is the wall clock.
var RealClock Clock = realClock{}

// ContextSleeper is an optional addition to a Clock: a sleep a context can cut short. A Clock
// without it finishes each sleep first, and the context is looked at right afterwards.
type ContextSleeper interface {
	SleepContext(ctx context.Context, d time.Duration) error
}

func sleepContext(ctx context.Context, clock Clock, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sleeper, ok := clock.(ContextSleeper); ok {
		return sleeper.SleepContext(ctx, d)
	}
	clock.Sleep(d)
	return ctx.Err()
}

// contextClock is a client's Clock when the client has a context to honour.
type contextClock struct {
	Clock
	ctx context.Context
}

func (c contextClock) Sleep(d time.Duration) { _ = sleepContext(c.ctx, c.Clock, d) }

const (
	maxFragment     = 42
	maxResponse     = 4096
	rpcWait         = 4 * time.Second
	rpcPoll         = 5 * time.Millisecond
	phaseStaged     = 1
	phaseProcessing = 2
	phaseReady      = 3
	phaseFailed     = 4
	opPage          = 4
	opCommit        = 6
	opFeature       = 7
	opCached        = 3
	codeUnsupported = 7
)

// Client serialises protocol transactions on one device. Everything derived from it, including
// the views WithContext returns, shares that serialisation, so it can be used from several
// goroutines; going to the device directly gives that up.
type Client struct {
	dev        hid.Device
	clock      Clock
	ctx        context.Context
	turn       chan struct{} // holds one token while a transaction runs
	controlSeq *uint32
}

// NewClient wraps an open device. The caller keeps ownership of the device, and closes it
// either directly or through Client.Close.
func NewClient(dev hid.Device) *Client {
	return &Client{dev: dev, clock: RealClock, ctx: context.Background(), turn: make(chan struct{}, 1), controlSeq: new(uint32)}
}

// SetClock replaces the time source; call it before using the client or deriving from it.
func (c *Client) SetClock(clock Clock) { c.clock = clock }

// Device returns the wrapped device. Traffic sent straight to it is not serialised with
// the client's; it is there for what the Device interface offers beyond the protocol.
func (c *Client) Device() hid.Device { return c.dev }

// WithContext returns a view of the client that gives up when ctx ends, sharing its device and
// its turn. Calls on the view, and sessions and settings clients derived from it, then fail
// with ctx.Err() in their chain, while waiting for their turn, between polls and during
// sleeps on Clock.
//
// What it does not do: a single HID transfer always runs to completion, which is bounded by the
// system (see hid.Device). A request stopped halfway is still cancelled on the dongle first,
// taking up to a second, and Session.Close still gives the lease back. A write stopped after it
// may have gone out is reported as Unconfirmed, like any other uncertain outcome.
func (c *Client) WithContext(ctx context.Context) *Client {
	if ctx == nil {
		panic("dongle: nil context")
	}
	view := *c
	view.ctx = ctx
	return &view
}

// Context returns the context of this view, context.Background() unless WithContext set one.
func (c *Client) Context() context.Context { return c.ctx }

// Close waits for the transaction under way, then closes the device. It is for callers that
// hand the device over to the client; a call under way ends sooner if its context does.
func (c *Client) Close() error {
	c.turn <- struct{}{}
	defer c.release()
	return c.dev.Close()
}

// acquire waits for this client's turn on the device, or for its context to end.
func (c *Client) acquire() error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	select {
	case c.turn <- struct{}{}:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

func (c *Client) release() { <-c.turn }

// Status reads and decodes report 2.
func (c *Client) Status() (Status, error) {
	if err := c.acquire(); err != nil {
		return Status{}, err
	}
	defer c.release()
	raw, err := c.dev.Get(2)
	if err != nil {
		return Status{}, err
	}
	return DecodeStatus(raw), nil
}

// TypedRequest runs one bounded report-3 request and never retries an uncertain
// write. A non-nil generation pins the request to that connection generation. A failure
// matching ErrNotSent cannot have reached the dongle.
func (c *Client) TypedRequest(payload []byte, generation *uint32) ([]byte, error) {
	return c.typedRequest(opFeature, payload, generation)
}

func (c *Client) typedRequest(op byte, payload []byte, generation *uint32) ([]byte, error) {
	if err := c.acquire(); err != nil {
		return nil, NotSent(err)
	}
	defer c.release()
	t := &transaction{dev: c.dev, clock: c.clock, ctx: c.ctx, rules: typedRules}
	if generation != nil {
		t.generation, t.pinned = *generation, true
	}
	return t.run(op, payload)
}

func currentGeneration(dev hid.Device) (uint32, error) {
	raw, err := dev.Get(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(raw[8:]), nil
}

// transactionRules are what one kind of request allows and how it words its failures.
type transactionRules struct {
	maxPayload     int
	sizeText       string
	changedText    string // the generation changed during the request
	changedEnd     string // the generation changed by the time the reply was complete
	paginationText string
	pageWait       time.Duration
}

var typedRules = transactionRules{
	maxPayload:     maxFragment,
	sizeText:       "Typed requests must fit one 1..42 byte HID fragment",
	changedText:    "Pico session changed during request",
	changedEnd:     "Pico session changed during read",
	paginationText: "Invalid diagnostic pagination",
	pageWait:       rpcWait,
}

var sessionRules = transactionRules{
	maxPayload:     maxPayload,
	sizeText:       "Invalid dongle settings payload length.",
	changedText:    generationChangedText,
	changedEnd:     generationChangedText,
	paginationText: "Dongle returned an incomplete response.",
	pageWait:       pageWait,
}

const generationChangedText = "Pico session changed; run the command again"

// transaction is one report-3 request at a time on a device, under the client's lock: staging,
// commit and paged reply. A failure cancels the request unless the dongle ended it itself, and
// nothing here is ever retried.
type transaction struct {
	dev   hid.Device
	clock Clock
	ctx   context.Context
	rules transactionRules

	generation uint32
	pinned     bool // the generation is fixed; otherwise the request adopts the current one
	sequence   uint32
	active     bool // the dongle may still hold this request
	attempted  bool // a Set of this request went to the system, so it may have reached the dongle
}

func (t *transaction) checkGeneration(message string) error {
	current, err := currentGeneration(t.dev)
	if err != nil {
		return err
	}
	if current != t.generation {
		return &rewordedError{message, ErrGenerationChanged}
	}
	return nil
}

func (t *transaction) send(op byte, total, offset int, data []byte) error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if err := t.checkGeneration(t.rules.changedText); err != nil {
		return err
	}
	err := t.dev.Set(t.report(op, total, offset, data))
	// A closed device sent nothing; any other outcome may have reached the dongle.
	if !errors.Is(err, hid.ErrClosed) {
		t.attempted = true
	}
	return err
}

func (t *transaction) report(op byte, total, offset int, data []byte) [hid.ReportSize]byte {
	var report [hid.ReportSize]byte
	header := hid.Header(3)
	copy(report[:], header[:])
	report[7] = op
	le := binary.LittleEndian
	le.PutUint32(report[8:], t.generation)
	le.PutUint32(report[12:], t.sequence)
	le.PutUint16(report[16:], uint16(total))
	le.PutUint16(report[18:], uint16(offset))
	report[20] = byte(len(data))
	copy(report[22:], data)
	return report
}

// wait polls report 3 until accept holds, a failure arrives or time runs out.
func (t *transaction) wait(limit time.Duration, accept func(report [hid.ReportSize]byte) bool) ([hid.ReportSize]byte, error) {
	var none [hid.ReportSize]byte
	le := binary.LittleEndian
	deadline := t.clock.Now().Add(limit)
	for t.clock.Now().Before(deadline) {
		if err := t.ctx.Err(); err != nil {
			return none, err
		}
		if err := t.checkGeneration(t.rules.changedText); err != nil {
			return none, err
		}
		report, err := t.dev.Get(3)
		if err != nil {
			return none, err
		}
		if le.Uint32(report[8:]) == t.generation && le.Uint32(report[12:]) == t.sequence {
			if report[21] == phaseFailed {
				// The dongle has ended the request, so there is nothing left to cancel.
				t.active = false
				if report[22] == codeUnsupported {
					return none, &Unsupported{"Firmware does not support this request for this device"}
				}
				return none, &RequestRejected{report[22]}
			}
			if accept(report) {
				return report, nil
			}
		}
		_ = sleepContext(t.ctx, t.clock, rpcPoll) // An ended context is seen at the top.
	}
	return none, ErrRPCTimeout
}

// run performs one request. A channel another client is using is a RequestRejected with code 2;
// a request left on it by an earlier generation is cancelled first.
// A failure from before the first Set matches ErrNotSent, as does that Set failing on a closed device.
func (t *transaction) run(op byte, payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > t.rules.maxPayload {
		return nil, NotSent(errors.New(t.rules.sizeText))
	}
	if t.active {
		return nil, NotSent(errors.New("The previous dongle request has not finished cancelling."))
	}
	t.attempted = false
	if err := t.ctx.Err(); err != nil {
		return nil, NotSent(err)
	}
	current, err := currentGeneration(t.dev)
	if err != nil {
		return nil, NotSent(err)
	}
	if t.pinned && current != t.generation {
		return nil, NotSent(ErrGenerationChanged)
	}
	t.generation = current
	initial, err := t.dev.Get(3)
	if err != nil {
		return nil, NotSent(err)
	}
	if initial[21] == phaseStaged || initial[21] == phaseProcessing {
		le := binary.LittleEndian
		if le.Uint32(initial[8:]) == current {
			return nil, NotSent(&RequestRejected{2})
		}
		// A request of an earlier generation is nobody's any more: a reconnect cut it off.
		t.generation, t.sequence = le.Uint32(initial[8:]), le.Uint32(initial[12:])
		dropped := t.drop()
		t.generation = current
		if !dropped {
			return nil, NotSent(&RequestRejected{2})
		}
		if initial, err = t.dev.Get(3); err != nil {
			return nil, NotSent(err)
		}
	}
	t.sequence = binary.LittleEndian.Uint32(initial[12:]) + 1
	if t.sequence == 0 {
		t.sequence = 1
	}
	t.active = true
	reply, err := t.exchange(op, payload)
	if err != nil {
		sent := t.attempted
		t.cancel()
		if !sent {
			return nil, NotSent(err)
		}
		return nil, err
	}
	t.active = false
	return reply, nil
}

func (t *transaction) exchange(op byte, payload []byte) ([]byte, error) {
	le := binary.LittleEndian
	for offset := 0; offset < len(payload); offset += maxFragment {
		chunk := payload[offset:min(offset+maxFragment, len(payload))]
		if err := t.send(op, len(payload), offset, chunk); err != nil {
			return nil, err
		}
		end := offset + len(chunk)
		if _, err := t.wait(rpcWait, func(r [hid.ReportSize]byte) bool {
			return r[21] == phaseStaged && int(le.Uint16(r[16:])) == len(payload) && int(le.Uint16(r[18:])) == end
		}); err != nil {
			return nil, err
		}
	}
	if err := t.send(opCommit, 0, 0, nil); err != nil {
		return nil, err
	}
	report, err := t.wait(rpcWait, func(r [hid.ReportSize]byte) bool {
		return r[21] == phaseReady && le.Uint16(r[18:]) == 0
	})
	if err != nil {
		return nil, err
	}
	total := int(le.Uint16(report[16:]))
	if total > maxResponse {
		return nil, errors.New("Invalid diagnostic length")
	}
	output := make([]byte, 0, total)
	for {
		size := int(report[20])
		if size > maxFragment {
			return nil, errors.New("Oversized HID page")
		}
		if int(le.Uint16(report[16:])) != total || int(le.Uint16(report[18:])) != len(output) || (size == 0 && len(output) < total) {
			return nil, errors.New(t.rules.paginationText)
		}
		output = append(output, report[22:22+size]...)
		if len(output) > total {
			return nil, errors.New("Diagnostic page exceeds declared length")
		}
		if len(output) == total {
			break
		}
		if err := t.send(opPage, 0, len(output), nil); err != nil {
			return nil, err
		}
		next := len(output)
		report, err = t.wait(t.rules.pageWait, func(r [hid.ReportSize]byte) bool {
			return r[21] == phaseReady && int(le.Uint16(r[18:])) == next
		})
		if err != nil {
			return nil, err
		}
	}
	if err := t.checkGeneration(t.rules.changedEnd); err != nil {
		return nil, err
	}
	return output, nil
}

// cancel asks the dongle to drop the request, since later traffic must not overtake it. A
// request that never reached a Set has nothing to drop. It runs whatever the context says and
// whatever the generation has become, so a stopped request does not leave the channel busy.
func (t *transaction) cancel() {
	if !t.attempted {
		t.active = false
	}
	if t.active && t.drop() {
		t.active = false
	}
}

// drop cancels the request of t.generation and t.sequence and waits, within cancelWait, for the
// dongle to end it. The cancel goes out without a look at the current generation: the dongle
// answers one from an earlier generation with a failure too, and that is what frees the channel.
func (t *transaction) drop() bool {
	if t.dev.Set(t.report(opCancel, 0, 0, nil)) != nil {
		return false
	}
	le := binary.LittleEndian
	deadline := t.clock.Now().Add(cancelWait)
	for t.clock.Now().Before(deadline) {
		report, err := t.dev.Get(3)
		if err != nil || le.Uint32(report[8:]) != t.generation {
			return false
		}
		if le.Uint32(report[12:]) == t.sequence && report[21] == phaseFailed {
			return true
		}
		t.clock.Sleep(rpcPoll)
	}
	return false
}
