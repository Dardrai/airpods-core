package hearing

import (
	"errors"
	"strings"
	"testing"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/dongle/sim"
)

const leaseKind = 10

// freeBetweenSteps fails unless the owner could poll and serve other jobs right now.
func freeBetweenSteps(t *testing.T, client *dongle.Client, dev *sim.Device) {
	t.Helper()
	if dev.LeaseHeld() {
		t.Fatal("a lease is held between steps")
	}
	if _, err := client.Status(); err != nil {
		t.Fatal(err)
	}
}

func TestStartSetSendsOnceAndStepsSendNothing(t *testing.T) {
	a := proThree()
	a.Silent = true
	client, dev := rig(t, a)
	modes := new(Modes)
	change, err := modes.StartSet(client, HearingAid, true)
	if err != nil {
		t.Fatal(err)
	}
	sent := [][]byte{assistOn, hearingOn, ancClean, askReports}
	samePackets(t, a.Received, sent)
	for range 5 {
		freeBetweenSteps(t, client, dev)
		if _, done, err := change.Step(client); done || err != nil {
			t.Fatalf("done %v, err %v", done, err)
		}
	}
	samePackets(t, a.Received, sent)
	a.Silent = false
	a.Report(2, hearingOn)
	a.Report(3, assistOn)
	result, done, err := change.Step(client)
	if !done || err != nil || result.State.HearingEnabled != FlagOn || result.Profile == nil {
		t.Fatalf("result %+v, done %v, err %v", result, done, err)
	}
	samePackets(t, a.Received, sent)
	freeBetweenSteps(t, client, dev)
	again, done, err := change.Step(client)
	if again != result || !done || err != nil {
		t.Error("a finished change must keep its outcome")
	}
}

func TestOnlyOneChangeAtATime(t *testing.T) {
	a := proThree()
	a.Silent = true
	client, _ := rig(t, a)
	modes := new(Modes)
	change, err := modes.StartSet(client, HearingAid, true)
	if err != nil {
		t.Fatal(err)
	}
	sent := len(a.Received)
	_, err = modes.StartSet(client, HearingAid, false)
	refused(t, err, "still being confirmed")
	_, err = modes.StartApplyProfile(client, Settings{}, Settings{})
	refused(t, err, "still being confirmed")
	if _, err := modes.Read(client); err != nil {
		t.Errorf("reading stays allowed: %v", err)
	}
	if len(a.Received) != sent {
		t.Error("a refused change sent something")
	}
	if _, _, err := change.Absent(errors.New("gone")); err == nil {
		t.Fatal("no outcome")
	}
	if _, err := modes.StartSet(client, HearingAid, false); err != nil {
		t.Errorf("after the end: %v", err)
	}
}

func TestAnotherClientOnlyDelaysALook(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	modes := new(Modes)
	change, err := modes.StartSet(client, MediaAssist, true)
	if err != nil {
		t.Fatal(err)
	}
	dev.Update(func(cfg *sim.Config) { cfg.Fail = map[byte]byte{leaseKind: 2} })
	if _, done, err := change.Step(client); done || err != nil {
		t.Fatalf("done %v, err %v", done, err)
	}
	dev.Update(func(cfg *sim.Config) { cfg.Fail = nil })
	result, done, err := change.Step(client)
	if !done || err != nil || result.State.Media != FlagOn {
		t.Fatalf("result %+v, done %v, err %v", result, done, err)
	}
}

func TestAnotherClientCannotOutlastTheLimit(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	modes := new(Modes)
	change, err := modes.StartSet(client, HearingAid, true)
	if err != nil {
		t.Fatal(err)
	}
	dev.Update(func(cfg *sim.Config) { cfg.Fail = map[byte]byte{leaseKind: 2} })
	_, err = change.wait(client)
	var unconfirmed *ModeUnconfirmed
	if !errors.As(err, &unconfirmed) || unconfirmed.Recovery == nil {
		t.Fatalf("err %#v", err)
	}
}

func TestReconnectBetweenStepsEndsTheChange(t *testing.T) {
	a := proThree()
	a.Silent = true
	client, dev := rig(t, a)
	modes := new(Modes)
	change, err := modes.StartSet(client, HearingAid, true)
	if err != nil {
		t.Fatal(err)
	}
	sent := len(a.Received)
	dev.BumpGeneration()
	_, done, err := change.Step(client)
	var unconfirmed *ModeUnconfirmed
	if !done || !errors.As(err, &unconfirmed) || unconfirmed.Recovery == nil {
		t.Fatalf("done %v, err %#v", done, err)
	}
	if len(a.Received) != sent {
		t.Error("something was resent")
	}
}

