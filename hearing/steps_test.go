package hearing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/dongle/sim"
	"github.com/Dardrai/airpods-core/hid"
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

// firstPacketDevice runs a hook on each commit, counted from one, and records how many
// commits came before the first AAP packet.
type firstPacketDevice struct {
	*sim.Device
	onCommit func(n int)
	commits  int
	firstAAP int
}

func (d *firstPacketDevice) Set(data [hid.ReportSize]byte) error {
	const opCommit, opSendAACP = 6, 2
	if data[0] == 3 && data[7] == opSendAACP && d.firstAAP == 0 {
		d.firstAAP = d.commits
	}
	if data[0] == 3 && data[7] == opCommit {
		d.commits++
		if d.onCommit != nil {
			d.onCommit(d.commits)
		}
	}
	return d.Device.Set(data)
}

// A failure at the first packet, before it went out, changed nothing: it must not be reported
// as an unknown state or leave a recovery to offer.
func TestFailureBeforeTheFirstPacketIsNotUnconfirmed(t *testing.T) {
	dry := &firstPacketDevice{Device: sim.New(proThree().Config())}
	client := dongle.NewClient(dry)
	client.SetClock(sim.NewVirtualClock())
	if _, err := new(Modes).StartSet(client, HearingAid, true); err != nil {
		t.Fatal(err)
	}
	// The last commit before the packet renews the lease, after the last state check. The one
	// before that is the last read, so anything changed there is seen just before the packet.
	lastRead := dry.firstAAP - 1

	for _, tc := range []struct {
		name   string
		at     int
		change func(*sim.Device)
		undo   func(*sim.Device)
		check  func(*testing.T, error)
	}{
		{
			name:   "a refusal",
			at:     lastRead,
			change: func(d *sim.Device) { d.SetFlags(0x13) },
			undo:   func(d *sim.Device) { d.SetFlags(0x03) },
			check: func(t *testing.T, err error) {
				refused(t, err, "Stop recording")
				if !errors.Is(err, dongle.ErrMicrophoneActive) {
					t.Error("not ErrMicrophoneActive")
				}
			},
		},
		{
			name:   "a lost lease",
			at:     lastRead,
			change: func(d *sim.Device) { d.Update(func(c *sim.Config) { c.Fail = map[byte]byte{leaseKind: 3} }) },
			undo:   func(d *sim.Device) { d.Update(func(c *sim.Config) { c.Fail = nil }) },
			check: func(t *testing.T, err error) {
				var rejected *dongle.RequestRejected
				if !errors.As(err, &rejected) || rejected.Code != 3 {
					t.Errorf("err %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := proThree()
			var device *firstPacketDevice
			device = &firstPacketDevice{Device: sim.New(a.Config()), onCommit: func(n int) {
				if n == tc.at {
					tc.change(device.Device)
				}
			}}
			client := dongle.NewClient(device)
			client.SetClock(sim.NewVirtualClock())
			modes := new(Modes)
			change, err := modes.StartSet(client, HearingAid, true)
			if change != nil || err == nil {
				t.Fatalf("change %v, err %v", change, err)
			}
			tc.check(t, err)
			var unconfirmed *ModeUnconfirmed
			if errors.As(err, &unconfirmed) {
				t.Errorf("reported as unconfirmed: %v", err)
			}
			if !errors.Is(err, dongle.ErrNotSent) {
				t.Errorf("not ErrNotSent: %v", err)
			}
			if strings.Contains(err.Error(), "unknown") {
				t.Errorf("text claims an unknown state: %v", err)
			}
			if len(a.Received) != 0 {
				t.Errorf("sent %x", a.Received)
			}
			if modes.Recovery() != nil {
				t.Error("a recovery was offered for a change that never went out")
			}
			device.onCommit = nil
			tc.undo(device.Device)
			next, err := modes.StartSet(client, HearingAid, true)
			if err != nil {
				t.Fatalf("the next try: %v", err)
			}
			if result, err := next.wait(client); err != nil || result.State.HearingEnabled != FlagOn {
				t.Fatalf("the next try: %+v %v", result, err)
			}
		})
	}
}

func TestLaterPacketFailureStaysUnconfirmed(t *testing.T) {
	var device *firstPacketDevice
	a := proThree()
	device = &firstPacketDevice{Device: sim.New(a.Config())}
	device.onCommit = func(int) {
		if len(a.Received) == 1 {
			device.Update(func(c *sim.Config) { c.Fail = map[byte]byte{leaseKind: 3} })
		}
	}
	client := dongle.NewClient(device)
	client.SetClock(sim.NewVirtualClock())
	modes := new(Modes)
	_, err := modes.StartSet(client, HearingAid, true)
	var unconfirmed *ModeUnconfirmed
	if !errors.As(err, &unconfirmed) || unconfirmed.Recovery == nil {
		t.Fatalf("err %#v", err)
	}
	if !errors.Is(unconfirmed.Err, dongle.ErrNotSent) {
		t.Fatalf("the later packet should have failed before it went out: %v", unconfirmed.Err)
	}
	if errors.Is(err, dongle.ErrNotSent) {
		t.Error("the first packet went out, yet the error matches ErrNotSent")
	}
	var rejected *dongle.RequestRejected
	if !errors.As(err, &rejected) || rejected.Code != 3 {
		t.Errorf("lost its cause: %v", err)
	}
}

func TestSentinelsMatchWithTheirTextUnchanged(t *testing.T) {
	if ErrDeadline.Error() != "The dongle request ran out of time and was not retried." {
		t.Errorf("text %q", ErrDeadline)
	}
	if !errors.Is(connectionChanged, dongle.ErrGenerationChanged) ||
		connectionChanged.Error() != "The dongle connection changed. Read the settings again." {
		t.Errorf("connection changed: %v", connectionChanged)
	}
	a := proThree()
	client, dev := rig(t, a)
	dev.SetFlags(0x13)
	if _, err := new(Modes).StartSet(client, HearingAid, true); !errors.Is(err, dongle.ErrMicrophoneActive) {
		t.Errorf("microphone: %v", err)
	}
	dev.SetFlags(0x00)
	if _, err := new(Modes).StartSet(client, HearingAid, true); !errors.Is(err, dongle.ErrNotReady) {
		t.Errorf("not ready: %v", err)
	}
}

func TestOperationPastItsLimitIsErrDeadline(t *testing.T) {
	client, _ := rig(t, proThree())
	o, err := openOperation(client)
	if err != nil {
		t.Fatal(err)
	}
	defer o.close()
	client.Clock().Sleep(operationLimit)
	if err := o.check(); !errors.Is(err, ErrDeadline) {
		t.Errorf("err %v", err)
	}
}

func endedContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestAnEndedContextEndsAModeChangeAsUnconfirmed(t *testing.T) {
	a := proThree()
	a.Silent = true
	client, _ := rig(t, a)
	modes := new(Modes)
	change, err := modes.StartSet(client, HearingAid, true)
	if err != nil {
		t.Fatal(err)
	}
	sent := len(a.Received)
	_, err = change.wait(client.WithContext(endedContext()))
	var unconfirmed *ModeUnconfirmed
	if !errors.As(err, &unconfirmed) || unconfirmed.Recovery == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if errors.Is(err, dongle.ErrNotSent) {
		t.Error("the change went out, yet the error matches ErrNotSent")
	}
	if len(a.Received) != sent {
		t.Error("sent something while stopping")
	}
	if _, err := modes.Set(client.WithContext(endedContext()), HearingAid, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("starting with an ended context: %v", err)
	}
}

func TestAnEndedContextEndsAProfileApply(t *testing.T) {
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
	ended := client.WithContext(endedContext())
	for range 10 {
		if _, done, err := apply.Step(ended); done {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err %v", err)
			}
			if a.Server.Writes != 0 {
				t.Error("written after the context ended")
			}
			if dev.LeaseHeld() {
				t.Error("left holding the lease")
			}
			return
		}
	}
	t.Fatal("kept going")
}

// reconnectingDevice starts a new generation on the nth status read after the first ATT write.
type reconnectingDevice struct {
	*sim.Device
	airpods   *sim.HearingAirPods
	at, reads int
}

func (d *reconnectingDevice) Get(report byte) ([hid.ReportSize]byte, error) {
	if report == 2 && d.airpods.Server.Writes > 0 {
		if d.reads++; d.reads == d.at {
			d.BumpGeneration()
		}
	}
	return d.Device.Get(report)
}

// A read back that fails before it goes out says nothing about the write before it: wherever
// a reconnect lands after the write, the outcome is unconfirmed and never ErrNotSent.
func TestAReconnectAfterAWriteIsNeverErrNotSent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, client *dongle.Client) error
	}{
		{"profile", func(t *testing.T, client *dongle.Client) error {
			modes := new(Modes)
			expected := readBaseline(t, client, modes)
			requested := expected
			requested.OwnVoice = 0.25
			_, err := modes.ApplyProfile(client, expected, requested)
			return err
		}},
		{"transparency", func(t *testing.T, client *dongle.Client) error {
			modes := new(Modes)
			expected, err := modes.ExperimentalReadTransparency(client)
			if err != nil {
				t.Fatal(err)
			}
			requested := expected
			requested.RightTone = 0.25
			_, err = modes.ExperimentalWriteTransparency(client, expected, requested)
			return err
		}},
		{"loud sound", func(t *testing.T, client *dongle.Client) error {
			_, err := ExperimentalSetLoudSoundReduction(client, false, true)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsentReads, finished := 0, false
			for at := 1; at <= 200 && !finished; at++ {
				a := enabled()
				device := &reconnectingDevice{Device: sim.New(a.Config()), airpods: a, at: at}
				client := dongle.NewClient(device)
				client.SetClock(sim.NewVirtualClock())
				err := tc.write(t, client)
				if a.Server.Writes != 1 {
					t.Fatalf("reconnect at read %d: writes %d", at, a.Server.Writes)
				}
				if err == nil {
					finished = true // The reconnect came after everything was confirmed.
					continue
				}
				var unconfirmed *dongle.Unconfirmed
				if !errors.As(err, &unconfirmed) || !errors.Is(err, dongle.ErrGenerationChanged) {
					t.Fatalf("reconnect at read %d: %v", at, err)
				}
				if errors.Is(err, dongle.ErrNotSent) {
					t.Errorf("reconnect at read %d: the write went out, yet the error matches ErrNotSent", at)
				}
				if errors.Is(unconfirmed.Err, dongle.ErrNotSent) {
					unsentReads++
				}
			}
			if unsentReads == 0 {
				t.Error("no reconnect landed before a read back went out")
			}
			if !finished {
				t.Error("the reconnect never came late enough for the write to finish")
			}
		})
	}
}
