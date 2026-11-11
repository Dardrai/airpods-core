package hearing

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/dongle/sim"
)

// enabled has Hearing Aid already on, so a profile write needs no mode change.
func enabled() *sim.HearingAirPods {
	a := proThree()
	a.Slots[2] = sim.CachedReport{Revision: 1, Packet: hearingOn}
	a.Slots[3] = sim.CachedReport{Revision: 1, Packet: assistOn}
	return a
}

func readBaseline(t *testing.T, client *dongle.Client, modes *Modes) Settings {
	t.Helper()
	profile, err := modes.ReadProfile(client)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Editable {
		t.Fatal("not editable")
	}
	return profile.Settings
}

func TestApplyGoldenWriteAndReadback(t *testing.T) {
	a := enabled()
	client, dev := rig(t, a)
	modes := new(Modes)
	expected := readBaseline(t, client, modes)
	requested := expected
	requested.LeftEQ[2] = 35
	requested.RightAmplification = 0.5
	requested.LeftConversationBoost = true
	result, err := modes.ApplyProfile(client, expected, requested)
	if err != nil {
		t.Fatal(err)
	}
	if result.Settings != requested || !result.Written {
		t.Errorf("result %+v", result)
	}
	want := append([]byte{0x12, 0x1f, 0}, sim.HearingRecord()...)
	copy(want[3:], []byte{2, 2, 0x64, 0}) // A read reports 0x60; the writer's header says 0x64.
	putFloat(want[3:], offLeftEQ+8, 35)
	putFloat(want[3:], offRightAmp, 0.5)
	putFloat(want[3:], offLeftConv, 1)
	var writes [][]byte
	for _, pdu := range dev.Exchanges() {
		if pdu[0] == attWrite || pdu[0] == attCommand {
			writes = append(writes, pdu)
		}
	}
	samePackets(t, writes, [][]byte{want})
	if len(want) != 107 {
		t.Errorf("write is %d bytes", len(want))
	}
	if len(a.Received) != 0 {
		t.Errorf("no mode change was needed: %x", a.Received)
	}
}

func TestPatchKeepsUntouchedBytes(t *testing.T) {
	baseline := sim.HearingRecord()
	baseline[offLeftConv+3] = 0x80 // Negative zero reads as off.
	settings, err := parseSettings(baseline)
	if err != nil {
		t.Fatal(err)
	}
	patched := patchRecord(baseline, settings)
	if !bytes.Equal(patched[4:], baseline[4:]) {
		t.Errorf("an unchanged request altered bytes\n%x\n%x", patched, baseline)
	}
	if !bytes.Equal(patched[:4], []byte{2, 2, 0x64, 0}) {
		t.Errorf("header %x", patched[:4])
	}
}

// A fitting write needs hearing assistance set up, not Hearing Aid on, and never turns it on.
func TestApplyWritesWithHearingAidOffAndLeavesItOff(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	modes := new(Modes)
	expected := readBaseline(t, client, modes)
	if err := modes.PrepareProfile(client, expected); err != nil {
		t.Fatal(err)
	}
	if a.Server.Writes != 0 || len(a.Received) != 0 {
		t.Fatal("prepare changed something")
	}
	requested := expected
	requested.OwnVoice = 0.25
	result, err := modes.ApplyProfile(client, expected, requested)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Written || result.State.HearingEnabled != FlagOff || a.Server.Writes != 1 {
		t.Errorf("%+v, %d writes", result, a.Server.Writes)
	}
	if len(a.Received) != 0 {
		t.Errorf("a profile write sent mode packets: %x", a.Received)
	}
	if !strings.Contains(strings.Join(dev.LeaseEvents(), " "), "release") || dev.LeaseHeld() {
		t.Error("lease kept")
	}
}

func TestApplyOfTheStoredValuesWritesNothing(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
	modes := new(Modes)
	expected := readBaseline(t, client, modes)
	result, err := modes.ApplyProfile(client, expected, expected)
	if err != nil || result.Written || result.Settings != expected {
		t.Fatalf("%+v %v", result, err)
	}
	if a.Server.Writes != 0 || len(a.Received) != 0 {
		t.Errorf("%d writes, sent %x", a.Server.Writes, a.Received)
	}
}

