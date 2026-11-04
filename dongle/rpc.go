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
	c.mu.Lock()
	defer c.mu.Unlock()
	return typedRequest(c.dev, c.clock, opFeature, payload, generation)
}

// TypedRequest runs one request directly on a device with the wall clock.
func TypedRequest(dev hid.Device, payload []byte, generation *uint32) ([]byte, error) {
	return typedRequest(dev, RealClock, opFeature, payload, generation)
}

type rpcSession struct {
	dev        hid.Device
	clock      Clock
	generation uint32
	sequence   uint32
}

func currentGeneration(dev hid.Device) (uint32, error) {
	raw, err := dev.Get(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(raw[8:]), nil
}

func (r *rpcSession) checkGeneration(message string) error {
	current, err := currentGeneration(r.dev)
	if err != nil {
		return err
	}
	if current != r.generation {
		return errors.New(message)
	}
	return nil
}

func (r *rpcSession) send(op byte, total, offset uint16, payload []byte) error {
	if err := r.checkGeneration("Pico session changed during request"); err != nil {
		return err
	}
	var data [hid.ReportSize]byte
	header := hid.Header(3)
	copy(data[:], header[:])
	data[7] = op
	le := binary.LittleEndian
	le.PutUint32(data[8:], r.generation)
	le.PutUint32(data[12:], r.sequence)
	le.PutUint16(data[16:], total)
	le.PutUint16(data[18:], offset)
	data[20] = byte(len(payload))
	copy(data[22:], payload)
	return r.dev.Set(data)
}

func (r *rpcSession) wait(phase byte, offset uint16) ([hid.ReportSize]byte, error) {
	var none [hid.ReportSize]byte
	le := binary.LittleEndian
	deadline := r.clock.Now().Add(rpcWait)
	for r.clock.Now().Before(deadline) {
		if err := r.checkGeneration("Pico session changed during request"); err != nil {
			return none, err
		}
		data, err := r.dev.Get(3)
		if err != nil {
			return none, err
		}
		if le.Uint32(data[8:]) == r.generation && le.Uint32(data[12:]) == r.sequence {
			if data[21] == phaseFailed {
				if data[22] == codeUnsupported {
					return none, &Unsupported{"Firmware does not support this request for this device"}
				}
				return none, &RequestRejected{data[22]}
			}
			if data[21] == phase && le.Uint16(data[18:]) == offset {
				if data[20] > maxFragment {
					return none, errors.New("Oversized HID page")
				}
				return data, nil
			}
		}
		r.clock.Sleep(rpcPoll)
	}
	return none, ErrRPCTimeout
}

func typedRequest(dev hid.Device, clock Clock, op byte, payload []byte, generation *uint32) ([]byte, error) {
	if len(payload) < 1 || len(payload) > maxFragment {
		return nil, errors.New("Typed requests must fit one 1..42 byte HID fragment")
	}
	current, err := currentGeneration(dev)
	if err != nil {
		return nil, err
	}
	if generation != nil && *generation != current {
		return nil, errors.New("Pico session changed; run the command again")
	}
	initial, err := dev.Get(3)
	if err != nil {
		return nil, err
	}
	if initial[21] == phaseStaged || initial[21] == phaseProcessing {
		return nil, &RequestRejected{2}
	}
	le := binary.LittleEndian
	sequence := le.Uint32(initial[12:]) + 1
	if sequence == 0 {
		sequence = 1
	}
	session := &rpcSession{dev: dev, clock: clock, generation: current, sequence: sequence}

	if err := session.send(op, uint16(len(payload)), 0, payload); err != nil {
		return nil, err
	}
	if _, err := session.wait(phaseStaged, uint16(len(payload))); err != nil {
		return nil, err
	}
	if err := session.send(opCommit, 0, 0, nil); err != nil {
		return nil, err
	}
	page, err := session.wait(phaseReady, 0)
	if err != nil {
		return nil, err
	}
	total := int(le.Uint16(page[16:]))
	if total > maxResponse {
		return nil, errors.New("Invalid diagnostic length")
	}
	output := make([]byte, 0, total)
	for total != 0 {
		if int(le.Uint16(page[16:])) != total || page[20] == 0 {
			return nil, errors.New("Invalid diagnostic pagination")
		}
		output = append(output, page[22:22+int(page[20])]...)
		if len(output) > total {
			return nil, errors.New("Diagnostic page exceeds declared length")
		}
		if len(output) == total {
			break
		}
		if err := session.send(opPage, 0, uint16(len(output)), nil); err != nil {
			return nil, err
		}
		if page, err = session.wait(phaseReady, uint16(len(output))); err != nil {
			return nil, err
		}
	}
	if err := session.checkGeneration("Pico session changed during read"); err != nil {
		return nil, err
	}
	return output, nil
}
