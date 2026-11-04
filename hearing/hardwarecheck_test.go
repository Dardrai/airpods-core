package hearing

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/dongle/sim"
)

func checkWrites(a *sim.HearingAirPods) int { return a.Server.Writes }

func TestCheckGoldenRequestAndCompletion(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	ticks := 0
	var phases []CheckPhase
	progress, err := RunHardwareCheck(client, HardwareCheckOptions{
		BudsInCaseConfirmed: true,
		Stop: func() bool {
			ticks++
			if ticks == 40 { // 10 s in: the results arrive.
				a.Server.Find(sim.UUIDDiagnostics).Value = sim.DiagnosticRecord(1750009000, 1750009300, 0, 0)
			}
			return false
		},
		Progress: func(p CheckProgress) {
			if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
				phases = append(phases, p.Phase)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Phase != CheckCompleted || !progress.LeftUpdated || !progress.RightUpdated || progress.Record.Left.MeasuredAtSeconds != 1750009000 {
		t.Fatalf("progress %+v", progress)
	}
	var writes [][]byte
	for _, pdu := range dev.Exchanges() {
		if pdu[0] == attWrite {
			writes = append(writes, pdu)
		}
	}
	samePackets(t, writes, [][]byte{{0x12, 0x1d, 0x00, 0x31}})
	if phases[0] != CheckWaitingCaseClose || phases[len(phases)-1] != CheckCompleted {
		t.Errorf("phases %v", phases)
	}
	if dev.LeaseHeld() {
		t.Error("lease kept through the wait")
	}
}

func TestCheckPreconditions(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		firmware  string
		setup     func(a *sim.HearingAirPods, dev *sim.Device)
		unconfirm bool
		want      string
	}{
		{name: "Pro 2", model: "A3047", firmware: "9A348", want: "verified"},
		{name: "another firmware", model: "A3063", firmware: "9A349", want: "9A348"},
		{name: "older firmware", model: "A3065", firmware: "8A358", want: "9A348"},
		{name: "not confirmed", model: "A3064", firmware: "9A348", unconfirm: true, want: "open case"},
		{name: "one bud out", model: "A3063", firmware: "9A348", want: "open case", setup: func(a *sim.HearingAirPods, _ *sim.Device) {
			a.Slots[5] = sim.CachedReport{Revision: 2, Packet: sim.EarPacket(2, 1)}
		}},
		{name: "in ear", model: "A3063", firmware: "9A348", want: "open case", setup: func(a *sim.HearingAirPods, _ *sim.Device) {
			a.Slots[5] = sim.CachedReport{Revision: 2, Packet: sim.EarPacket(0, 0)}
		}},
		{name: "moving", model: "A3063", firmware: "9A348", want: "open case", setup: func(a *sim.HearingAirPods, _ *sim.Device) {
			a.Slots[5] = sim.CachedReport{Revision: 2, Packet: sim.EarPacket(4, 2)}
		}},
		{name: "no ear report", model: "A3063", firmware: "9A348", want: "open case", setup: func(a *sim.HearingAirPods, _ *sim.Device) {
			delete(a.Slots, 5)
		}},
		{name: "stale ear report", model: "A3063", firmware: "9A348", want: "open case", setup: func(a *sim.HearingAirPods, _ *sim.Device) {
			a.Slots[5] = sim.CachedReport{Age: 30001, Revision: 2, Packet: sim.EarPacket(2, 2)}
		}},
		{name: "streaming", model: "A3063", firmware: "9A348", want: "Pause", setup: func(_ *sim.HearingAirPods, dev *sim.Device) { dev.SetFlags(0x07) }},
		{name: "phone", model: "A3063", firmware: "9A348", want: "Pause", setup: func(_ *sim.HearingAirPods, dev *sim.Device) { dev.SetFlags(0x0b) }},
		{name: "USB playback", model: "A3063", firmware: "9A348", want: "Pause", setup: func(_ *sim.HearingAirPods, dev *sim.Device) { dev.SetFlags(0x43) }},
		{name: "recording", model: "A3063", firmware: "9A348", want: "recording", setup: func(_ *sim.HearingAirPods, dev *sim.Device) { dev.SetFlags(0x13) }},
		{name: "disconnected", model: "A3063", firmware: "9A348", want: "Connect", setup: func(_ *sim.HearingAirPods, dev *sim.Device) { dev.SetFlags(0) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := sim.NewHearingAirPods(tc.model, tc.firmware)
			client, dev := rig(t, a)
			if tc.setup != nil {
				tc.setup(a, dev)
			}
			refused(t, HardwareCheckReady(client, !tc.unconfirm), tc.want)
			_, err := RunHardwareCheck(client, HardwareCheckOptions{BudsInCaseConfirmed: !tc.unconfirm})
			refused(t, err, tc.want)
			if checkWrites(a) != 0 {
				t.Error("request sent")
			}
		})
	}
	a := proThree()
	client, _ := rig(t, a)
	if err := HardwareCheckReady(client, true); err != nil {
		t.Errorf("ready: %v", err)
	}
}

func TestCheckRechecksRightBeforeTheWrite(t *testing.T) {
	a := proThree()
	lookedAtRequest := false
	a.BeforeExchange = func(pdu []byte) {
		if pdu[0] == attByType {
			lookedAtRequest = true
			a.Slots[5] = sim.CachedReport{Revision: 2, Packet: sim.EarPacket(0, 2)}
		}
	}
	client, _ := rig(t, a)
	_, err := RunHardwareCheck(client, HardwareCheckOptions{BudsInCaseConfirmed: true})
	refused(t, err, "open case")
	if !lookedAtRequest || checkWrites(a) != 0 {
		t.Errorf("request sent %d", checkWrites(a))
	}
}

func TestCheckNeedsASavedRecordAndAWritableRequest(t *testing.T) {
	a := proThree()
	a.Server.Find(sim.UUIDDiagnostics).UUID = [16]byte{1}
	client, _ := rig(t, a)
	_, err := RunHardwareCheck(client, HardwareCheckOptions{BudsInCaseConfirmed: true})
	refused(t, err, "saved hardware results")

	for name, props := range map[string]byte{"no write": 0x02, "command only": 0x04} {
		a = proThree()
		a.Server.Find(sim.UUIDCheck).Props = props
		client, _ = rig(t, a)
		_, err = RunHardwareCheck(client, HardwareCheckOptions{BudsInCaseConfirmed: true})
		if err == nil || checkWrites(a) != 0 {
			t.Errorf("%s: %v, %d writes", name, err, checkWrites(a))
		}
	}
}

func TestCheckRequestIsSentOnceWhenUnacknowledged(t *testing.T) {
	a := proThree()
	a.Server.Find(sim.UUIDCheck).Props = 0x08
	a.BeforeExchange = func(pdu []byte) {
		if pdu[0] == attWrite {
			a.Server.RefuseWrites = true
		}
	}
	client, dev := rig(t, a)
	progress, err := RunHardwareCheck(client, HardwareCheckOptions{BudsInCaseConfirmed: true})
	var u *dongle.Unconfirmed
	if !errors.As(err, &u) || progress.Phase != CheckError {
		t.Fatalf("%+v %v", progress, err)
	}
	requests := 0
	for _, pdu := range dev.Exchanges() {
		if pdu[0] == attWrite {
			requests++
		}
	}
	if requests != 1 {
		t.Errorf("requests %d", requests)
	}
}

func TestCheckTimesOutAt120SecondsKeepingPartialProgress(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
	ticks := 0
	progress, err := RunHardwareCheck(client, HardwareCheckOptions{
		BudsInCaseConfirmed: true,
		Stop: func() bool {
			ticks++
			if ticks == 8 {
				a.Server.Find(sim.UUIDDiagnostics).Value = sim.DiagnosticRecord(1750009000, 1750000300, 0, 0)
			}
			return false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Phase != CheckTimedOut || !progress.LeftUpdated || progress.RightUpdated {
		t.Errorf("%+v", progress)
	}
	if progress.Elapsed != checkWait || progress.Remaining != 0 {
		t.Errorf("elapsed %v remaining %v", progress.Elapsed, progress.Remaining)
	}
	if ticks != int(checkWait/checkTick)+1 {
		t.Errorf("ticks %d", ticks)
	}
}

func TestStoppingOnlyEndsTheWaiting(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	ticks := 0
	progress, err := RunHardwareCheck(client, HardwareCheckOptions{
		BudsInCaseConfirmed: true,
		Stop: func() bool {
			ticks++
			return ticks == 5
		},
	})
	if err != nil || progress.Phase != CheckStopped {
		t.Fatalf("%+v %v", progress, err)
	}
	requests := 0
	for _, pdu := range dev.Exchanges() {
		if pdu[0] == attWrite {
			requests++
		}
	}
	if requests != 1 || len(a.Received) != 0 {
		t.Errorf("stopping should send nothing: %d, %x", requests, a.Received)
	}
}

func TestCheckSurvivesTheCaseClosingAndAReconnect(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	ticks := 0
	var sawWaitingResult bool
	progress, err := RunHardwareCheck(client, HardwareCheckOptions{
		BudsInCaseConfirmed: true,
		Stop: func() bool {
			ticks++
			switch ticks {
			case 10:
				dev.SetFlags(0)
			case 100:
				dev.BumpGeneration()
				dev.SetFlags(0x03)
				a.Server.Find(sim.UUIDDiagnostics).Value = sim.DiagnosticRecord(1, 2, 0, 0) // Older is still new.
			}
			return false
		},
		Progress: func(p CheckProgress) { sawWaitingResult = sawWaitingResult || p.Phase == CheckWaitingResult },
	})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Phase != CheckCompleted || !sawWaitingResult {
		t.Errorf("%+v, saw waiting %v", progress, sawWaitingResult)
	}
}

func TestCheckStopsFollowingWhenOtherAirPodsConnect(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	ticks := 0
	progress, err := RunHardwareCheck(client, HardwareCheckOptions{
		BudsInCaseConfirmed: true,
		Stop: func() bool {
			ticks++
			if ticks == 5 {
				dev.Update(func(c *sim.Config) { c.Peer = sim.Hex{9, 9, 9, 9, 9, 9} })
			}
			return false
		},
	})
	if err == nil || progress.Phase != CheckError || !strings.Contains(err.Error(), "Different AirPods") {
		t.Errorf("%+v %v", progress, err)
	}
}

func TestCheckFailsOnABadReadWhileTheLinkHolds(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
	ticks := 0
	progress, err := RunHardwareCheck(client, HardwareCheckOptions{
		BudsInCaseConfirmed: true,
		Stop: func() bool {
			ticks++
			if ticks == 5 {
				a.Server.Find(sim.UUIDDiagnostics).Value = bytes.Repeat([]byte{1}, 10)
			}
			return false
		},
	})
	if err == nil || progress.Phase != CheckError {
		t.Errorf("%+v %v", progress, err)
	}
}

func TestCheckJobRules(t *testing.T) {
	base := Diagnostics{Left: EarResult{MeasuredAtSeconds: 100}, Right: EarResult{MeasuredAtSeconds: 200}}
	record := func(left, right uint64) Diagnostics {
		return Diagnostics{Left: EarResult{MeasuredAtSeconds: left}, Right: EarResult{MeasuredAtSeconds: right}}
	}
	job := newCheckJob(base)
	job.observe(record(100, 200))
	job.observe(record(0, 0))
	if job.updated != [2]bool{} || job.phase != CheckWaitingCaseClose {
		t.Errorf("stale results moved it: %+v", job)
	}
	job.observe(record(101, 200))
	if job.updated != [2]bool{true, false} || job.phase != CheckWaitingResult {
		t.Errorf("%+v", job)
	}
	job.observe(record(100, 201))
	if job.phase == CheckCompleted {
		t.Error("a replayed baseline completed it")
	}
	job.observe(record(99, 199))
	if job.phase != CheckCompleted {
		t.Errorf("%+v", job)
	}
	job.tick(time.Hour)
	job.fail("late")
	job.stop()
	if job.phase != CheckCompleted || job.message != "" {
		t.Errorf("a finished job moved: %+v", job)
	}
	late := newCheckJob(base)
	late.tick(checkWait - 1)
	if late.phase != CheckWaitingCaseClose {
		t.Error("too early")
	}
	late.tick(checkWait)
	late.observe(record(101, 201))
	if late.phase != CheckTimedOut {
		t.Errorf("late result accepted: %+v", late)
	}
}

func TestCheckStepsNeverHoldTheLeaseAndLeaveTheClientFree(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	check, _, err := StartHardwareCheck(client, HardwareCheckOptions{BudsInCaseConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if dev.LeaseHeld() {
		t.Fatal("lease kept after the request")
	}
	if _, done, err := check.Step(client); done || err != nil {
		t.Fatalf("first step: %v %v", done, err)
	}
	if dev.LeaseHeld() {
		t.Fatal("lease kept after a step")
	}
	// Something else can use the client between steps.
	if _, err := client.Status(); err != nil {
		t.Fatal(err)
	}
	if err := HardwareCheckReady(client, true); err != nil {
		t.Fatalf("a request between steps: %v", err)
	}
	if progress, done, err := check.Absent(); done || err != nil || progress.Phase != CheckWaitingResult {
		t.Fatalf("absent: %+v %v %v", progress, done, err)
	}
	a.Server.Find(sim.UUIDDiagnostics).Value = sim.DiagnosticRecord(1750009000, 1750009300, 0, 0)
	for i := 0; i < 40; i++ {
		progress, done, err := check.Step(client)
		if done {
			if err != nil || progress.Phase != CheckCompleted {
				t.Fatalf("final %+v %v", progress, err)
			}
			return
		}
		client.Clock().Sleep(checkTick)
	}
	t.Fatal("never completed")
}

func TestCheckStoppedWhileAbsent(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
	stop := false
	check, _, err := StartHardwareCheck(client, HardwareCheckOptions{BudsInCaseConfirmed: true, Stop: func() bool { return stop }})
	if err != nil {
		t.Fatal(err)
	}
	stop = true
	if progress, done, err := check.Absent(); !done || err != nil || progress.Phase != CheckStopped {
		t.Fatalf("%+v %v %v", progress, done, err)
	}
}
