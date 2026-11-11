// Package disconnect ends the AirPods connection while keeping them paired, on the
// dongles that can. Opcode 2 (pair) is never an option: it erases the saved pairing.
//
// Firmware 0.1.59 and newer advertise dongle.CapRouting and release the AirPods through
// the routing handover (typed kinds 20 and 21). After a release the dongle declines the
// AirPods' incoming connections until a reconnect (opcode 1), a button press, Acquire or a
// restart of the dongle. The release is not saved: under the Automatic reconnect policy
// (dongle.ReconnectAutomatic) a restarted dongle takes the AirPods back by itself.
package disconnect

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/Dardrai/airpods-core/dongle"
)

// ErrUnsupported is the result on a dongle without a way to disconnect.
var ErrUnsupported = errors.New("this dongle firmware has no command to disconnect AirPods while keeping them paired; update it to 0.1.59 or newer, or put the AirPods in their case to disconnect")

// ErrAcquireUnsupported is the result of Acquire on a dongle without the routing handover.
var ErrAcquireUnsupported = errors.New("this dongle firmware has no routing handover; use reconnect instead")

// ErrIntentChanged means another choice (a button press, a reconnect, another app, an
// automatic reconnect attempt) moved the routing intent first, or replaced an accepted
// request. Nothing more is sent; read the status again.
var ErrIntentChanged = errors.New("routing intent changed; read status again. The request was not retried")

// Backend kinds.
const (
	KindUnsupported = "unsupported"
	KindRouting     = "routing"
)

// Outcome is how far an accepted request got before the wait ended.
type Outcome int

// Outcomes. A pending outcome is not an error: the dongle accepted the request and is
// still carrying it out.
const (
	Released       Outcome = iota + 1 // the connection is closed; the pair is kept
	ReleasePending                    // release accepted, the connection not closed yet
	Acquired                          // the dongle owns the AirPods' audio
	AcquirePending                    // takeover accepted, still in progress
)

func (o Outcome) String() string {
	switch o {
	case Released:
		return "released; the AirPods stay paired"
	case ReleasePending:
		return "release accepted, not yet closed"
	case Acquired:
		return "the dongle has the AirPods"
	case AcquirePending:
		return "takeover accepted, still in progress"
	}
	return "unknown"
}

// Disconnector ends the AirPods connection.
type Disconnector interface {
	Disconnect() (Outcome, error)
	Kind() string
}

type unsupported struct{}

func (unsupported) Disconnect() (Outcome, error) { return 0, ErrUnsupported }
func (unsupported) Kind() string                 { return KindUnsupported }

// Pick returns the backend for a dongle with this status. The client is what a working
// backend sends through; it may be nil when only Kind is wanted.
func Pick(client *dongle.Client, status dongle.Status) Disconnector {
	if status.Has(dongle.CapRouting) {
		return routing{client}
	}
	return unsupported{}
}

// Acquire asks the dongle to take the AirPods back through the routing handover and waits
// for them to confirm it. It also ends a release. When the dongle already has them it sends
// nothing. On dongles without the handover it returns ErrAcquireUnsupported;
// dongle.Client.Reconnect is the plain alternative.
func Acquire(client *dongle.Client) (Outcome, error) {
	status, err := client.Status()
	if err != nil {
		return 0, err
	}
	if !status.Has(dongle.CapRouting) {
		return 0, ErrAcquireUnsupported
	}
	before, err := client.Routing()
	if err != nil {
		return 0, err
	}
	if acquired(before) {
		return Acquired, nil
	}
	return routing{client}.handover(dongle.HandoverAcquire, before, true)
}

// owning is the routing state of a finished takeover: the AirPods have confirmed the dongle,
// audio and controls are up, and nothing is pending or released.
const owning = dongle.RoutingKnown | dongle.RoutingOwned | dongle.RoutingA2DP | dongle.RoutingControlsReady

// acquired and released are the end states the firmware's own tool waits for. Released leaves
// out only whether the AirPods said who owns them; both leave out microphone use.
func acquired(r dongle.Routing) bool { return r.Flags&0x7f == owning }
func released(r dongle.Routing) bool { return r.Flags&0x7e == dongle.RoutingReleased }

// sameIntent reports whether two routing reads still describe the same accepted request.
func sameIntent(a, b dongle.Routing) bool {
	return a.Revision == b.Revision && a.Peer == b.Peer && a.LastAction == b.LastAction && a.LastNonce == b.LastNonce
}

