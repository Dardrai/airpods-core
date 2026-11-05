package hearing

import (
	"errors"
	"fmt"
	"time"

	"github.com/Dardrai/airpods-core/dongle"
)

// Refused means a precondition failed and nothing was sent that could change the AirPods.
// Reason is written to be shown to the user. errors.Is finds the condition behind it, when it
// is one of the dongle package's, such as dongle.ErrMicrophoneActive.
type Refused struct {
	Reason string
	cause  error
}

func (r *Refused) Error() string { return r.Reason }
func (r *Refused) Unwrap() error { return r.cause }

// ErrDeadline means one request used up its time limit. Nothing further was sent for it.
var ErrDeadline = errors.New("The dongle request ran out of time and was not retried.")

// connectionChanged is dongle.ErrGenerationChanged as this package words it.
var connectionChanged error = &reworded{"The dongle connection changed. Read the settings again.", dongle.ErrGenerationChanged}

// reworded shows its own text and matches the condition it wraps.
type reworded struct {
	text string
	err  error
}

func (e *reworded) Error() string { return e.text }
func (e *reworded) Unwrap() error { return e.err }

func refuse(format string, args ...any) error {
	return &Refused{Reason: fmt.Sprintf(format, args...)}
}

// noted adds an outcome to an error while keeping its type visible to errors.As.
type noted struct {
	err  error
	note string
}

func (n *noted) Error() string { return n.err.Error() + " " + n.note }
func (n *noted) Unwrap() error { return n.err }

// unconfirmed reports a write that went out with no proof of its effect.
func unconfirmed(message string, cause error) error {
	return &dongle.Unconfirmed{Message: message, Err: cause}
}

// unconfirmedWrite is unconfirmed for the first request of a write, which may also have failed
// before it went out. That failure stays as it is: the write cannot have applied.
func unconfirmedWrite(message string, cause error) error {
	if errors.Is(cause, dongle.ErrNotSent) {
		return cause
	}
	return unconfirmed(message, cause)
}

const operationLimit = 60 * time.Second

// operation is one user request: one session, one settings lease, one deadline. It
// checks the dongle's state around every raw request, so a recording that starts or a
// reconnect that happens halfway ends the work rather than mixing two connections.
type operation struct {
	client   *dongle.Client
	session  *dongle.Session
	start    dongle.Status
	clock    dongle.Clock
	deadline time.Time
}

func requestGate(status dongle.Status) error {
	switch {
	case !status.Connected || !status.ControlsReady:
		return &Refused{"Connect the dongle's AirPods before reading or changing hearing data.", dongle.ErrNotReady}
	case !status.Has(dongle.CapRPC):
		return refuse("Update the dongle firmware before reading or changing hearing data.")
	case status.MicrophoneActive:
		return &Refused{"Stop recording and wait for the microphone to recover before reading or changing hearing data.", dongle.ErrMicrophoneActive}
	}
	return nil
}

func openOperation(c *dongle.Client) (*operation, error) {
	status, err := c.Status()
	if err != nil {
		return nil, err
	}
	if err := requestGate(status); err != nil {
		return nil, err
	}
	session, err := c.OpenSession()
	if err != nil {
		return nil, err
	}
	// Kept reports must belong to the same lease as the writes and cleanup that follow.
	if err := session.AcquireLease(); err != nil {
		session.Close()
		return nil, err
	}
	clock := c.Clock()
	return &operation{client: c, session: session, start: status, clock: clock, deadline: clock.Now().Add(operationLimit)}, nil
}

// reopenOperation takes a new lease for a later step of one request. The step still belongs
// to the connection the request began on and to its deadline, so a reconnect between two steps
// ends the work just as one inside a lease does.
func reopenOperation(c *dongle.Client, start dongle.Status, deadline time.Time) (*operation, error) {
	o, err := openOperation(c)
	if err != nil {
		return nil, err
	}
	o.start, o.deadline = start, deadline
	if err := o.check(); err != nil {
		o.close()
		return nil, err
	}
	return o, nil
}

func (o *operation) close() { o.session.Close() }

func (o *operation) check() error {
	if !o.clock.Now().Before(o.deadline) {
		return ErrDeadline
	}
	status, err := o.client.Status()
	if err != nil {
		return err
	}
	if status.Generation != o.start.Generation || status.Peer != o.start.Peer {
		return connectionChanged
	}
	return requestGate(status)
}

// Exchange implements Link.
func (o *operation) Exchange(pdu []byte) ([]byte, error) {
	if err := o.check(); err != nil {
		return nil, dongle.NotSent(err)
	}
	response, err := o.session.Exchange(pdu)
	if err != nil {
		return nil, err
	}
	if err := o.check(); err != nil {
		return nil, err
	}
	return response, nil
}

// SendAAP implements Link. A nil result reports delivery to the dongle, not an effect.
func (o *operation) SendAAP(pdu []byte) error {
	if err := o.check(); err != nil {
		return dongle.NotSent(err)
	}
	if err := o.session.SendAAP(pdu); err != nil {
		return err
	}
	return o.check()
}

// Cached implements Link.
func (o *operation) Cached(slot byte) (*dongle.Cached, error) { return o.session.Cached(slot) }

// identity reads the reported model and firmware; empty strings mean none was reported.
func identity(link Link) (dongle.Metadata, error) {
	report, err := link.Cached(dongle.SlotMetadata)
	if err != nil || report == nil {
		return dongle.Metadata{}, err
	}
	meta, _ := dongle.ParseMetadata(report.Packet)
	return meta, nil
}
