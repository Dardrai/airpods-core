package dongle

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
)

func TestControlFrameAndReceipt(t *testing.T) {
	cfg := sim.Default()
	cfg.ReceiptSequence = 41
	client, device := newClient(cfg)
	if err := client.Reconnect(); err != nil {
		t.Fatal(err)
	}
	frame := device.Controls()[0]
	want := []byte{2, 'A', 'P', 'D', 'G', 1, 64, 1, 7, 0, 0, 0, 1, 2, 3, 4, 5, 6, 42, 0, 0, 0, 0}
	for i, b := range want {
		if frame[i] != b {
			t.Fatalf("byte %d = %d, want %d: % x", i, frame[i], b, frame[:23])
		}
	}
	if err := client.Reconnect(); err != nil {
		t.Fatal(err)
	}
	if seq := binary.LittleEndian.Uint32(device.Controls()[1][18:]); seq != 43 {
		t.Fatalf("second sequence = %d", seq)
	}
}

func TestControlSequenceSkipsZeroAndStaysMonotonic(t *testing.T) {
	cfg := sim.Default()
	cfg.ReceiptSequence = 0xffffffff
	client, device := newClient(cfg)
	if err := client.Reconnect(); err != nil {
		t.Fatal(err)
	}
	if seq := binary.LittleEndian.Uint32(device.Controls()[0][18:]); seq != 1 {
		t.Fatalf("wrapped sequence = %d", seq)
	}
	cfg = sim.Default()
	cfg.ControlNoReceipt = true
	client, device = newClient(cfg)
	_ = client.Reconnect()
	_ = client.Reconnect()
	first := binary.LittleEndian.Uint32(device.Controls()[0][18:])
	second := binary.LittleEndian.Uint32(device.Controls()[1][18:])
	if second != first+1 {
		t.Fatalf("sequence reused without a receipt: %d then %d", first, second)
	}
}

func TestPairReceiptSurvivesGenerationChange(t *testing.T) {
	client, _ := newClient(sim.Default())
	if err := client.Pair(); err != nil {
		t.Fatal(err)
	}
	status, _ := client.Status()
	if status.Generation != 8 {
		t.Fatalf("generation = %d", status.Generation)
	}
}

func TestControlDelayedReceipt(t *testing.T) {
	cfg := sim.Default()
	cfg.ControlDelay = 5
	client, _ := newClient(cfg)
	if err := client.PairPhone(); err != nil {
		t.Fatal(err)
	}
	status, _ := client.Status()
	if !status.PhonePairing {
		t.Fatal("phone pairing flag not set")
	}
}

func TestControlTimeoutIsUnconfirmed(t *testing.T) {
	cfg := sim.Default()
	cfg.ControlNoReceipt = true
	client, device := newClient(cfg)
	var unconfirmed *Unconfirmed
	if err := client.Reconnect(); !errors.As(err, &unconfirmed) {
		t.Fatalf("err = %v", err)
	}
	// 2 s at 50 ms: one read before the write, then 40 polls.
	if ops, _ := device.TraceSummary(); ops != 42 {
		t.Fatalf("%d operations", ops)
	}
}

func TestControlValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*sim.Config)
		opcode byte
		arg    byte
		result byte
	}{
		{"opcode zero", nil, 0, 0, ResultUnsupported},
		{"reconnect with argument", nil, OpReconnect, 1, ResultUnsupported},
		{"mode off not allowed", nil, OpListeningMode, 1, ResultUnsupported},
		{"mode adaptive not allowed", nil, OpListeningMode, 4, ResultUnsupported},
		{"mode not ready", func(c *sim.Config) { c.Flags = 1 }, OpListeningMode, ListeningANC, ResultDisconnected},
		{"control capability", func(c *sim.Config) { c.Capabilities = 0x7e }, OpPair, 0, ResultUnsupported},
		{"mode capability", func(c *sim.Config) { c.Capabilities = 0x7d }, OpListeningMode, ListeningANC, ResultUnsupported},
		{"phone capability", func(c *sim.Config) { c.Capabilities = 0x7b }, OpPairPhone, 0, ResultUnsupported},
		{"pair busy", func(c *sim.Config) { c.Flags = 0x43 }, OpPair, 0, ResultBusy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sim.Default()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			client, device := newClient(cfg)
			var rejected *ControlRejected
			if err := client.Command(tc.opcode, tc.arg); !errors.As(err, &rejected) || rejected.Result != tc.result {
				t.Fatalf("err = %v", err)
			}
			if len(device.Controls()) != 0 {
				t.Fatal("refused command reached the device")
			}
		})
	}
}

func TestControlReceiptResults(t *testing.T) {
	for result := ResultBusy; result <= ResultMalformed; result++ {
		cfg := sim.Default()
		cfg.ControlResults = map[byte]byte{OpReconnect: result}
		client, _ := newClient(cfg)
		var rejected *ControlRejected
		if err := client.Reconnect(); !errors.As(err, &rejected) || rejected.Result != result {
			t.Errorf("result %d: %v", result, err)
		}
	}
}

func TestGenericCommandReachesDeviceWithNewOpcode(t *testing.T) {
	cfg := sim.Default()
	cfg.Capabilities = 0
	client, device := newClient(cfg)
	var seen [2]byte
	device.OnControl = func(opcode, arg byte) byte {
		seen = [2]byte{opcode, arg}
		return ResultOK
	}
	if err := client.Command(5, 9); err != nil {
		t.Fatal(err)
	}
	if seen != [2]byte{5, 9} {
		t.Fatalf("seen %v", seen)
	}
	device.OnControl = nil
	var rejected *ControlRejected
	if err := client.Command(5, 0); !errors.As(err, &rejected) || rejected.Result != ResultUnsupported {
		t.Fatalf("unknown opcode: %v", err)
	}
}

func TestListeningModeApplies(t *testing.T) {
	client, _ := newClient(sim.Default())
	if err := client.SetListeningMode(ListeningTransparency); err != nil {
		t.Fatal(err)
	}
	status, _ := client.Status()
	if status.ANC != ListeningTransparency {
		t.Fatalf("anc = %d", status.ANC)
	}
}