// receiptOf reports whether a routing read is the receipt of this request: the dongle advances
// the revision by exactly one when it accepts a handover, and records its action and nonce.
func receiptOf(r, before dongle.Routing, action byte, nonce uint32) bool {
	next := before.Revision + 1
	if next == 0 {
		next = 1
	}
	return r.Revision == next && r.Peer == before.Peer && r.LastAction == action+1 && r.LastNonce == nonce
}

const (
	routingWait = 5 * time.Second
	routingPoll = 100 * time.Millisecond
	receiptWait = time.Second
)

// nonceSource draws handover nonces; tests replace it.
var nonceSource = func() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.LittleEndian.Uint32(b[:])
}

// newNonce is nonzero and differs from the last accepted nonce, which the dongle would
// treat as a repeat.
func newNonce(last uint32) uint32 {
	for {
		if n := nonceSource(); n != 0 && n != last {
			return n
		}
	}
}

// explained keeps the dongle's error visible to errors.As under a clearer message.
type explained struct {
	message string
	err     error
}

func (e *explained) Error() string { return e.message }
func (e *explained) Unwrap() error { return e.err }

type routing struct{ client *dongle.Client }

func (routing) Kind() string { return KindRouting }

// Disconnect releases the AirPods once and waits up to 5 s for the dongle to close the
// connection. A release that is accepted but not closed in time is ReleasePending. Like the
// firmware's own tool it sends the release even when the AirPods are not connected, since the
// release is also what keeps the dongle from taking them back.
func (b routing) Disconnect() (Outcome, error) {
	if b.client == nil {
		return 0, errors.New("no dongle")
	}
	before, err := b.client.Routing()
	if err != nil {
		return 0, err
	}
	switch {
	case released(before):
		return Released, nil
	case before.Has(dongle.RoutingReleased), before.Has(dongle.RoutingReleasePending):
		// An earlier release holds and is still closing; wait for it rather than send another.
		return b.wait(dongle.HandoverRelease, before)
	}
	return b.handover(dongle.HandoverRelease, before, true)
}

// handover sends one request quoting the revision of before. With retry, a stale refusal may
// lead to one fresh request; see stale.
func (b routing) handover(action byte, before dongle.Routing, retry bool) (Outcome, error) {
	name := [...]string{"release", "takeover"}[action]
	nonce := newNonce(before.LastNonce)
	after, err := b.client.Handover(action, before.Revision, nonce)
	var rejected *dongle.RequestRejected
	var unsupported *dongle.Unsupported
	switch {
	case err == nil:
	case errors.Is(err, dongle.ErrNotSent):
		// It never reached the dongle: a busy channel, an ended context, a closed device.
		return 0, err
	case errors.As(err, &rejected) && rejected.Code == 2:
		return 0, &explained{busyText(action), err}
	case errors.As(err, &rejected) && rejected.Code == 3 && retry:
		return b.stale(action, before)
	case errors.As(err, &rejected) && rejected.Code == 3:
		return 0, b.intentChanged(true)
	case errors.As(err, &rejected) && rejected.Code == 4:
		return 0, &explained{"the dongle has no saved AirPods pair to " + name, err}
	case errors.As(err, &rejected) && rejected.Code == 1, errors.As(err, &unsupported):
		return 0, err
	default:
		// The request may have been accepted; only its receipt tells, never a second send.
		if after, err = b.receipt(action, before, nonce, err); err != nil {
			return 0, err
		}
	}
	if !receiptOf(after, before, action, nonce) {
		return 0, fmt.Errorf("%w (now %s)", ErrIntentChanged, describe(after))
	}
	return b.wait(action, after)
}

// stale follows a handover refused because the routing revision moved after it was read. The
// dongle checks the revision before it changes anything, so nothing was applied. Under the
// Automatic reconnect policy every automatic attempt moves the revision while the AirPods are
// away, so one fresh request is worth it, but only when the move was not another app's choice:
// the end state already reached sends nothing, another accepted handover (its action shows) or
// another peer is reported as ErrIntentChanged, and so is a takeover in progress for a release,
// which the dongle would refuse as busy until it ends. An acquire follows that takeover instead.
// The fresh request is never retried in turn.
func (b routing) stale(action byte, before dongle.Routing) (Outcome, error) {
	now, err := b.client.Routing()
	if err != nil {
		return 0, ErrIntentChanged
	}
	switch {
	case action == dongle.HandoverRelease && released(now):
		return Released, nil
	case action == dongle.HandoverAcquire && acquired(now):
		return Acquired, nil
	case now.Revision == before.Revision || now.LastAction != 0 || now.Peer != before.Peer,
		action == dongle.HandoverRelease && now.Has(dongle.RoutingTakeoverPending):
		return 0, fmt.Errorf("%w (now %s)", ErrIntentChanged, describe(now))
	case now.Has(dongle.RoutingTakeoverPending):
		return b.wait(action, now)
	}
	return b.handover(action, now, false)
}

