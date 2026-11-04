package disconnect

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Dardrai/airpods-core/dongle"
)

// Provisional until the firmware defines them: the opcode of a disconnect command and
// the capability bit that advertises it. Nothing is sent unless the bit is set.
const (
	OpDisconnect  byte   = 5
	CapDisconnect uint16 = 0x80
)

// KindHID is the Kind of the backend that uses the dongle's disconnect command.
const KindHID = "hid"

type hidop struct{ client *dongle.Client }

func (h hidop) Disconnect() error {
	if h.client == nil {
		return errors.New("no dongle")
	}
	err := h.client.Command(OpDisconnect, 0)
	// Command names only the opcodes it knows, so name this one for the user.
	var unconfirmed *dongle.Unconfirmed
	if errors.As(err, &unconfirmed) {
		generic := fmt.Sprintf("opcode %d", OpDisconnect)
		return &dongle.Unconfirmed{
			Message: "disconnect" + strings.TrimPrefix(unconfirmed.Message, generic),
			Err:     unconfirmed.Err,
		}
	}
	return err
}

func (hidop) Kind() string { return KindHID }
