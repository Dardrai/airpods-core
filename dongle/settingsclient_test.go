package dongle

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
	"github.com/Dardrai/airpods-core/hid"
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
		{"confirm unsupported", func(c *sim.Config) { c.SnapshotFailAfterWrite = 7 }, unconfirmed, "mode: change not confirmed: AirPods controls are not ready yet; read the settings again in a moment. It may have applied; read it again before retrying."},
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
		// The client's own status read and the fresh one, nothing more.
		if ops, _ := device.TraceSummary(); ops != 2 {
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

// TestWriteFailuresBeforeAnythingWasSentAreNotUnconfirmed counts on Send's order: a status read,
// then the request's own G2 and G3, then the G2 just before its first Set.
func TestWriteFailuresBeforeAnythingWasSentAreNotUnconfirmed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*sim.Config)
		check  func(*testing.T, error)
		text   string
	}{
		{
			"status read fails before the write",
			func(c *sim.Config) { c.GetFailAt = 3; c.FailMessage = "boom" },
			nil, "boom",
		},
		{
			"report 3 read fails before the write",
			func(c *sim.Config) { c.GetFailAt = 4; c.FailMessage = "boom" },
			nil, "boom",
		},
		{
			"generation already moved",
			func(c *sim.Config) { c.GenerationChangeAtGet = 3 },
			func(t *testing.T, err error) {
				if !errors.Is(err, ErrGenerationChanged) {
					t.Error("not ErrGenerationChanged")
				}
			}, "Pico session changed; run the command again",
		},
		{
			"generation moved just before the first Set",
			func(c *sim.Config) { c.GenerationChangeAtGet = 5 },
			func(t *testing.T, err error) {
				if !errors.Is(err, ErrGenerationChanged) {
					t.Error("not ErrGenerationChanged")
				}
			}, "Pico session changed during request",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sim.Default()
			tc.mutate(&cfg)
			client, device := newClient(cfg)
			settings, err := client.SettingsClient()
			if err != nil {
				t.Fatal(err)
			}
			_, err = settings.Send("mode", 1)
			if err == nil || err.Error() != tc.text {
				t.Fatalf("err = %v, want %q", err, tc.text)
			}
			var unconfirmed *Unconfirmed
			if errors.As(err, &unconfirmed) {
				t.Errorf("wrapped as Unconfirmed: %v", err)
			}
			if !errors.Is(err, ErrNotSent) {
				t.Errorf("not ErrNotSent: %v", err)
			}
			if device.Writes() != 0 {
				t.Error("wrote anyway")
			}
			if tc.check != nil {
				tc.check(t, err)
			}
		})
	}
}

func TestAWriteThatWasAttemptedStaysUnconfirmed(t *testing.T) {
	cfg := sim.Default()
	cfg.SetFailOnWrite, cfg.FailMessage = true, "boom"
	settings, _ := settingsFor(t, cfg)
	_, err := settings.Set("mode", 1)
	var unconfirmed *Unconfirmed
	if !errors.As(err, &unconfirmed) {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrNotSent) {
		t.Error("the Set was tried, so it may have gone out")
	}
}

func TestPreconditionSentinels(t *testing.T) {
	for flags, want := range map[byte]error{1: ErrNotReady, 0x13: ErrMicrophoneActive} {
		cfg := sim.Default()
		cfg.Flags = flags
		settings, _ := settingsFor(t, cfg)
		if _, err := settings.Set("mode", 1); !errors.Is(err, want) {
			t.Errorf("Set flags %#x: %v", flags, err)
		}
		if _, err := settings.Send("mode", 1); !errors.Is(err, want) {
			t.Errorf("Send flags %#x: %v", flags, err)
		}
	}
	if ErrGenerationChanged.Error() != "Pico session changed; run the command again" ||
		ErrNotReady.Error() != "AirPods settings connection is not ready" ||
		ErrMicrophoneActive.Error() != "Stop microphone use before changing AirPods settings" {
		t.Error("sentinel text changed")
	}
}