func TestApplyRefusalsWriteNothing(t *testing.T) {
	good := func(t *testing.T) (*sim.HearingAirPods, *dongle.Client, *Modes, Settings) {
		t.Helper()
		a := enabled()
		client, _ := rig(t, a)
		modes := new(Modes)
		return a, client, modes, readBaseline(t, client, modes)
	}
	t.Run("no read first", func(t *testing.T) {
		a := enabled()
		client, _ := rig(t, a)
		settings, _ := parseSettings(sim.HearingRecord())
		_, err := new(Modes).ApplyProfile(client, settings, settings)
		refused(t, err, "Read it again")
		if a.Server.Writes != 0 {
			t.Error("wrote")
		}
	})
	t.Run("changed on the AirPods", func(t *testing.T) {
		a, client, modes, expected := good(t)
		a.Server.Find(sim.UUIDHearing).Value[6] = 0x30
		_, err := modes.ApplyProfile(client, expected, expected)
		refused(t, err, "changed on the AirPods")
		if a.Server.Writes != 0 {
			t.Error("wrote")
		}
	})
	t.Run("stale expected", func(t *testing.T) {
		a, client, modes, expected := good(t)
		other := expected
		other.OwnVoice = 0
		_, err := modes.ApplyProfile(client, other, expected)
		refused(t, err, "Read it again")
		if a.Server.Writes != 0 {
			t.Error("wrote")
		}
	})
	t.Run("out of range", func(t *testing.T) {
		for name, change := range map[string]func(*Settings){
			"loud":    func(s *Settings) { s.LeftEQ[0] = 90.5 },
			"NaN":     func(s *Settings) { s.RightEQ[3] = float32(math.NaN()) },
			"gain":    func(s *Settings) { s.LeftAmplification = 1.75 },
			"tone":    func(s *Settings) { s.RightTone = -2 },
			"Inf":     func(s *Settings) { s.OwnVoice = float32(math.Inf(1)) },
			"ambient": func(s *Settings) { s.LeftAmbientNoiseReduction = -0.5 },
		} {
			a, client, modes, expected := good(t)
			requested := expected
			change(&requested)
			_, err := modes.ApplyProfile(client, expected, requested)
			refused(t, err, "valid")
			if a.Server.Writes != 0 {
				t.Errorf("%s wrote", name)
			}
		}
	})
	t.Run("read only format", func(t *testing.T) {
		a := enabled()
		a.Server.Find(sim.UUIDHearing).Value[1] = 0
		client, _ := rig(t, a)
		modes := new(Modes)
		profile, err := modes.ReadProfile(client)
		if err != nil || profile.Editable {
			t.Fatalf("%+v %v", profile, err)
		}
		_, err = modes.ApplyProfile(client, profile.Settings, profile.Settings)
		refused(t, err, "cannot be edited")
	})
	for name, report := range map[string][]byte{
		"not set up":    {4, 0, 4, 0, 9, 0, 0x2c, 2, 0, 0, 0},
		"state unknown": {4, 0, 4, 0, 9, 0, 0x2c, 2, 1, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			a := proThree()
			client, _ := rig(t, a)
			modes := new(Modes)
			expected := readBaseline(t, client, modes)
			a.Report(2, report)
			if err := modes.PrepareProfile(client, expected); err == nil {
				t.Error("prepared")
			}
			requested := expected
			requested.OwnVoice = 0.25
			_, err := modes.ApplyProfile(client, expected, requested)
			refused(t, err, "iPhone or iPad")
			if len(a.Received) != 0 || a.Server.Writes != 0 {
				t.Errorf("sent %x, %d writes", a.Received, a.Server.Writes)
			}
		})
	}
	t.Run("a new connection needs a new read", func(t *testing.T) {
		a := enabled()
		client, dev := rig(t, a)
		modes := new(Modes)
		expected := readBaseline(t, client, modes)
		dev.BumpGeneration()
		_, err := modes.ApplyProfile(client, expected, expected)
		refused(t, err, "Read it again")
		if a.Server.Writes != 0 {
			t.Error("wrote")
		}
	})
}