func TestRecordingBetweenStepsEndsTheChange(t *testing.T) {
	a := proThree()
	a.Silent = true
	client, dev := rig(t, a)
	modes := new(Modes)
	change, err := modes.StartSet(client, HearingAid, true)
	if err != nil {
		t.Fatal(err)
	}
	dev.SetFlags(0x07 | 16) // connected, ready, streaming, microphone
	_, done, err := change.Step(client)
	var unconfirmed *ModeUnconfirmed
	if !done || !errors.As(err, &unconfirmed) {
		t.Fatalf("done %v, err %#v", done, err)
	}
}

func TestLostDeviceEndsTheChangeUnconfirmed(t *testing.T) {
	a := proThree()
	a.Silent = true
	client, _ := rig(t, a)
	modes := new(Modes)
	change, err := modes.StartSet(client, HearingAid, true)
	if err != nil {
		t.Fatal(err)
	}
	_, done, err := change.Absent(errors.New("no dongle"))
	var unconfirmed *ModeUnconfirmed
	if !done || !errors.As(err, &unconfirmed) || modes.Recovery() == nil {
		t.Fatalf("done %v, err %#v, recovery %+v", done, err, modes.Recovery())
	}
}

func TestApplyStepsWriteOnceAndHoldNothingBetween(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	modes := new(Modes)
	expected := readBaseline(t, client, modes)
	requested := expected
	requested.OwnVoice = 0.25
	apply, err := modes.StartApplyProfile(client, expected, requested)
	if err != nil {
		t.Fatal(err)
	}
	if a.Server.Writes != 0 {
		t.Error("written before the enable was confirmed")
	}
	var result *ProfileResult
	for done := false; !done; {
		freeBetweenSteps(t, client, dev)
		result, done, err = apply.Step(client)
		if err != nil {
			t.Fatal(err)
		}
	}
	if a.Server.Writes != 1 || !result.Enabled || result.Settings.OwnVoice != 0.25 {
		t.Errorf("writes %d, result %+v", a.Server.Writes, result)
	}
	freeBetweenSteps(t, client, dev)
	if _, err := modes.StartSet(client, HearingAid, false); err != nil {
		t.Errorf("the apply did not let go: %v", err)
	}
}

func TestApplyWaitsWithoutWritingWhileAnEnableGoesUnconfirmed(t *testing.T) {
	a := proThree()
	a.Silent = true
	client, _ := rig(t, a)
	modes := new(Modes)
	expected := readBaseline(t, client, modes)
	apply, err := modes.StartApplyProfile(client, expected, expected)
	if err != nil {
		t.Fatal(err)
	}
	for done := false; !done; {
		_, done, err = apply.Step(client)
		client.Clock().Sleep(confirmPoll)
	}
	var partial *ModeUnconfirmed
	if !errors.As(err, &partial) || !strings.Contains(err.Error(), "may still be active") {
		t.Fatalf("err %#v", err)
	}
	if a.Server.Writes != 0 || modes.Recovery() == nil {
		t.Errorf("writes %d, recovery %+v", a.Server.Writes, modes.Recovery())
	}
}

func TestApplyAbsentSaysWhatMayBeLeftOn(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
	modes := new(Modes)
	expected := readBaseline(t, client, modes)
	apply, err := modes.StartApplyProfile(client, expected, expected)
	if err != nil {
		t.Fatal(err)
	}
	_, done, err := apply.Absent(errors.New("no dongle"))
	if !done || err == nil || !strings.Contains(err.Error(), "may still be active") || modes.Recovery() == nil {
		t.Fatalf("done %v, err %v, recovery %+v", done, err, modes.Recovery())
	}
	if a.Server.Writes != 0 {
		t.Error("written")
	}
	// A pair with the mode already on has nothing to undo.
	b := enabled()
	client, _ = rig(t, b)
	modes = new(Modes)
	expected = readBaseline(t, client, modes)
	apply, err = modes.StartApplyProfile(client, expected, expected)
	if err != nil {
		t.Fatal(err)
	}
	_, done, err = apply.Absent(errors.New("no dongle"))
	if !done || err == nil || strings.Contains(err.Error(), "may still be active") || modes.Recovery() != nil {
		t.Fatalf("done %v, err %v, recovery %+v", done, err, modes.Recovery())
	}
}
