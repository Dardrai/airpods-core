package dongle

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/Dardrai/airpods-core/hid"
)

const (
	opExchange = 1 // ATT request with a response from the AirPods
	opSendAACP = 2 // AAP packet with no response of its own
	opCancel   = 5

	maxPayload    = 512
	cancelWait    = time.Second
	pageWait      = time.Second
	codeCancelled = 8
)

// Session is one run of related requests. Raw traffic to the AirPods needs the settings
// lease, which it holds from the first raw request until Close, renewing it before each
// request. If renewing fails the session is over: taking a new lease halfway through a
// read, change and verify could mix two sessions' views of the AirPods.
type Session struct {
	client     *Client
	generation uint32
	sequence   uint32
	token      uint32
	failed     bool
	active     bool
	leaseOK    bool
}

// ErrLeaseEnded means this session lost the lease and can do nothing further.
var ErrLeaseEnded = errors.New("Dongle settings lease ended. Read the settings again.")

// OpenSession pins a session to the current connection generation.
func (c *Client) OpenSession() (*Session, error) {
	status, err := c.Status()
	if err != nil {
		return nil, err
	}
	return &Session{client: c, generation: status.Generation, leaseOK: status.Has(CapLease)}, nil
}

func (s *Session) dev() hid.Device { return s.client.dev }

func (s *Session) checkGeneration() error {
	current, err := currentGeneration(s.dev())
	if err != nil {
		return err
	}
	if current != s.generation {
		return errors.New("Pico session changed; run the command again")
	}
	return nil
}

func (s *Session) send(op byte, total, offset int, data []byte) error {
	if err := s.checkGeneration(); err != nil {
		return err
	}
	var report [hid.ReportSize]byte
	header := hid.Header(3)
	copy(report[:], header[:])
	report[7] = op
	le := binary.LittleEndian
	le.PutUint32(report[8:], s.generation)
	le.PutUint32(report[12:], s.sequence)
	le.PutUint16(report[16:], uint16(total))
	le.PutUint16(report[18:], uint16(offset))
	report[20] = byte(len(data))
	copy(report[22:], data)
	return s.dev().Set(report)
}

// wait polls report 3 until accept holds, a failure arrives or time runs out.
func (s *Session) wait(limit time.Duration, accept func(report [hid.ReportSize]byte) bool) ([hid.ReportSize]byte, error) {
	var none [hid.ReportSize]byte
	le := binary.LittleEndian
	clock := s.client.clock
	deadline := clock.Now().Add(limit)
	for clock.Now().Before(deadline) {
		if err := s.checkGeneration(); err != nil {
			return none, err
		}
		report, err := s.dev().Get(3)
		if err != nil {
			return none, err
		}
		if le.Uint32(report[8:]) == s.generation && le.Uint32(report[12:]) == s.sequence {
			if report[21] == phaseFailed {
				if report[22] == codeUnsupported {
					return none, &Unsupported{"Firmware does not support this request for this device"}
				}
				return none, &RequestRejected{report[22]}
			}
			if accept(report) {
				return report, nil
			}
		}
		clock.Sleep(rpcPoll)
	}
	return none, ErrRPCTimeout
}

// transact runs one request of any size. A failure cancels it, and is never retried.
func (s *Session) transact(op byte, payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > maxPayload {
		return nil, errors.New("Invalid dongle settings payload length.")
	}
	if s.active {
		return nil, errors.New("The previous dongle request has not finished cancelling.")
	}
	if err := s.checkGeneration(); err != nil {
		return nil, err
	}
	initial, err := s.dev().Get(3)
	if err != nil {
		return nil, err
	}
	if initial[21] == phaseStaged || initial[21] == phaseProcessing {
		return nil, &RequestRejected{2}
	}
	le := binary.LittleEndian
	s.sequence = le.Uint32(initial[12:]) + 1
	if s.sequence == 0 {
		s.sequence = 1
	}
	s.active = true
	reply, err := s.run(op, payload)
	if err != nil {
		s.cancel()
		return nil, err
	}
	s.active = false
	return reply, nil
}

