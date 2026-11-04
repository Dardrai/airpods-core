package disconnect

import (
	"errors"
	"testing"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/dongle/sim"
)

func supported() sim.Config {
	cfg := sim.Demo()
	cfg.Capabilities |= CapDisconnect
	cfg.DisconnectOpcode = OpDisconnect
	return cfg
}

func clientFor(t *testing.T, dev *sim.Device) (*dongle.Client, dongle.Status) {
	t.Helper()
	client := dongle.NewClient(dev)
	client.SetClock(sim.NewVirtualClock())
	status, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	return client, status
}

func TestUnsupportedBackendText(t *testing.T) {
	const want = "this dongle firmware has no command to disconnect AirPods while keeping them paired; put the AirPods in their case to disconnect"
	d := Pick(nil, dongle.Status{Capabilities: 0x7f})
	if d.Kind() != "unsupported" {
		t.Errorf("kind %q", d.Kind())
	}
	err := d.Disconnect()
	if !errors.Is(err, ErrUnsupported) || err.Error() != want {
		t.Errorf("err %v", err)
	}
}

func TestStockDongleNeverGetsACommand(t *testing.T) {
	dev := sim.New(sim.Demo())
	client, status := clientFor(t, dev)
	if status.Has(CapDisconnect) {
		t.Fatal("the stock config must not advertise disconnect")
	}
	d := Pick(client, status)
	if d.Kind() != KindUnsupported {
		t.Fatalf("kind %q", d.Kind())
	}
	if err := d.Disconnect(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err %v", err)
	}
	if _, _, sets := dev.Counts(); sets != 0 || len(dev.Controls()) != 0 {
		t.Errorf("sets %d controls %d", sets, len(dev.Controls()))
	}
}

func TestPickFollowsTheCapabilityBit(t *testing.T) {
	if got := Pick(nil, dongle.Status{Capabilities: CapDisconnect}).Kind(); got != "hid" {
		t.Errorf("with the bit: %q", got)
	}
	if got := Pick(nil, dongle.Status{Capabilities: 0x7f}).Kind(); got != "unsupported" {
		t.Errorf("without the bit: %q", got)
	}
}

func TestNeverThePairOpcode(t *testing.T) {
	if OpDisconnect == dongle.OpPair {
		t.Fatal("the disconnect opcode must not be the pair opcode")
	}
}

func TestHIDReceiptOK(t *testing.T) {
	dev := sim.New(supported())
	client, status := clientFor(t, dev)
	d := Pick(client, status)
	if d.Kind() != KindHID {
		t.Fatalf("kind %q", d.Kind())
	}
	if err := d.Disconnect(); err != nil {
		t.Fatal(err)
	}
	controls := dev.Controls()
	if len(controls) != 1 || controls[0][7] != OpDisconnect || controls[0][22] != 0 {
		t.Fatalf("controls: %v", controls)
	}
	after, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	if after.Connected {
		t.Error("still connected after the receipt")
	}
}

func TestHIDRejected(t *testing.T) {
	cfg := supported()
	cfg.ControlResults = map[byte]byte{OpDisconnect: dongle.ResultBusy}
	dev := sim.New(cfg)
	client, status := clientFor(t, dev)
	var rejected *dongle.ControlRejected
	if err := Pick(client, status).Disconnect(); !errors.As(err, &rejected) || rejected.Result != dongle.ResultBusy {
		t.Fatalf("err %v", err)
	}
	if len(dev.Controls()) != 1 {
		t.Errorf("sent %d times", len(dev.Controls()))
	}
}

func TestHIDLostReceiptIsUnconfirmedAndNotResent(t *testing.T) {
	cfg := supported()
	cfg.ControlNoReceipt = true
	dev := sim.New(cfg)
	client, status := clientFor(t, dev)
	var unconfirmed *dongle.Unconfirmed
	if err := Pick(client, status).Disconnect(); !errors.As(err, &unconfirmed) {
		t.Fatalf("err %v", err)
	}
	if len(dev.Controls()) != 1 {
		t.Errorf("sent %d times", len(dev.Controls()))
	}
}

func TestHIDWithoutClientFailsCleanly(t *testing.T) {
	if err := Pick(nil, dongle.Status{Capabilities: CapDisconnect}).Disconnect(); err == nil {
		t.Error("no error")
	}
}