func TestPreconditionsAreReadAgainForEachWrite(t *testing.T) {
	writes := map[string]func(*SettingsClient) (string, error){
		"Set":  func(s *SettingsClient) (string, error) { return s.Set("mode", 1) },
		"Send": func(s *SettingsClient) (string, error) { return s.Send("mode", 1) },
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			// A microphone that starts after the client was created refuses the write.
			settings, device := settingsFor(t, sim.Default())
			device.SetFlags(0x13)
			if _, err := write(settings); !errors.Is(err, ErrMicrophoneActive) {
				t.Errorf("started microphone: %v", err)
			}
			device.SetFlags(0x01)
			if _, err := write(settings); !errors.Is(err, ErrNotReady) {
				t.Errorf("not ready any more: %v", err)
			}
			if device.Writes() != 0 {
				t.Error("wrote anyway")
			}
			// And one that stopped lets it through on the same client.
			device.SetFlags(0x03)
			if _, err := write(settings); err != nil {
				t.Errorf("back to ready: %v", err)
			}

			cfg := sim.Default()
			cfg.Flags = 0x01
			waiting, device := settingsFor(t, cfg)
			if _, err := write(waiting); !errors.Is(err, ErrNotReady) {
				t.Errorf("not ready yet: %v", err)
			}
			device.SetFlags(0x03)
			if _, err := write(waiting); err != nil {
				t.Errorf("a settings connection that came up: %v", err)
			}

			// The generation alone stays pinned.
			pinned, device := settingsFor(t, sim.Default())
			device.BumpGeneration()
			if _, err := write(pinned); !errors.Is(err, ErrGenerationChanged) {
				t.Errorf("new generation: %v", err)
			}
			if device.Writes() != 0 {
				t.Error("wrote across a generation")
			}
		})
	}
}

// reconnectingDevice starts a new generation on the nth status read after a setting write.
type reconnectingDevice struct {
	*sim.Device
	at, reads int
}

func (d *reconnectingDevice) Get(report byte) ([hid.ReportSize]byte, error) {
	if report == 2 && d.Writes() > 0 {
		if d.reads++; d.reads == d.at {
			d.BumpGeneration()
		}
	}
	return d.Device.Get(report)
}

// A confirming read that fails before it goes out says nothing about the write before it:
// wherever a reconnect lands after the write, the outcome is Unconfirmed and never ErrNotSent.
func TestAReconnectAfterASettingWriteIsNeverErrNotSent(t *testing.T) {
	unsentReads := 0
	for at := 1; at <= 12; at++ {
		cfg := sim.Default()
		cfg.WriteMode = sim.WriteLost
		device := &reconnectingDevice{Device: sim.New(cfg), at: at}
		client := NewClient(device)
		client.SetClock(sim.NewVirtualClock())
		settings, err := client.SettingsClient()
		if err != nil {
			t.Fatal(err)
		}
		_, err = settings.Set("mode", 1)
		var unconfirmed *Unconfirmed
		if !errors.As(err, &unconfirmed) || !errors.Is(err, ErrGenerationChanged) {
			t.Fatalf("reconnect at read %d: %v", at, err)
		}
		if errors.Is(err, ErrNotSent) {
			t.Errorf("reconnect at read %d: the write went out, yet the error matches ErrNotSent", at)
		}
		if errors.Is(unconfirmed.Err, ErrNotSent) {
			unsentReads++
		}
		if device.Writes() != 1 {
			t.Errorf("reconnect at read %d: writes %d", at, device.Writes())
		}
	}
	if unsentReads == 0 {
		t.Error("no reconnect landed before a confirming read went out")
	}
}

