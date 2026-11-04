package disconnect

import (
	"errors"
	"strings"
	"testing"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/dongle/sim"
)

const stockCapabilities uint16 = 0x7f

func connect(t *testing.T, cfg sim.Config) (*sim.Device, *dongle.Client, dongle.Status) {
	t.Helper()
	device := sim.New(cfg)
	client := dongle.NewClient(device)
	client.SetClock(sim.NewVirtualClock())
	status, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	return device, client, status
}

func firmwareSupport() sim.Config {
	cfg := sim.Default()
	cfg.Capabilities = stockCapabilities | CapDisconnect
	cfg.ControlResults = map[byte]byte{OpDisconnect: dongle.ResultOK}
	return cfg
}

func TestStockFirmwareNeverGetsACommand(t *testing.T) {
	for _, capabilities := range []uint16{0, dongle.CapControl, stockCapabilities, 0xffff &^ CapDisconnect} {
		cfg := sim.Default()
		cfg.Capabilities = capabilities
		device, client, status := connect(t, cfg)
		backend := Pick(client, status)
		if kind := backend.Kind(); kind != "unsupported" {
			t.Errorf("capabilities %#x: Kind() = %q", capabilities, kind)
		}
		if err := backend.Disconnect(); !errors.Is(err, ErrUnsupported) {
			t.Errorf("capabilities %#x: Disconnect() = %v", capabilities, err)
		}
		if got := len(device.Controls()); got != 0 {
			t.Errorf("capabilities %#x: sent %d control reports", capabilities, got)
		}
		if ops, _ := device.TraceSummary(); ops != 1 {
			t.Errorf("capabilities %#x: %d HID operations, want the one status read", capabilities, ops)
		}
	}
}

func TestErrUnsupportedText(t *testing.T) {
	const want = "this dongle firmware has no command to disconnect AirPods while keeping them paired; put the AirPods in their case to disconnect"
	if got := ErrUnsupported.Error(); got != want {
		t.Errorf("text = %q", got)
	}
}

func TestHIDSendsOpcode5OnlyAndAcceptsReceipt(t *testing.T) {
	device, client, status := connect(t, firmwareSupport())
	backend := Pick(client, status)
	if kind := backend.Kind(); kind != "hid" {
		t.Fatalf("Kind() = %q", kind)
	}
	if err := backend.Disconnect(); err != nil {
		t.Fatal(err)
	}
	controls := device.Controls()
	if len(controls) != 1 {
		t.Fatalf("sent %d control reports", len(controls))
	}
	if opcode, arg := controls[0][7], controls[0][22]; opcode != 5 || arg != 0 {
		t.Errorf("opcode %d arg %d, want 5 and 0", opcode, arg)
	}
}

func TestHIDRejectedReceipt(t *testing.T) {
	cfg := firmwareSupport()
	cfg.ControlResults = map[byte]byte{OpDisconnect: dongle.ResultBusy}
	_, client, status := connect(t, cfg)
	var rejected *dongle.ControlRejected
	if err := Pick(client, status).Disconnect(); !errors.As(err, &rejected) || rejected.Opcode != OpDisconnect || rejected.Result != dongle.ResultBusy {
		t.Errorf("Disconnect() = %v", err)
	}
}

// A firmware that advertises the bit but does not know the opcode is refused, not sent opcode 2.
func TestHIDUnknownOpcodeIsRejected(t *testing.T) {
	cfg := firmwareSupport()
	cfg.ControlResults = nil
	device, client, status := connect(t, cfg)
	var rejected *dongle.ControlRejected
	if err := Pick(client, status).Disconnect(); !errors.As(err, &rejected) || rejected.Result != dongle.ResultUnsupported {
		t.Errorf("Disconnect() = %v", err)
	}
	if controls := device.Controls(); len(controls) != 1 || controls[0][7] != OpDisconnect {
		t.Errorf("controls = %d", len(controls))
	}
}

func TestHIDLostReceipt(t *testing.T) {
	cfg := firmwareSupport()
	cfg.ControlNoReceipt = true
	_, client, status := connect(t, cfg)
	err := Pick(client, status).Disconnect()
	var unconfirmed *dongle.Unconfirmed
	if !errors.As(err, &unconfirmed) {
		t.Fatalf("Disconnect() = %v", err)
	}
	if !strings.HasPrefix(err.Error(), "disconnect: sent, but no receipt") {
		t.Errorf("message = %q", err)
	}
}

func TestHIDWriteFailureKeepsCause(t *testing.T) {
	cfg := firmwareSupport()
	cfg.SetFailAt = 1
	cfg.FailMessage = "boom"
	_, client, status := connect(t, cfg)
	err := Pick(client, status).Disconnect()
	var unconfirmed *dongle.Unconfirmed
	if !errors.As(err, &unconfirmed) || unconfirmed.Err == nil || !strings.HasPrefix(err.Error(), "disconnect: not confirmed: boom") {
		t.Errorf("Disconnect() = %v", err)
	}
}
