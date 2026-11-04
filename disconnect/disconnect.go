// Package disconnect ends the AirPods connection while keeping them paired, on the
// dongles that can. Opcode 2 (pair) is never an option: it erases the saved pairing.
package disconnect

import (
	"errors"

	"github.com/Dardrai/airpods-core/dongle"
)

// ErrUnsupported is the result on a dongle without a disconnect command.
var ErrUnsupported = errors.New("this dongle firmware has no command to disconnect AirPods while keeping them paired; put the AirPods in their case to disconnect")

// KindUnsupported is the Kind of the backend for dongles without a disconnect command.
const KindUnsupported = "unsupported"

// Disconnector ends the AirPods connection.
type Disconnector interface {
	Disconnect() error
	Kind() string
}

type unsupported struct{}

func (unsupported) Disconnect() error { return ErrUnsupported }
func (unsupported) Kind() string      { return KindUnsupported }

// Pick returns the backend for a dongle with this status. The client is what a working
// backend sends through; it may be nil when only Kind is wanted.
func Pick(client *dongle.Client, status dongle.Status) Disconnector {
	if status.Has(CapDisconnect) {
		return hidop{client}
	}
	return unsupported{}
}