func busyText(action byte) string {
	if action == dongle.HandoverAcquire {
		return "the dongle cannot take the AirPods back right now: the microphone or a call is in use, a takeover or audio change is pending, audio is recovering, a Game Mode change is in flight, or another app holds the settings lease"
	}
	return "the dongle cannot release the AirPods right now: the microphone or a call is in use, a takeover or audio connection is pending, audio is recovering, a Game Mode change is in flight, or another app holds the settings lease"
}

// intentChanged reads the routing state once more so the message says where it stands now.
// Again is for the one fresh request after a stale one, which was sent a second time.
func (b routing) intentChanged(again bool) error {
	where := ""
	if now, err := b.client.Routing(); err == nil {
		where = " (now " + describe(now) + ")"
	}
	if again {
		return &explained{"routing intent changed again during one fresh request; read status again" + where, ErrIntentChanged}
	}
	if where == "" {
		return ErrIntentChanged
	}
	return fmt.Errorf("%w%s", ErrIntentChanged, where)
}

func describe(r dongle.Routing) string {
	switch {
	case r.Has(dongle.RoutingReleased):
		return "released"
	case r.Has(dongle.RoutingReleasePending):
		return "release pending"
	case r.Has(dongle.RoutingTakeoverPending):
		return "takeover pending"
	case r.Has(dongle.RoutingOwned):
		return "owned by the dongle"
	}
	return "not owned by the dongle"
}

// receipt looks for this request's receipt in fresh routing reads after an uncertain reply.
func (b routing) receipt(action byte, before dongle.Routing, nonce uint32, cause error) (dongle.Routing, error) {
	clock := b.client.Clock()
	deadline := clock.Now().Add(receiptWait)
	for clock.Now().Before(deadline) && b.client.Context().Err() == nil {
		if now, err := b.client.Routing(); err == nil {
			if receiptOf(now, before, action, nonce) {
				return now, nil
			}
			break
		}
		clock.Sleep(routingPoll)
	}
	name := [...]string{"release", "takeover"}[action]
	return dongle.Routing{}, &dongle.Unconfirmed{
		Message: fmt.Sprintf("%s: not confirmed: %v. It may have applied; read status before trying again.", name, cause),
		Err:     cause,
	}
}

// wait polls the routing state until the request completes, the intent moves on or the
// wait ends. Failed reads are read again: reading changes nothing. A client context that
// ends stops the wait early, with the pending outcome: the request was accepted either way.
//
// Done is the firmware tool's end state (acquired or released). The tool waits 12 s and then
// calls the outcome unconfirmed; this waits 5 s and calls it pending, since the dongle
// accepted the request and goes on carrying it out. A later read that no longer carries the
// same revision, peer, action and nonce means another choice replaced this one.
func (b routing) wait(action byte, last dongle.Routing) (Outcome, error) {
	// Going holds the flags of a request still under way.
	done, outcome, pending, going := released, Released, ReleasePending, dongle.RoutingReleased|dongle.RoutingReleasePending
	if action == dongle.HandoverAcquire {
		done, outcome, pending, going = acquired, Acquired, AcquirePending, dongle.RoutingTakeoverPending|dongle.RoutingOwned
	}
	start := last
	clock := b.client.Clock()
	deadline := clock.Now().Add(routingWait)
	for {
		switch {
		case !sameIntent(last, start):
			return 0, fmt.Errorf("%w (now %s)", ErrIntentChanged, describe(last))
		case done(last):
			return outcome, nil
		case last.Flags&going == 0 && action == dongle.HandoverAcquire:
			return 0, errors.New("the takeover ended without the AirPods confirming the dongle; they may be in their case, out of range or in use elsewhere")
		case last.Flags&going == 0:
			return 0, fmt.Errorf("%w (now %s)", ErrIntentChanged, describe(last))
		}
		if !clock.Now().Before(deadline) || b.client.Context().Err() != nil {
			return pending, nil
		}
		clock.Sleep(routingPoll)
		if now, err := b.client.Routing(); err == nil {
			last = now
		}
	}
}