func TestReadbackRules(t *testing.T) {
	unapplied := func(t *testing.T, a *sim.HearingAirPods, client *dongle.Client, modes *Modes, expected Settings) {
		t.Helper()
		requested := expected
		requested.OwnVoice = 0.25
		_, err := modes.ApplyProfile(client, expected, requested)
		var u *dongle.Unconfirmed
		if !errors.As(err, &u) {
			t.Fatalf("err %#v", err)
		}
		if a.Server.Writes != 1 {
			t.Errorf("writes %d", a.Server.Writes)
		}
		if _, err := modes.ApplyProfile(client, expected, requested); err == nil {
			t.Error("the baseline should be gone after an unconfirmed write")
		}
		if a.Server.Writes != 1 {
			t.Errorf("a later call wrote again: %d", a.Server.Writes)
		}
	}
	t.Run("delayed apply is accepted within the tries", func(t *testing.T) {
		a := enabled()
		attribute := a.Server.Find(sim.UUIDHearing)
		a.BeforeExchange = func(pdu []byte) {
			if pdu[0] == attWrite {
				attribute.AppliesAfterReads = 3
			}
		}
		client, _ := rig(t, a)
		modes := new(Modes)
		expected := readBaseline(t, client, modes)
		requested := expected
		requested.OwnVoice = 0.25
		if _, err := modes.ApplyProfile(client, expected, requested); err != nil {
			t.Fatal(err)
		}
		if a.Server.Writes != 1 {
			t.Errorf("writes %d", a.Server.Writes)
		}
	})
	t.Run("never applies", func(t *testing.T) {
		a := enabled()
		attribute := a.Server.Find(sim.UUIDHearing)
		a.BeforeExchange = func(pdu []byte) {
			if pdu[0] == attWrite {
				attribute.AppliesAfterReads = 6
			}
		}
		client, _ := rig(t, a)
		modes := new(Modes)
		unapplied(t, a, client, modes, readBaseline(t, client, modes))
	})
	t.Run("something else changed it", func(t *testing.T) {
		a := enabled()
		attribute := a.Server.Find(sim.UUIDHearing)
		attribute.OnWrite = func(value []byte) {
			other := bytes.Clone(value)
			putFloat(other, offLeftEQ, 80)
			attribute.Value = other
		}
		client, _ := rig(t, a)
		modes := new(Modes)
		unapplied(t, a, client, modes, readBaseline(t, client, modes))
	})
	t.Run("readback in another format", func(t *testing.T) {
		a := enabled()
		attribute := a.Server.Find(sim.UUIDHearing)
		attribute.OnWrite = func(value []byte) {
			other := bytes.Clone(value)
			other[1] = 0
			attribute.Value = other
		}
		client, _ := rig(t, a)
		modes := new(Modes)
		unapplied(t, a, client, modes, readBaseline(t, client, modes))
	})
	t.Run("write refused", func(t *testing.T) {
		a := enabled()
		client, _ := rig(t, a)
		modes := new(Modes)
		expected := readBaseline(t, client, modes)
		a.Server.RefuseWrites = true
		requested := expected
		requested.OwnVoice = 0.25
		_, err := modes.ApplyProfile(client, expected, requested)
		var u *dongle.Unconfirmed
		if !errors.As(err, &u) || !strings.Contains(u.Err.Error(), "error 0x03") {
			t.Fatalf("err %#v", err)
		}
	})
	t.Run("write command has no acknowledgement to check", func(t *testing.T) {
		a := enabled()
		a.Server.Find(sim.UUIDHearing).Props = 0x06
		client, dev := rig(t, a)
		modes := new(Modes)
		expected := readBaseline(t, client, modes)
		requested := expected
		requested.OwnVoice = 0.25
		if _, err := modes.ApplyProfile(client, expected, requested); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, pdu := range dev.Exchanges() {
			found = found || pdu[0] == attCommand
		}
		if !found {
			t.Error("no write command")
		}
	})
}

// A failed write leaves the modes alone: there is no enable to undo and nothing to recover.
func TestFailedWriteLeavesModesAlone(t *testing.T) {
	for name, a := range map[string]*sim.HearingAirPods{"off": proThree(), "on": enabled()} {
		t.Run(name, func(t *testing.T) {
			attribute := a.Server.Find(sim.UUIDHearing)
			attribute.OnWrite = func(value []byte) {
				other := bytes.Clone(value)
				putFloat(other, offLeftEQ, 80)
				attribute.Value = other
			}
			client, _ := rig(t, a)
			modes := new(Modes)
			expected := readBaseline(t, client, modes)
			requested := expected
			requested.OwnVoice = 0.25
			_, err := modes.ApplyProfile(client, expected, requested)
			var u *dongle.Unconfirmed
			if !errors.As(err, &u) {
				t.Fatalf("err %#v", err)
			}
			if len(a.Received) != 0 || a.Server.Writes != 1 || modes.Recovery() != nil {
				t.Errorf("sent %x, %d writes, recovery %+v", a.Received, a.Server.Writes, modes.Recovery())
			}
		})
	}
}

