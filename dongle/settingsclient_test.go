package dongle

import (
	"errors"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
)

func settingsFor(t *testing.T, cfg sim.Config) (*SettingsClient, *sim.Device) {
	t.Helper()
	client, device := newClient(cfg)
	settings, err := client.SettingsClient()
	if err != nil {
		t.Fatal(err)
	}
	return settings, device
}

// TestSetClassification pins which failures stay plain errors and which become Unconfirmed.
func TestSetClassification(t *testing.T) {
	const (
		plain = iota
		unconfirmed
		unsupported
		reject
	)
	for _, tc := range []struct {
		name   string
		mutate func(*sim.Config)
		class  int
		text   string
	}{
		{"before read fails", func(c *sim.Config) { c.Fail = map[byte]byte{2: 5} }, reject, "RPC failure: device timeout"},
		{"before read busy", func(c *sim.Config) { c.InitialPhase = 2 }, reject, "RPC failure: busy; close competing companion controls"},
		{"before read timeout", func(c *sim.Config) { c.StallStage = true }, plain, "Pico diagnostic read timed out"},
		{"write unsupported", func(c *sim.Config) { c.Fail = map[byte]byte{1: 7} }, unsupported, "Firmware does not support this request for this device"},
		{"write malformed", func(c *sim.Config) { c.Fail = map[byte]byte{1: 1} }, reject, "RPC failure: malformed request"},
		{"write busy", func(c *sim.Config) { c.Fail = map[byte]byte{1: 2} }, reject, "RPC failure: busy; close competing companion controls"},
		{"write disconnected", func(c *sim.Config) { c.Fail = map[byte]byte{1: 4} }, reject, "RPC failure: AirPods disconnected"},
		{"write stale", func(c *sim.Config) { c.Fail = map[byte]byte{1: 3} }, unconfirmed, "mode: change not confirmed: RPC failure: stale session. Read it again before retrying."},
		{"write unmapped", func(c *sim.Config) { c.Fail = map[byte]byte{1: 200} }, unconfirmed, "mode: change not confirmed: RPC failure: 200. Read it again before retrying."},
		{"write reply", func(c *sim.Config) { c.WriteReply = sim.Hex{1} }, unconfirmed, "mode: change not confirmed: Unexpected setting-write response. It may have applied; read it again before retrying."},
		{"write transport", func(c *sim.Config) { c.SetFailOnWrite = true; c.FailMessage = "boom" }, unconfirmed, "mode: change not confirmed: boom. It may have applied; read it again before retrying."},
		{"session change", func(c *sim.Config) { c.GenerationChangeAfterWrite = true }, unconfirmed, "mode: change not confirmed: Pico session changed during request. It may have applied; read it again before retrying."},
		{"confirm rejected", func(c *sim.Config) { c.SnapshotFailAfterWrite = 4 }, unconfirmed, "mode: change not confirmed: RPC failure: AirPods disconnected. It may have applied; read it again before retrying."},
		{"confirm unsupported", func(c *sim.Config) { c.SnapshotFailAfterWrite = 7 }, unconfirmed, "mode: change not confirmed: Firmware does not support this request for this device. It may have applied; read it again before retrying."},
		{"confirm garbage", func(c *sim.Config) { c.SnapshotGarbageAfterWrite = true }, unconfirmed, "mode: change not confirmed: Malformed settings snapshot. It may have applied; read it again before retrying."},
		{"never fresh", func(c *sim.Config) { c.WriteMode = sim.WriteLost }, unconfirmed, "mode: sent, but no fresh matching device report. It may have applied; use get mode before retrying."},
		{"same revision", func(c *sim.Config) { c.WriteMode = sim.WriteSameRevision }, unconfirmed, "mode: sent, but no fresh matching device report. It may have applied; use get mode before retrying."},
		{"other value", func(c *sim.Config) { c.WriteMode = sim.WriteOtherValue }, unconfirmed, "mode: sent, but no fresh matching device report. It may have applied; use get mode before retrying."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sim.Default()
			tc.mutate(&cfg)
			settings, _ := settingsFor(t, cfg)
			_, err := settings.Set("mode", 1)
			if err == nil || err.Error() != tc.text {
				t.Fatalf("err = %v, want %q", err, tc.text)
			}
			var u *Unconfirmed
			var s *Unsupported
			var r *RequestRejected
			isUnconfirmed := errors.As(err, &u)
			switch tc.class {
			case plain:
				if isUnconfirmed {
					t.Fatal("wrapped")
				}
			case unconfirmed:
				if !isUnconfirmed {
					t.Fatal("not Unconfirmed")
				}
			case unsupported:
				if isUnconfirmed || !errors.As(err, &s) {
					t.Fatalf("%T", err)
				}
			case reject:
				if isUnconfirmed || !errors.As(err, &r) {
					t.Fatalf("%T", err)
				}
			}
		})
	}
}

