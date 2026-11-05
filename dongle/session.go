package dongle

import (
	"context"
	"errors"
	"time"
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
	client  *Client
	request transaction
	token   uint32
	failed  bool
	leaseOK bool
}

// ErrLeaseEnded means this session lost the lease and can do nothing further.
var ErrLeaseEnded = errors.New("Dongle settings lease ended. Read the settings again.")

// OpenSession pins a session to the current connection generation.
func (c *Client) OpenSession() (*Session, error) {
	status, err := c.Status()
	if err != nil {
		return nil, err
	}
	return &Session{
		client:  c,
		request: transaction{dev: c.dev, clock: c.clock, ctx: c.ctx, rules: sessionRules, generation: status.Generation, pinned: true},
		leaseOK: status.Has(CapLease),
	}, nil
}

func (s *Session) ensureLease(raw bool) error {
	if s.failed {
		return ErrLeaseEnded
	}
	if s.token != 0 {
		s.failed = true
		reply, err := s.request.run(opFeature, leaseRequest(1, s.token))
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
	reply, err := s.request.run(opFeature, leaseRequest(0, 0))
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

func (s *Session) do(op byte, payload []byte) ([]byte, error) {
	if err := s.client.acquire(); err != nil {
		return nil, NotSent(err)
	}
	defer s.client.release()
	if err := s.ensureLease(op == opExchange || op == opSendAACP); err != nil {
		return nil, NotSent(err) // Lease traffic only; this request has not gone out.
	}
	return s.request.run(op, payload)
}

// AcquireLease takes the lease up front, so kept reports read now belong to the same
// lease as the writes and cleanup that follow them.
func (s *Session) AcquireLease() error {
	if err := s.client.acquire(); err != nil {
		return err
	}
	defer s.client.release()
	return s.ensureLease(true)
}

// Exchange sends an ATT request to the AirPods and returns their response. A failure matching
// ErrNotSent never went out, here and in the other requests of a session.
func (s *Session) Exchange(pdu []byte) ([]byte, error) { return s.do(opExchange, pdu) }

// SendAAP sends an AAP packet to the AirPods. It reports delivery, not its effect.
func (s *Session) SendAAP(pdu []byte) error {
	_, err := s.do(opSendAACP, pdu)
	return err
}

// Feature runs a typed request inside the session.
func (s *Session) Feature(payload []byte) ([]byte, error) { return s.do(opFeature, payload) }

// Cached reads a kept report inside the session; nil means the slot holds nothing.
func (s *Session) Cached(slot byte) (*Cached, error) {
	if slot < SlotMetadata || slot > SlotEarStatus {
		return nil, errors.New("Unknown dongle report.")
	}
	reply, err := s.do(opCached, []byte{slot})
	if err != nil {
		return nil, err
	}
	return DecodeCached(reply)
}

// Close cancels anything unfinished and gives the lease back, even after the session's context
// has ended: it is the cleanup that lets the next user of the dongle in.
func (s *Session) Close() {
	s.client.turn <- struct{}{}
	defer s.client.release()
	s.request.ctx = context.Background()
	s.request.cancel()
	token := s.token
	s.token, s.failed = 0, true
	if token == 0 {
		return
	}
	// A failed release just lets the lease expire on its own.
	_, _ = s.request.run(opFeature, leaseRequest(2, token))
}
