package dongle

import (
	"encoding/binary"
	"errors"
	"sync"
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

// RealClock is the wall clock.
var RealClock Clock = realClock{}

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

// Client serialises protocol transactions on one device.
type Client struct {
	dev        hid.Device
	clock      Clock
	mu         sync.Mutex
	controlSeq uint32
}

// NewClient wraps an open device. The caller keeps ownership of closing it.
func NewClient(dev hid.Device) *Client { return &Client{dev: dev, clock: RealClock} }

// SetClock replaces the time source; call it before using the client.
func (c *Client) SetClock(clock Clock) { c.clock = clock }

// Device returns the wrapped device.
func (c *Client) Device() hid.Device { return c.dev }

// Status reads and decodes report 2.
func (c *Client) Status() (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := c.dev.Get(2)
	if err != nil {
		return Status{}, err
	}
	return DecodeStatus(raw), nil
}

// TypedRequest runs one bounded report-3 request and never retries an uncertain
// write. A non-nil generation pins the request to that connection generation.
func (c *Client) TypedRequest(payload []byte, generation *uint32) ([]byte, error) {
	return c.typedRequest(opFeature, payload, generation)
}

func (c *Client) typedRequest(op byte, payload []byte, generation *uint32) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &transaction{dev: c.dev, clock: c.clock, rules: typedRules}
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
	rules transactionRules

	generation uint32
	pinned     bool // the generation is fixed; otherwise the request adopts the current one
	sequence   uint32
	active     bool // the dongle may still hold this request
	attempted  bool // a Set of this request was tried, so it may have reached the dongle
}

func (t *transaction) checkGeneration(message string) error {
	current, err := currentGeneration(t.dev)
	if err != nil {
		return err
	}
	if current != t.generation {
		return errors.New(message)
	}
	return nil
}

func (t *transaction) send(op byte, total, offset int, data []byte) error {
	if err := t.checkGeneration(t.rules.changedText); err != nil {
		return err
	}
	t.attempted = true
	return t.dev.Set(t.report(op, total, offset, data))
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
		t.clock.Sleep(rpcPoll)
	}
	return none, ErrRPCTimeout
}

// run performs one request. A channel another client is using is a RequestRejected with code 2.
func (t *transaction) run(op byte, payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > t.rules.maxPayload {
		return nil, errors.New(t.rules.sizeText)
	}
	if t.active {
		return nil, errors.New("The previous dongle request has not finished cancelling.")
	}
	t.attempted = false
	current, err := currentGeneration(t.dev)
	if err != nil {
		return nil, err
	}
	if t.pinned && current != t.generation {
		return nil, errors.New(generationChangedText)
	}
	t.generation = current
	initial, err := t.dev.Get(3)
	if err != nil {
		return nil, err
	}
	if initial[21] == phaseStaged || initial[21] == phaseProcessing {
		return nil, &RequestRejected{2}
	}
	t.sequence = binary.LittleEndian.Uint32(initial[12:]) + 1
	if t.sequence == 0 {
		t.sequence = 1
	}
	t.active = true
	reply, err := t.exchange(op, payload)
	if err != nil {
		t.cancel()
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

// cancel asks the dongle to drop the request and waits for its receipt, since later traffic
// must not overtake it. A request that never reached a Set has nothing to drop.
func (t *transaction) cancel() {
	if !t.attempted {
		t.active = false
	}
	if !t.active || t.send(opCancel, 0, 0, nil) != nil {
		return
	}
	le := binary.LittleEndian
	deadline := t.clock.Now().Add(cancelWait)
	for t.clock.Now().Before(deadline) {
		report, err := t.dev.Get(3)
		if err != nil || le.Uint32(report[8:]) != t.generation {
			return
		}
		if le.Uint32(report[12:]) == t.sequence && report[21] == phaseFailed && report[22] == codeCancelled {
			t.active = false
			return
		}
		t.clock.Sleep(rpcPoll)
	}
}