func TestSetConfirmsAfterDelay(t *testing.T) {
	cfg := sim.Default()
	cfg.ApplyAfterReads = 10
	settings, device := settingsFor(t, cfg)
	line, err := settings.Set("mode", 1)
	if err != nil || line != "mode: off (confirmed by AirPods)" {
		t.Fatalf("%q %v", line, err)
	}
	if device.Writes() != 1 {
		t.Fatalf("%d writes", device.Writes())
	}
}

func TestConfirmationWindowIsFiveSeconds(t *testing.T) {
	for reads, confirmed := range map[int]bool{49: true, 50: false} {
		cfg := sim.Default()
		cfg.ApplyAfterReads = reads
		settings, device := settingsFor(t, cfg)
		_, err := settings.Set("mode", 1)
		if (err == nil) != confirmed {
			t.Errorf("apply after %d reads: %v", reads, err)
		}
		if device.Writes() != 1 {
			t.Errorf("write retried: %d", device.Writes())
		}
	}
}

func TestSetNewRecordNeedsOnlyTheValue(t *testing.T) {
	settings, _ := settingsFor(t, sim.Default())
	line, err := settings.Set("microphone", 2)
	if err != nil || line != "microphone: left (confirmed by AirPods)" {
		t.Fatalf("%q %v", line, err)
	}
}

func TestAlreadyReportedSendsNoWrite(t *testing.T) {
	settings, device := settingsFor(t, sim.Default())
	line, err := settings.Set("mode", 2)
	if err != nil || line != "mode: anc (already reported, 1200 ms old; no write sent)" {
		t.Fatalf("%q %v", line, err)
	}
	if device.Writes() != 0 {
		t.Fatal("write sent")
	}
}

func TestPreconditions(t *testing.T) {
	for flags, want := range map[byte]string{
		1:    "AirPods settings connection is not ready",
		0x13: "Stop microphone use before changing AirPods settings",
	} {
		cfg := sim.Default()
		cfg.Flags = flags
		settings, device := settingsFor(t, cfg)
		if _, err := settings.Set("mode", 1); err == nil || err.Error() != want {
			t.Errorf("flags %#x: %v", flags, err)
		}
		if ops, _ := device.TraceSummary(); ops != 1 {
			t.Errorf("flags %#x: %d operations", flags, ops)
		}
	}
	cfg := sim.Default()
	cfg.Capabilities = 0
	settings, _ := settingsFor(t, cfg)
	if _, err := settings.Read(); err == nil || err.Error() != "Firmware does not advertise typed settings support" {
		t.Errorf("read: %v", err)
	}
}

func TestSetExtraSetting(t *testing.T) {
	settings, device := settingsFor(t, sim.Default())
	line, err := settings.Set("connect-automatically", 1)
	if err != nil || line != "connect-automatically: on (confirmed by AirPods)" {
		t.Fatalf("%q %v", line, err)
	}
	if device.Writes() != 1 {
		t.Fatal("no write")
	}
}

func TestGenerationPinnedAcrossReads(t *testing.T) {
	settings, device := settingsFor(t, sim.Default())
	if _, err := settings.Read(); err != nil {
		t.Fatal(err)
	}
	device.BumpGeneration()
	if _, err := settings.Read(); err == nil || err.Error() != "Pico session changed; run the command again" {
		t.Fatalf("err = %v", err)
	}
}

func TestSendWritesOnceWithoutWaitingForAReport(t *testing.T) {
	cfg := sim.Default()
	cfg.WriteMode = sim.WriteLost
	settings, dev := settingsFor(t, cfg)
	got, err := settings.Send("conversation-awareness", 1)
	if err != nil || got != "conversation-awareness: on (sent)" {
		t.Fatalf("%q %v", got, err)
	}
	if dev.Writes() != 1 {
		t.Errorf("writes %d", dev.Writes())
	}
}

func TestSendChecksTheSameGatesAsSet(t *testing.T) {
	cfg := sim.Default()
	cfg.Flags = 0x13
	settings, dev := settingsFor(t, cfg)
	if _, err := settings.Send("mode", 3); err == nil {
		t.Error("microphone use must refuse")
	}
	if dev.Writes() != 0 {
		t.Error("wrote anyway")
	}
}
