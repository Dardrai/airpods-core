package dongle

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/Dardrai/airpods-core/hid"
)

// Control opcodes of the published protocol.
const (
	OpReconnect     byte = 1
	OpPair          byte = 2
	OpListeningMode byte = 3
	OpPairPhone     byte = 4
)

// Listening modes accepted by opcode 3.
const (
	ListeningANC          byte = 2
	ListeningTransparency byte = 3
)

const (
	controlWait = 2 * time.Second
	controlPoll = 50 * time.Millisecond
)

func opcodeName(opcode byte) string {
	switch opcode {
	case OpReconnect:
		return "reconnect"
	case OpPair:
		return "pair"
	case OpListeningMode:
		return "listening mode"
	case OpPairPhone:
		return "pair-phone"
	}
	return fmt.Sprintf("opcode %d", opcode)
}

// validateCommand applies the local precondition checks to known opcodes.
// Opcodes above 4 pass through unchecked so a later backend can send a new one.
func validateCommand(status Status, opcode, arg byte) error {
	reject := func(result byte) error { return &ControlRejected{Opcode: opcode, Result: result} }
	var capability uint16
	switch opcode {
	case 0:
		return reject(ResultUnsupported)
	case OpReconnect, OpPair:
		capability = CapControl
	case OpListeningMode:
		capability = CapListening
	case OpPairPhone:
		capability = CapPairPhone
	default:
		return nil
	}
	if opcode == OpListeningMode {
		if arg != ListeningANC && arg != ListeningTransparency {
			return reject(ResultUnsupported)
		}
	} else if arg != 0 {
		return reject(ResultUnsupported)
	}
	if status.Capabilities&capability == 0 {
		return reject(ResultUnsupported)
	}
	if opcode == OpListeningMode && !status.ControlsReady {
		return reject(ResultDisconnected)
	}
	if opcode == OpPair && (status.USBPlayback || status.MicrophoneActive || status.PhoneConnected) {
		return reject(ResultBusy)
	}
	return nil
}

// Command sends one control report and waits up to 2 s for its receipt. A receipt
// is tied to the sequence, not the generation, because pairing changes generation.
// It returns ControlRejected when refused and Unconfirmed once the outcome is unknown. A
// report that a closed device refused matches ErrNotSent instead: it cannot have applied.
func (c *Client) Command(opcode, arg byte) error {
	if err := c.acquire(); err != nil {
		return err
	}
	defer c.release()
	raw, err := c.dev.Get(2)
	if err != nil {
		return err
	}
	status := DecodeStatus(raw)
	if err := validateCommand(status, opcode, arg); err != nil {
		return err
	}
	// Monotonic even if an earlier command never produced a receipt.
	sequence := status.ReceiptSequence
	if *c.controlSeq-sequence < 1<<31 {
		sequence = *c.controlSeq
	}
	sequence++
	if sequence == 0 {
		sequence = 1
	}
	*c.controlSeq = sequence

	var data [hid.ReportSize]byte
	header := hid.Header(2)
	copy(data[:], header[:])
	data[7] = opcode
	le := binary.LittleEndian
	le.PutUint32(data[8:], status.Generation)
	copy(data[12:18], status.Peer[:])
	le.PutUint32(data[18:], sequence)
	data[22] = arg

	name := opcodeName(opcode)
	unconfirmed := func(err error) error {
		return &Unconfirmed{
			Message: fmt.Sprintf("%s: not confirmed: %v. It may have applied; check status before retrying.", name, err),
			Err:     err,
		}
	}
	if err := c.dev.Set(data); err != nil {
		if errors.Is(err, hid.ErrClosed) {
			return NotSent(err) // A closed device sent nothing, so there is no outcome to doubt.
		}
		return unconfirmed(err)
	}
	deadline := c.clock.Now().Add(controlWait)
	for c.clock.Now().Before(deadline) {
		if err := c.ctx.Err(); err != nil {
			return unconfirmed(err)
		}
		raw, err := c.dev.Get(2)
		if err != nil {
			return unconfirmed(err)
		}
		report := DecodeStatus(raw)
		if report.ReceiptSequence == sequence && report.ReceiptOpcode == opcode && report.ReceiptResult != 0 {
			if report.ReceiptResult == ResultOK {
				return nil
			}
			return &ControlRejected{Opcode: opcode, Result: report.ReceiptResult}
		}
		_ = sleepContext(c.ctx, c.clock, controlPoll) // An ended context is seen at the top.
	}
	return &Unconfirmed{Message: name + ": sent, but no receipt from the dongle. It may have applied; check status before retrying."}
}

// Reconnect asks the dongle to reconnect to its AirPods (opcode 1).
func (c *Client) Reconnect() error { return c.Command(OpReconnect, 0) }

// Pair puts the dongle in AirPods pairing (opcode 2); refused while audio is in use.
func (c *Client) Pair() error { return c.Command(OpPair, 0) }

// SetListeningMode switches listening mode through opcode 3 (ListeningANC or
// ListeningTransparency); setting 0x0d covers all modes with device confirmation.
func (c *Client) SetListeningMode(mode byte) error { return c.Command(OpListeningMode, mode) }

// PairPhone opens the phone pairing window (opcode 4).
func (c *Client) PairPhone() error { return c.Command(OpPairPhone, 0) }