func TestEditMapping(t *testing.T) {
	base, _ := parseSettings(sim.HearingRecord())
	edit := NewProfileEdit(Profile{Settings: base, Editable: true})
	// left 0.25, right -0.25: mean 0, right minus left -0.5.
	if edit.Amplification != 0 || edit.Balance != -0.5 {
		t.Errorf("controls %v %v", edit.Amplification, edit.Balance)
	}
	if edit.Dirty || edit.Draft != base {
		t.Error("draft changed itself")
	}
	if !edit.EditAmplification(1) {
		t.Fatal("amplification refused")
	}
	if edit.Draft.LeftAmplification != 1.25 || edit.Draft.RightAmplification != 0.75 {
		t.Errorf("gains %v %v", edit.Draft.LeftAmplification, edit.Draft.RightAmplification)
	}
	if !edit.EditBalance(1) {
		t.Fatal("balance refused")
	}
	if edit.Draft.LeftAmplification != 0.5 || edit.Draft.RightAmplification != 1.5 {
		t.Errorf("gains %v %v", edit.Draft.LeftAmplification, edit.Draft.RightAmplification)
	}
	if !edit.EditAmplification(-0.5) || edit.Draft.LeftAmplification != -1 || edit.Draft.RightAmplification != 0 {
		t.Errorf("gains %v %v", edit.Draft.LeftAmplification, edit.Draft.RightAmplification)
	}
	if !edit.EditAmplification(0) || !edit.EditBalance(-3) || edit.Draft.LeftAmplification != 1.5 || edit.Draft.RightAmplification != -1.5 {
		t.Errorf("full balance: %v %v", edit.Draft.LeftAmplification, edit.Draft.RightAmplification)
	}
	for _, bad := range []float32{-1.6, 1.6, float32(math.NaN())} {
		if edit.EditAmplification(bad) {
			t.Errorf("amplification %v", bad)
		}
	}
	if edit.EditBalance(3.5) || edit.EditBalance(float32(math.Inf(1))) {
		t.Error("balance")
	}
	if edit.EditTone(1.5) || !edit.EditTone(-0.5) || edit.Draft.LeftTone != -0.5 || edit.Draft.RightTone != -0.5 {
		t.Error("tone")
	}
	if !edit.EditConversationBoost(true) || !edit.Draft.LeftConversationBoost || !edit.Draft.RightConversationBoost {
		t.Error("conversation boost")
	}
	if edit.Baseline() != base {
		t.Error("baseline moved")
	}
}

// An ear that would leave -1.5 to 1.5 rejects the edit; nothing is clamped, so the balance
// asked for is never silently changed.
func TestEditRejectsAnEarOutOfRange(t *testing.T) {
	base, _ := parseSettings(sim.HearingRecord())
	base.LeftAmplification, base.RightAmplification = 1.5, 1.5
	edit := NewProfileEdit(Profile{Settings: base, Editable: true})
	if edit.Amplification != 1.5 || edit.Balance != 0 {
		t.Errorf("controls %v %v", edit.Amplification, edit.Balance)
	}
	if edit.EditBalance(-1) {
		t.Error("an ear at 2 was accepted")
	}
	if edit.Draft != base || edit.Dirty || edit.Balance != 0 {
		t.Errorf("a rejected edit changed the draft: %+v", edit)
	}
	if !edit.EditAmplification(1) || !edit.EditBalance(-1) {
		t.Fatal("in range")
	}
	if edit.Draft.LeftAmplification != 1.5 || edit.Draft.RightAmplification != 0.5 {
		t.Errorf("gains %v %v", edit.Draft.LeftAmplification, edit.Draft.RightAmplification)
	}
	if edit.EditAmplification(1.25) || edit.Amplification != 1 {
		t.Error("left would be 1.75")
	}
}

func TestEditCurveValidation(t *testing.T) {
	base, _ := parseSettings(sim.HearingRecord())
	edit := NewProfileEdit(Profile{Settings: base, Editable: true})
	flat := func(v float64) []float64 {
		out := make([]float64, Bands)
		for i := range out {
			out[i] = v
		}
		return out
	}
	for name, curve := range map[string][]float64{"short": flat(1)[:7], "high": flat(90.5), "low": flat(-1), "NaN": flat(math.NaN()), "Inf": flat(math.Inf(1))} {
		if edit.EditCurve(curve, flat(1)) {
			t.Errorf("%s accepted", name)
		}
		if edit.CanApply() || !edit.InvalidCurve {
			t.Errorf("%s still applies", name)
		}
	}
	if edit.EditTone(0) {
		t.Error("edits are blocked while the curve is invalid")
	}
	if !edit.EditCurve(flat(0), flat(90)) || edit.InvalidCurve || !edit.CanApply() {
		t.Error("a good curve should clear the invalid state")
	}
	if edit.Draft.LeftEQ[7] != 0 || edit.Draft.RightEQ[0] != 90 {
		t.Errorf("curve %+v", edit.Draft)
	}
	locked := NewProfileEdit(Profile{Settings: base})
	if locked.EditCurve(flat(1), flat(1)) || locked.EditTone(0) || locked.CanApply() {
		t.Error("a read-only profile is not editable")
	}
}

func TestBandsAreTheAudiogramFrequencies(t *testing.T) {
	if BandHz[0] != 250 || BandHz[Bands-1] != 8000 {
		t.Errorf("%v", BandHz)
	}
}