func (s *Session) run(op byte, payload []byte) ([]byte, error) {
	le := binary.LittleEndian
	for offset := 0; offset < len(payload); offset += maxFragment {
		chunk := payload[offset:min(offset+maxFragment, len(payload))]
		if err := s.send(op, len(payload), offset, chunk); err != nil {
			return nil, err
		}
		end := offset + len(chunk)
		if _, err := s.wait(rpcWait, func(r [hid.ReportSize]byte) bool {
			return r[21] == phaseStaged && int(le.Uint16(r[16:])) == len(payload) && int(le.Uint16(r[18:])) == end
		}); err != nil {
			return nil, err
		}
	}
	if err := s.send(opCommit, 0, 0, nil); err != nil {
		return nil, err
	}
	report, err := s.wait(rpcWait, func(r [hid.ReportSize]byte) bool {
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
			return nil, errors.New("Dongle returned an incomplete response.")
		}
		output = append(output, report[22:22+size]...)
		if len(output) > total {
			return nil, errors.New("Diagnostic page exceeds declared length")
		}
		if len(output) == total {
			break
		}
		if err := s.send(opPage, 0, len(output), nil); err != nil {
			return nil, err
		}
		next := len(output)
		report, err = s.wait(pageWait, func(r [hid.ReportSize]byte) bool {
			return r[21] == phaseReady && int(le.Uint16(r[18:])) == next
		})
		if err != nil {
			return nil, err
		}
	}
	return output, s.checkGeneration()
}

// cancel asks the dongle to drop the request and waits for its receipt, since cleanup
// traffic must not overtake it.
func (s *Session) cancel() {
	if !s.active || s.send(opCancel, 0, 0, nil) != nil {
		return
	}
	le := binary.LittleEndian
	clock := s.client.clock
	deadline := clock.Now().Add(cancelWait)
	for clock.Now().Before(deadline) {
		report, err := s.dev().Get(3)
		if err != nil || le.Uint32(report[8:]) != s.generation {
			return
		}
		if le.Uint32(report[12:]) == s.sequence && report[21] == phaseFailed && report[22] == codeCancelled {
			s.active = false
			return
		}
		clock.Sleep(rpcPoll)
	}
}

func (s *Session) ensureLease(raw bool) error {
	if s.failed {
		return ErrLeaseEnded
	}
	if s.token != 0 {
		s.failed = true
		reply, err := s.transact(opFeature, leaseRequest(1, s.token))
		if err != nil {
			return err
		}
		if len(reply) != 0 {
			return errors.New("Unsupported dongle settings lease renewal.")
		}
		s.failed = false
		return nil
	}
	if !raw {
		return nil
	}
	if !s.leaseOK {
		return errors.New("Update the dongle firmware before reading or changing hearing data.")
	}
	s.failed = true
	reply, err := s.transact(opFeature, leaseRequest(0, 0))
	if err != nil {
		return err
	}
	token, err := DecodeLeaseToken(reply)
	if err != nil {
		return err
	}
	s.token, s.failed = token, false
	return nil
}

func (s *Session) request(op byte, payload []byte) ([]byte, error) {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if err := s.ensureLease(op == opExchange || op == opSendAACP); err != nil {
		return nil, err
	}
	return s.transact(op, payload)
}

// AcquireLease takes the lease up front, so kept reports read now belong to the same
// lease as the writes and cleanup that follow them.
func (s *Session) AcquireLease() error {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	return s.ensureLease(true)
}

// Exchange sends an ATT request to the AirPods and returns their response.
func (s *Session) Exchange(pdu []byte) ([]byte, error) { return s.request(opExchange, pdu) }

// SendAAP sends an AAP packet to the AirPods. It reports delivery, not its effect.
func (s *Session) SendAAP(pdu []byte) error {
	_, err := s.request(opSendAACP, pdu)
	return err
}

// Feature runs a typed request inside the session.
func (s *Session) Feature(payload []byte) ([]byte, error) { return s.request(opFeature, payload) }

// Cached reads a kept report inside the session; nil means the slot holds nothing.
func (s *Session) Cached(slot byte) (*Cached, error) {
	if slot < SlotMetadata || slot > SlotEarStatus {
		return nil, errors.New("Unknown dongle report.")
	}
	reply, err := s.request(opCached, []byte{slot})
	if err != nil {
		return nil, err
	}
	return DecodeCached(reply)
}

// Close cancels anything unfinished and gives the lease back.
func (s *Session) Close() {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	s.cancel()
	token := s.token
	s.token, s.failed = 0, true
	if token == 0 {
		return
	}
	// A failed release just lets the lease expire on its own.
	_, _ = s.transact(opFeature, leaseRequest(2, token))
}