func TestUnconfirmedKeepsItsCauseButNeverMatchesErrNotSent(t *testing.T) {
	reworded := &rewordedError{"reworded", ErrGenerationChanged}
	for name, cause := range map[string]error{
		"context":    NotSent(context.Canceled),
		"generation": NotSent(reworded),
		"busy":       NotSent(&RequestRejected{2}),
		"transport":  NotSent(hid.ErrDisconnected),
		"wrapped":    &rewordedError{"outer", NotSent(context.Canceled)},
	} {
		err := error(&Unconfirmed{Message: "not confirmed", Err: cause})
		if errors.Is(err, ErrNotSent) {
			t.Errorf("%s: matches ErrNotSent", name)
		}
		if got := errors.Unwrap(err); got == nil || got.Error() != cause.Error() {
			t.Errorf("%s: cause %v", name, got)
		}
		var rejected *RequestRejected
		switch name {
		case "context", "wrapped":
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s: lost context.Canceled", name)
			}
		case "generation":
			if !errors.Is(err, ErrGenerationChanged) {
				t.Errorf("%s: lost ErrGenerationChanged", name)
			}
		case "busy":
			if !errors.As(err, &rejected) || rejected.Code != 2 {
				t.Errorf("%s: lost the rejection", name)
			}
		case "transport":
			if !errors.Is(err, hid.ErrDisconnected) {
				t.Errorf("%s: lost hid.ErrDisconnected", name)
			}
		}
	}
	plain := errors.New("boom")
	if got := errors.Unwrap(&Unconfirmed{Err: plain}); got != plain {
		t.Errorf("a cause that was sent should come back as it is: %v", got)
	}
	if got := errors.Unwrap(&Unconfirmed{}); got != nil {
		t.Errorf("no cause: %v", got)
	}
}

// The dongle refuses the snapshot with code 7 only while its AirPods controls are not
// ready, so that refusal is not reported as missing support.
func TestSnapshotRefusalMeansControlsNotReady(t *testing.T) {
	cfg := sim.Default()
	cfg.Flags = 0x01
	cfg.SettingsNeedReady = true
	settings, device := settingsFor(t, cfg)
	_, err := settings.Read()
	var unsupported *Unsupported
	if !errors.Is(err, ErrNotReady) || !errors.As(err, &unsupported) || err.Error() != "AirPods controls are not ready yet; read the settings again in a moment" {
		t.Fatalf("err = %v", err)
	}
	device.SetFlags(0x03)
	if values, err := settings.Read(); err != nil || len(values) != 4 {
		t.Fatalf("after ready: %v %v", values, err)
	}
	// Another request refused with code 7 still means unsupported.
	client, _ := newClient(sim.Default())
	if _, err := client.TypedRequest([]byte{99}, nil); errors.Is(err, ErrNotReady) || !errors.As(err, &unsupported) {
		t.Errorf("other kind: %v", err)
	}
}

func TestSettingsArriveOverSuccessiveReads(t *testing.T) {
	cfg := sim.Default()
	cfg.SettingsPerRead = 3
	settings, _ := settingsFor(t, sim.Demo())
	all, err := settings.Read()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings = sim.Demo().Settings
	settings, _ = settingsFor(t, cfg)
	var sizes []int
	for range 9 {
		values, err := settings.Read()
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(values))
	}
	want := []int{3, 6, 9, 12, 15, 18, 21, 22, 22}
	if len(all) != 22 || fmt.Sprint(sizes) != fmt.Sprint(want) {
		t.Errorf("sizes %v of %d", sizes, len(all))
	}
}

func TestAllowOffGateNeedsAllowOffInThisGeneration(t *testing.T) {
	cfg := sim.Default()
	cfg.AllowOffGate = true
	settings, device := settingsFor(t, cfg)
	var unsupported *Unsupported
	for _, write := range []struct {
		name  string
		value uint32
	}{{"mode", 1}, {"hold-modes", 3}} {
		if _, err := settings.Set(write.name, write.value); !errors.As(err, &unsupported) {
			t.Errorf("%s before allow-off: %v", write.name, err)
		}
	}
	if _, err := settings.Set("hold-modes", 6); err != nil {
		t.Errorf("hold-modes without off: %v", err)
	}
	if _, err := settings.Send("allow-off", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Set("mode", 1); err != nil {
		t.Errorf("mode off after allow-off: %v", err)
	}
	if _, err := settings.Set("mode", 2); err != nil {
		t.Fatal(err)
	}
	// A reconnect starts a new generation, and the dongle has forgotten Allow Off.
	device.BumpGeneration()
	client := NewClient(device)
	client.SetClock(sim.NewVirtualClock())
	settings, err := client.SettingsClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Set("mode", 1); !errors.As(err, &unsupported) {
		t.Errorf("mode off in a new generation: %v", err)
	}
	if _, err := settings.Send("allow-off", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Set("mode", 1); !errors.As(err, &unsupported) {
		t.Errorf("mode off after allow-off off: %v", err)
	}
}
