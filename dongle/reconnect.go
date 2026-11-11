package dongle

import (
	"errors"
	"fmt"
)

// kindReconnectPolicy is the typed request kind of the saved reconnect policy (firmware 0.1.63).
const kindReconnectPolicy = 23

// ReconnectPolicy is the reconnect policy the dongle keeps across restarts.
type ReconnectPolicy byte

// Reconnect policies. Under Manual, the default, the dongle connects out to the AirPods only
// after a button press, a reconnect (opcode 1) or a routing acquire. Under Automatic it also
// retries the saved pair by itself while they are not connected and not released, which can
// take them from another device using them.
const (
	ReconnectManual    ReconnectPolicy = 0
	ReconnectAutomatic ReconnectPolicy = 1
)

func (p ReconnectPolicy) String() string {
	switch p {
	case ReconnectManual:
		return "manual"
	case ReconnectAutomatic:
		return "auto"
	}
	return fmt.Sprintf("unknown (%d)", byte(p))
}

const policyUnavailableText = "Reconnect policy unavailable: the dongle firmware is older than 0.1.63, or its saved configuration is unreadable"

const policyBusyText = "The dongle saves the reconnect policy only while idle: pause playback, stop microphone use and calls, " +
	"wait for connecting or a Game Mode change to finish, and close settings sessions; any settings lease blocks it, this app's own included"

// DecodeReconnectPolicy validates a kind 23 reply.
func DecodeReconnectPolicy(reply []byte) (ReconnectPolicy, error) {
	if len(reply) != 2 || reply[0] != 1 || reply[1] > 1 {
		return 0, errors.New("Unsupported dongle reconnect policy response.")
	}
	return ReconnectPolicy(reply[1]), nil
}

// ReconnectPolicy reads the saved reconnect policy (kind 23). It is read-only and allowed during
// playback and while any settings lease is held. Unsupported means firmware older than 0.1.63,
// or a saved policy the dongle cannot read, in which case it acts as Manual.
func (c *Client) ReconnectPolicy() (ReconnectPolicy, error) {
	reply, err := c.TypedRequest([]byte{kindReconnectPolicy}, nil)
	var unsupported *Unsupported
	if errors.As(err, &unsupported) {
		return 0, &Unsupported{Message: policyUnavailableText}
	}
	if err != nil {
		return 0, err
	}
	return DecodeReconnectPolicy(reply)
}

// SetReconnectPolicy saves a reconnect policy once and checks the value the dongle echoes. It
// reads the policy first; when that policy is already saved it sends nothing and returns
// changed false. Errors are Unsupported and RequestRejected (code 2 busy: the dongle saves only
// while idle and while no settings lease is held, the caller's own included) when nothing was
// saved, and Unconfirmed once the save may have happened: never retry that automatically.
func (c *Client) SetReconnectPolicy(policy ReconnectPolicy) (changed bool, err error) {
	if policy > ReconnectAutomatic {
		return false, NotSent(errors.New("Invalid reconnect policy."))
	}
	current, err := c.ReconnectPolicy()
	var unsupported *Unsupported
	switch {
	case err == nil && current == policy:
		return false, nil
	case err != nil && !errors.As(err, &unsupported):
		return false, err
	}
	// A dongle whose last save could not be verified reads as unsupported until a later save
	// verifies, so an unsupported read does not stop this one; older firmware refuses it too.
	reply, err := c.TypedRequest([]byte{kindReconnectPolicy, byte(policy)}, nil)
	if err != nil {
		var rejected *RequestRejected
		switch {
		case errors.Is(err, ErrNotSent):
			return false, err
		case errors.As(err, &unsupported):
			// The dongle refuses before writing when its saved configuration is unreadable.
			return false, &Unsupported{Message: policyUnavailableText + "; nothing was saved"}
		case errors.As(err, &rejected) && rejected.Code == 2:
			return false, &rewordedError{policyBusyText, err}
		case errors.As(err, &rejected) && (rejected.Code == 1 || rejected.Code == 4):
			return false, err
		case errors.As(err, &rejected) && rejected.Code == 6:
			return false, &Unconfirmed{
				Message: "reconnect policy: the dongle could not verify the save, so it may have been saved. Do not retry it automatically; " +
					"the policy reads as unavailable until the dongle restarts or a later save verifies.",
				Err: err,
			}
		}
		return false, policyUnconfirmed(err)
	}
	saved, err := DecodeReconnectPolicy(reply)
	if err == nil && saved != policy {
		err = fmt.Errorf("the dongle reports %s", saved)
	}
	if err != nil {
		return false, policyUnconfirmed(err)
	}
	return true, nil
}

func policyUnconfirmed(err error) error {
	return &Unconfirmed{
		Message: fmt.Sprintf("reconnect policy: not confirmed: %v. It may have been saved; read it again before retrying.", err),
		Err:     err,
	}
}
