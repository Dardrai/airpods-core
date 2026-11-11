package hearing

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/dongle/sim"
	"github.com/Dardrai/airpods-core/hid"
)

func TestTransparencyGoldenWriteKeepsCurvesAndOwnVoice(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	modes := new(Modes)
	expected, err := modes.ExperimentalReadTransparency(client)
	if err != nil {
		t.Fatal(err)
	}
	if expected.Enabled || expected.LeftAmplification != 0.5 || expected.RightAmbientNoiseReduction != 0.25 {
		t.Fatalf("%+v", expected)
	}
	requested := expected
	requested.Enabled = true
	requested.RightTone = -0.5
	requested.LeftConversationBoost = true
	got, err := modes.ExperimentalWriteTransparency(client, expected, requested)
	if err != nil {
		t.Fatal(err)
	}
	if got != requested {
		t.Errorf("%+v", got)
	}
	want := append([]byte{0x12, 0x21, 0}, sim.TransparencyRecord(false)...)
	putFloat(want[3:], 0, 1)
	putFloat(want[3:], offRightTone, -0.5)
	putFloat(want[3:], offLeftConv, 1)
	var writes [][]byte
	for _, pdu := range dev.Exchanges() {
		if pdu[0] == attWrite {
			writes = append(writes, pdu)
		}
	}
	samePackets(t, writes, [][]byte{want})
}

func TestTransparencyRefusals(t *testing.T) {
	read := func(t *testing.T, a *sim.HearingAirPods) (*dongle.Client, *Modes, TransparencySettings) {
		t.Helper()
		client, _ := rig(t, a)
		modes := new(Modes)
		expected, err := modes.ExperimentalReadTransparency(client)
		if err != nil {
			t.Fatal(err)
		}
		return client, modes, expected
	}
	t.Run("changed since reading", func(t *testing.T) {
		a := proThree()
		client, modes, expected := read(t, a)
		putFloat(a.Server.Find(sim.UUIDTransparency).Value, offLeftTone, 0.5)
		requested := expected
		requested.RightTone = 0.25
		_, err := modes.ExperimentalWriteTransparency(client, expected, requested)
		refused(t, err, "changed on the AirPods")
		if a.Server.Writes != 0 {
			t.Error("wrote")
		}
	})
	t.Run("out of range", func(t *testing.T) {
		a := proThree()
		client, modes, expected := read(t, a)
		for _, change := range []func(*TransparencySettings){
			func(s *TransparencySettings) { s.LeftAmplification = 2.5 },
			func(s *TransparencySettings) { s.RightAmplification = -1.25 },
			func(s *TransparencySettings) { s.RightTone = float32(math.NaN()) },
			func(s *TransparencySettings) { s.LeftAmbientNoiseReduction = -1 },
		} {
			requested := expected
			change(&requested)
			_, err := modes.ExperimentalWriteTransparency(client, expected, requested)
			refused(t, err, "out of range")
		}
		if a.Server.Writes != 0 {
			t.Error("wrote")
		}
	})
	t.Run("no change writes nothing", func(t *testing.T) {
		a := proThree()
		client, modes, expected := read(t, a)
		if _, err := modes.ExperimentalWriteTransparency(client, expected, expected); err != nil {
			t.Fatal(err)
		}
		if a.Server.Writes != 0 {
			t.Error("wrote")
		}
	})
	t.Run("hearing assistance on", func(t *testing.T) {
		a := proThree()
		a.Slots[2] = sim.CachedReport{Revision: 1, Packet: hearingOn}
		a.Slots[3] = sim.CachedReport{Revision: 1, Packet: assistOn}
		client, modes, expected := read(t, a)
		requested := expected
		requested.Enabled = true
		_, err := modes.ExperimentalWriteTransparency(client, expected, requested)
		refused(t, err, "must be reported off")
		if a.Server.Writes != 0 {
			t.Error("wrote")
		}
	})
	t.Run("hearing state unknown", func(t *testing.T) {
		a := proThree()
		delete(a.Slots, 3)
		client, modes, expected := read(t, a)
		requested := expected
		requested.Enabled = true
		_, err := modes.ExperimentalWriteTransparency(client, expected, requested)
		refused(t, err, "must be reported off")
	})
	t.Run("read-only attribute", func(t *testing.T) {
		a := proThree()
		a.Server.Find(sim.UUIDTransparency).Props = 0x02
		client, modes, expected := read(t, a)
		requested := expected
		requested.RightTone = 0.25
		_, err := modes.ExperimentalWriteTransparency(client, expected, requested)
		refused(t, err, "read-only")
	})
	t.Run("model without the feature", func(t *testing.T) {
		a := sim.NewHearingAirPods("A2564", "9A348")
		client, _ := rig(t, a)
		_, err := new(Modes).ExperimentalReadTransparency(client)
		refused(t, err, "not supported")
	})
	t.Run("unknown record format", func(t *testing.T) {
		a := proThree()
		putFloat(a.Server.Find(sim.UUIDTransparency).Value, 0, 0.5)
		client, _ := rig(t, a)
		if _, err := new(Modes).ExperimentalReadTransparency(client); err == nil || !strings.Contains(err.Error(), "format") {
			t.Errorf("err %v", err)
		}
	})
}

func TestTransparencyReadbackIsNeverFaked(t *testing.T) {
	apply := func(t *testing.T, a *sim.HearingAirPods) error {
		t.Helper()
		client, _ := rig(t, a)
		modes := new(Modes)
		expected, err := modes.ExperimentalReadTransparency(client)
		if err != nil {
			t.Fatal(err)
		}
		requested := expected
		requested.RightTone = 0.25
		_, err = modes.ExperimentalWriteTransparency(client, expected, requested)
		if a.Server.Writes > 1 {
			t.Errorf("writes %d", a.Server.Writes)
		}
		return err
	}
	unconfirmedErr := func(t *testing.T, err error) {
		t.Helper()
		var u *dongle.Unconfirmed
		if !errors.As(err, &u) {
			t.Fatalf("err %#v", err)
		}
	}
	t.Run("delayed", func(t *testing.T) {
		a := proThree()
		attribute := a.Server.Find(sim.UUIDTransparency)
		a.BeforeExchange = func(pdu []byte) {
			if pdu[0] == attWrite {
				attribute.AppliesAfterReads = 2
			}
		}
		if err := apply(t, a); err != nil {
			t.Error(err)
		}
	})
	t.Run("never applied", func(t *testing.T) {
		a := proThree()
		attribute := a.Server.Find(sim.UUIDTransparency)
		a.BeforeExchange = func(pdu []byte) {
			if pdu[0] == attWrite {
				attribute.AppliesAfterReads = 6
			}
		}
		unconfirmedErr(t, apply(t, a))
	})
	t.Run("another value", func(t *testing.T) {
		a := proThree()
		attribute := a.Server.Find(sim.UUIDTransparency)
		attribute.OnWrite = func(value []byte) {
			other := bytes.Clone(value)
			putFloat(other, offLeftTone, 0.75)
			attribute.Value = other
		}
		unconfirmedErr(t, apply(t, a))
	})
	t.Run("refused", func(t *testing.T) {
		a := proThree()
		a.BeforeExchange = func(pdu []byte) { a.Server.RefuseWrites = pdu[0] == attWrite }
		unconfirmedErr(t, apply(t, a))
	})
}

// Custom Transparency comes as a 100 byte record without Own Voice too, and amplification
// runs to 2. A write keeps the length it read.
func TestTransparencyRecordForms(t *testing.T) {
	t.Run("100 bytes", func(t *testing.T) {
		a := proThree()
		a.Server.Find(sim.UUIDTransparency).Value = sim.ShortTransparencyRecord(false)
		client, dev := rig(t, a)
		modes := new(Modes)
		expected, err := modes.ExperimentalReadTransparency(client)
		if err != nil {
			t.Fatal(err)
		}
		requested := expected
		requested.LeftAmplification = 2
		if got, err := modes.ExperimentalWriteTransparency(client, expected, requested); err != nil || got != requested {
			t.Fatalf("%+v %v", got, err)
		}
		want := append([]byte{0x12, 0x21, 0}, sim.ShortTransparencyRecord(false)...)
		putFloat(want[3:], offLeftAmp, 2)
		var writes [][]byte
		for _, pdu := range dev.Exchanges() {
			if pdu[0] == attWrite {
				writes = append(writes, pdu)
			}
		}
		samePackets(t, writes, [][]byte{want})
	})
	t.Run("amplification above 1", func(t *testing.T) {
		value := sim.TransparencyRecord(false)
		putFloat(value, offRightAmp, 1.75)
		if s, err := parseTransparency(value); err != nil || s.RightAmplification != 1.75 {
			t.Errorf("%+v %v", s, err)
		}
		putFloat(value, offRightAmp, 2.25)
		if _, err := parseTransparency(value); err == nil {
			t.Error("2.25 accepted")
		}
	})
	t.Run("own voice only in the long form", func(t *testing.T) {
		value := sim.TransparencyRecord(false)
		putFloat(value, offOwnVoice, 1.5)
		if _, err := parseTransparency(value); err == nil {
			t.Error("own voice 1.5 accepted")
		}
		for _, bad := range [][]byte{value[:96], value[:101], sim.UnsetTransparencyRecord()} {
			if _, err := parseTransparency(bad); err == nil {
				t.Errorf("%d bytes accepted", len(bad))
			}
		}
	})
}

func TestTheDonglePassingNothingIsAnError(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	dev.Update(func(c *sim.Config) { c.Exchange = nil })
	if _, err := new(Modes).ExperimentalReadTransparency(client); err == nil {
		t.Error("transparency")
	}
	if _, err := ExperimentalReadLoudSoundReduction(client); err == nil {
		t.Error("loud sound")
	}
	if _, err := ExperimentalSetLoudSoundReduction(client, false, true); err == nil {
		t.Error("loud sound write")
	}
	a = proThree()
	a.Server.Unreachable = true
	client, _ = rig(t, a)
	if _, err := ExperimentalReadLoudSoundReduction(client); err == nil || !strings.Contains(err.Error(), "error 0x0E") {
		t.Errorf("err %v", err)
	}
}

// valueRequests keeps the reads and writes, leaving out discovery.
func valueRequests(exchanges [][]byte) [][]byte {
	var out [][]byte
	for _, pdu := range exchanges {
		if pdu[0] == attRead || pdu[0] == attWrite || pdu[0] == attCommand {
			out = append(out, pdu)
		}
	}
	return out
}

func TestLoudSoundGoldenAndGating(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	current, err := ExperimentalReadLoudSoundReduction(client)
	if err != nil || current {
		t.Fatalf("%v %v", current, err)
	}
	after, err := ExperimentalSetLoudSoundReduction(client, false, true)
	if err != nil || !after {
		t.Fatalf("%v %v", after, err)
	}
	// Found at 0x23 by discovery: two baseline reads, one write, one read back.
	read := []byte{0x0a, 0x23, 0}
	samePackets(t, valueRequests(dev.Exchanges()), [][]byte{read, read, read, {0x12, 0x23, 0, 1}, read})
	searches := 0
	for _, pdu := range dev.Exchanges() {
		if pdu[0] == attFindType {
			searches++
		}
	}
	if searches != 2 {
		t.Errorf("each request discovers again: %d searches", searches)
	}

	if _, err := ExperimentalSetLoudSoundReduction(client, false, false); err == nil {
		t.Error("a stale expected value was accepted")
	} else {
		refused(t, err, "changed")
	}
	writes := a.Server.Writes
	if _, err := ExperimentalSetLoudSoundReduction(client, true, true); err != nil || a.Server.Writes != writes {
		t.Errorf("no change: %v, %d writes", err, a.Server.Writes)
	}

	for _, model := range []string{"A2564", "A3454"} { // AirPods 3; AirPods Max 2
		other := sim.NewHearingAirPods(model, "9A348")
		client, _ = rig(t, other)
		_, err = ExperimentalReadLoudSoundReduction(client)
		refused(t, err, "not supported")
	}
}

// Whatever sits at a fixed handle is never touched: only a sound discovery decides.
func TestLoudSoundNeverUsesAFixedHandle(t *testing.T) {
	for name, fault := range map[string]sim.DiscoveryFault{
		"truncated":     sim.DiscoveryTruncated,
		"duplicate":     sim.DiscoveryDuplicate,
		"out of range":  sim.DiscoveryOutOfRange,
		"foreign error": sim.DiscoveryForeignError,
	} {
		t.Run(name, func(t *testing.T) {
			a := proThree()
			a.Server.Fault = fault
			client, dev := rig(t, a)
			if _, err := ExperimentalReadLoudSoundReduction(client); err == nil {
				t.Error("read")
			}
			if _, err := ExperimentalSetLoudSoundReduction(client, false, true); err == nil {
				t.Error("write")
			}
			if a.Server.Writes != 0 || len(valueRequests(dev.Exchanges())) != 0 {
				t.Errorf("used a handle: %x", valueRequests(dev.Exchanges()))
			}
		})
	}
	t.Run("absent", func(t *testing.T) {
		a := proThree()
		a.Server.Attributes = a.Server.Attributes[:len(a.Server.Attributes)-1]
		client, dev := rig(t, a)
		_, err := ExperimentalSetLoudSoundReduction(client, false, true)
		refused(t, err, "not available")
		if len(valueRequests(dev.Exchanges())) != 0 {
			t.Errorf("used a handle: %x", valueRequests(dev.Exchanges()))
		}
	})
	t.Run("another characteristic listed twice", func(t *testing.T) {
		a := proThree()
		twin := *a.Server.Find(sim.UUIDLoudSound)
		twin.UUID, twin.Handle = sim.UUIDHearing, 0x25
		a.Server.Attributes = append(a.Server.Attributes, &twin)
		client, _ := rig(t, a)
		if _, err := ExperimentalReadLoudSoundReduction(client); err == nil || !strings.Contains(err.Error(), "twice") {
			t.Errorf("err %v", err)
		}
	})
}

func TestLoudSoundWriteFormFollowsTheProperties(t *testing.T) {
	t.Run("write command", func(t *testing.T) {
		a := proThree()
		a.Server.Find(sim.UUIDLoudSound).Props = 0x06
		client, dev := rig(t, a)
		if after, err := ExperimentalSetLoudSoundReduction(client, false, true); err != nil || !after {
			t.Fatalf("%v %v", after, err)
		}
		requests := valueRequests(dev.Exchanges())
		if len(requests) != 4 || !bytes.Equal(requests[2], []byte{attCommand, 0x23, 0, 1}) {
			t.Errorf("%x", requests)
		}
	})
	t.Run("read only", func(t *testing.T) {
		a := proThree()
		a.Server.Find(sim.UUIDLoudSound).Props = 0x02
		client, _ := rig(t, a)
		if current, err := ExperimentalReadLoudSoundReduction(client); err != nil || current {
			t.Fatalf("%v %v", current, err)
		}
		_, err := ExperimentalSetLoudSoundReduction(client, false, true)
		refused(t, err, "read-only")
		if a.Server.Writes != 0 {
			t.Error("wrote")
		}
	})
	t.Run("not readable", func(t *testing.T) {
		a := proThree()
		a.Server.Find(sim.UUIDLoudSound).Props = 0x08
		client, _ := rig(t, a)
		if _, err := ExperimentalSetLoudSoundReduction(client, false, true); err == nil || a.Server.Writes != 0 {
			t.Errorf("%v, %d writes", err, a.Server.Writes)
		}
	})
}

func TestLoudSoundOutcomesAreHonest(t *testing.T) {
	t.Run("not a flag", func(t *testing.T) {
		a := proThree()
		a.Server.Find(sim.UUIDLoudSound).Value = []byte{7}
		client, _ := rig(t, a)
		if _, err := ExperimentalSetLoudSoundReduction(client, false, true); err == nil {
			t.Error("accepted")
		}
		if a.Server.Writes != 0 {
			t.Error("wrote over something unknown")
		}
	})
	t.Run("wrong length", func(t *testing.T) {
		for _, value := range [][]byte{{}, {0, 0}, {1, 0}} {
			a := proThree()
			a.Server.Find(sim.UUIDLoudSound).Value = value
			client, _ := rig(t, a)
			if _, err := ExperimentalSetLoudSoundReduction(client, false, true); err == nil || a.Server.Writes != 0 {
				t.Errorf("%x: %v, %d writes", value, err, a.Server.Writes)
			}
		}
	})
	t.Run("changed between the baseline reads", func(t *testing.T) {
		a := proThree()
		attribute := a.Server.Find(sim.UUIDLoudSound)
		reads := 0
		a.BeforeExchange = func(pdu []byte) {
			if pdu[0] == attRead {
				if reads++; reads == 2 {
					attribute.Value = []byte{1}
				}
			}
		}
		client, _ := rig(t, a)
		_, err := ExperimentalSetLoudSoundReduction(client, false, true)
		refused(t, err, "changed")
		if a.Server.Writes != 0 {
			t.Error("wrote")
		}
	})
	t.Run("lands late", func(t *testing.T) {
		a := proThree()
		attribute := a.Server.Find(sim.UUIDLoudSound)
		a.BeforeExchange = func(pdu []byte) {
			if pdu[0] == attWrite {
				attribute.AppliesAfterReads = 4
			}
		}
		client, dev := rig(t, a)
		if after, err := ExperimentalSetLoudSoundReduction(client, false, true); err != nil || !after {
			t.Fatalf("%v %v", after, err)
		}
		if got := len(valueRequests(dev.Exchanges())); got != 2+1+4 || a.Server.Writes != 1 {
			t.Errorf("%d requests, %d writes", got, a.Server.Writes)
		}
	})
	t.Run("did not stick", func(t *testing.T) {
		a := proThree()
		attribute := a.Server.Find(sim.UUIDLoudSound)
		attribute.OnWrite = func([]byte) { attribute.Value = []byte{0} }
		client, dev := rig(t, a)
		after, err := ExperimentalSetLoudSoundReduction(client, false, true)
		var u *dongle.Unconfirmed
		if !errors.As(err, &u) || after {
			t.Errorf("%v %v", after, err)
		}
		if a.Server.Writes != 1 || len(valueRequests(dev.Exchanges())) != 2+1+5 {
			t.Errorf("writes %d, requests %x", a.Server.Writes, valueRequests(dev.Exchanges()))
		}
	})
	t.Run("refused", func(t *testing.T) {
		a := proThree()
		a.BeforeExchange = func(pdu []byte) { a.Server.RefuseWrites = pdu[0] == attWrite }
		client, _ := rig(t, a)
		_, err := ExperimentalSetLoudSoundReduction(client, false, true)
		var u *dongle.Unconfirmed
		if !errors.As(err, &u) || !strings.Contains(u.Err.Error(), "error 0x03") {
			t.Errorf("err %#v", err)
		}
	})
	t.Run("an error for another handle", func(t *testing.T) {
		a := proThree()
		client, dev := rig(t, a)
		dev.Update(func(c *sim.Config) {
			handle := c.Exchange
			c.Exchange = func(pdu []byte) []byte {
				if pdu[0] == attRead {
					return []byte{attError, attRead, 0x1b, 0, 0x0e}
				}
				return handle(pdu)
			}
		})
		if _, err := ExperimentalReadLoudSoundReduction(client); err == nil || !strings.Contains(err.Error(), "different ATT request") {
			t.Errorf("err %v", err)
		}
	})
}

// unlisted lets the sim pass rename packets on, as a dongle without the fixed packet list
// would, and keeps every packet the AirPods received.
func unlisted(t *testing.T, a *sim.HearingAirPods) (*dongle.Client, *sim.Device) {
	t.Helper()
	cfg := a.Config()
	cfg.AnyAAP = true
	dev := sim.New(cfg)
	client := dongle.NewClient(dev)
	client.SetClock(sim.NewVirtualClock())
	return client, dev
}

var eqBaseline = EQ{On: false, Low: 30, Mid: 50, High: 70}

func TestEQReadModifyWriteKeepsTheOtherBands(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
	current, err := ExperimentalReadEQ(client)
	if err != nil || current != eqBaseline {
		t.Fatalf("%+v %v", current, err)
	}
	requested := current
	requested.Low = 42
	got, err := ExperimentalSetEQ(client, current, requested)
	if err != nil || got != requested {
		t.Fatalf("%+v %v", got, err)
	}
	on := got
	on.On = true
	if got, err = ExperimentalSetEQ(client, got, on); err != nil || got != on {
		t.Fatalf("%+v %v", got, err)
	}
	samePackets(t, a.Received, [][]byte{sim.EQReport(false, 42, 50, 70), sim.EQReport(true, 42, 50, 70)})
	change, err := ExperimentalStartSetEQ(client, on, on)
	if err != nil || change.Sent() {
		t.Fatalf("no change: %v", err)
	}
	if result, done, err := change.Step(client); !done || err != nil || result != on {
		t.Errorf("%+v %v %v", result, done, err)
	}
	if len(a.Received) != 2 || a.Server.Writes != 0 {
		t.Errorf("sent %x, %d writes", a.Received, a.Server.Writes)
	}
}

func TestEQStepsSendNothing(t *testing.T) {
	a := proThree()
	a.SilentSlots = map[byte]bool{7: true}
	client, dev := rig(t, a)
	requested := eqBaseline
	requested.On = true
	change, err := ExperimentalStartSetEQ(client, eqBaseline, requested)
	if err != nil || !change.Sent() {
		t.Fatal(err)
	}
	for range 3 {
		freeBetweenSteps(t, client, dev)
		if _, done, err := change.Step(client); done || err != nil {
			t.Fatalf("done %v, err %v", done, err)
		}
	}
	a.Report(7, sim.EQReport(true, 30, 50, 70))
	if result, done, err := change.Step(client); !done || err != nil || result != requested {
		t.Fatalf("%+v %v %v", result, done, err)
	}
	if len(a.Received) != 1 {
		t.Errorf("sent %x", a.Received)
	}
}

// The report ages between the reads; only a new revision or new bytes are an external edit.
func TestAgingEQReportIsNotAnExternalEdit(t *testing.T) {
	requested := eqBaseline
	requested.Low = 42
	a := proThree()
	device := &firstPacketDevice{Device: sim.New(a.Config()), onCommit: func(int) {
		report := a.Slots[7]
		report.Age += 10
		a.Slots[7] = report
	}}
	client := dongle.NewClient(device)
	client.SetClock(sim.NewVirtualClock())
	if _, err := ExperimentalSetEQ(client, eqBaseline, requested); err != nil {
		t.Fatal(err)
	}
	if len(a.Received) != 1 {
		t.Errorf("sent %x", a.Received)
	}

	// The same bytes under a new revision are a new report, so the edit stops.
	b := proThree()
	reads := &slotReadDevice{Device: sim.New(b.Config())}
	reads.before = func(n int) {
		if n == 2 {
			b.Report(7, b.Slots[7].Packet)
		}
	}
	client = dongle.NewClient(reads)
	client.SetClock(sim.NewVirtualClock())
	_, err := ExperimentalSetEQ(client, eqBaseline, requested)
	refused(t, err, "changed")
	if len(b.Received) != 0 {
		t.Errorf("sent %x", b.Received)
	}
}

// slotReadDevice runs before(n) ahead of the nth kept report read.
type slotReadDevice struct {
	*sim.Device
	before func(n int)
	reads  int
}

func (d *slotReadDevice) Set(data [hid.ReportSize]byte) error {
	const opCached = 3
	if data[0] == 3 && data[7] == opCached {
		d.reads++
		d.before(d.reads)
	}
	return d.Device.Set(data)
}

func TestEQIsConfirmedOnlyByAFreshMatchingReport(t *testing.T) {
	requested := eqBaseline
	requested.Low = 42
	unconfirmedErr := func(t *testing.T, err error) {
		t.Helper()
		var u *dongle.Unconfirmed
		if !errors.As(err, &u) || errors.Is(err, dongle.ErrNotSent) {
			t.Fatalf("err %#v", err)
		}
	}
	t.Run("newer report differs", func(t *testing.T) {
		a := proThree()
		a.AfterAAP = func(pdu []byte) {
			if pdu[4] == 0x63 {
				a.Report(7, sim.EQReport(false, 42, 50, 71))
			}
		}
		client, _ := rig(t, a)
		_, err := ExperimentalSetEQ(client, eqBaseline, requested)
		unconfirmedErr(t, err)
		if len(a.Received) != 1 {
			t.Errorf("sent %x", a.Received)
		}
	})
	t.Run("no new report", func(t *testing.T) {
		a := proThree()
		a.SilentSlots = map[byte]bool{7: true}
		client, _ := rig(t, a)
		start := client.Clock().Now()
		_, err := ExperimentalSetEQ(client, eqBaseline, requested)
		unconfirmedErr(t, err)
		if waited := client.Clock().Now().Sub(start); waited < confirmLimit || waited > confirmLimit+time.Second {
			t.Errorf("waited %v", waited)
		}
		if len(a.Received) != 1 {
			t.Errorf("sent %x", a.Received)
		}
	})
	t.Run("lost device", func(t *testing.T) {
		a := proThree()
		a.SilentSlots = map[byte]bool{7: true}
		client, _ := rig(t, a)
		change, err := ExperimentalStartSetEQ(client, eqBaseline, requested)
		if err != nil {
			t.Fatal(err)
		}
		_, done, err := change.Absent(errors.New("no dongle"))
		if !done {
			t.Fatal("not done")
		}
		unconfirmedErr(t, err)
	})
}

func TestEQRefusals(t *testing.T) {
	report := func(packet []byte) func(*sim.HearingAirPods) {
		return func(a *sim.HearingAirPods) { a.Slots[7] = sim.CachedReport{Revision: 1, Packet: packet} }
	}
	for name, change := range map[string]func(*sim.HearingAirPods){
		"no report":    func(a *sim.HearingAirPods) { delete(a.Slots, 7) },
		"state 0":      report(append(sim.EQReport(false, 30, 50, 70)[:9], 0, 30, 50, 70)),
		"band 101":     report(sim.EQReport(false, 101, 50, 70)),
		"short report": report(sim.EQReport(false, 30, 50, 70)[:12]),
	} {
		t.Run(name, func(t *testing.T) {
			a := proThree()
			change(a)
			client, _ := rig(t, a)
			_, err := ExperimentalReadEQ(client)
			var r *Refused
			if !errors.As(err, &r) {
				t.Errorf("read: %v", err)
			}
			_, err = ExperimentalSetEQ(client, eqBaseline, EQ{On: true, Low: 30, Mid: 50, High: 70})
			if !errors.As(err, &r) {
				t.Errorf("set: %v", err)
			}
			if len(a.Received) != 0 {
				t.Errorf("sent %x", a.Received)
			}
		})
	}
	t.Run("out of range", func(t *testing.T) {
		client, _ := rig(t, proThree())
		_, err := ExperimentalSetEQ(client, eqBaseline, EQ{Low: 101})
		refused(t, err, "between 0 and 100")
	})
	t.Run("stale expected", func(t *testing.T) {
		a := proThree()
		client, _ := rig(t, a)
		_, err := ExperimentalSetEQ(client, EQ{Low: 31, Mid: 50, High: 70}, EQ{Low: 42, Mid: 50, High: 70})
		refused(t, err, "changed")
		if len(a.Received) != 0 {
			t.Errorf("sent %x", a.Received)
		}
	})
	// The dongle passes EQ on only for a model it knows; its code 6 means nothing was sent.
	t.Run("model the dongle does not know", func(t *testing.T) {
		a := sim.NewHearingAirPods("A9999", "9A348")
		client, _ := rig(t, a)
		_, err := ExperimentalSetEQ(client, eqBaseline, EQ{Low: 42, Mid: 50, High: 70})
		refused(t, err, "no current EQ report from these AirPods")
		var rejected *dongle.RequestRejected
		if !errors.As(err, &rejected) || rejected.Code != 6 {
			t.Errorf("cause %v", err)
		}
		if len(a.Received) != 0 {
			t.Errorf("sent %x", a.Received)
		}
	})
	t.Run("dongle without the EQ slot", func(t *testing.T) {
		_, _, err := readEQ(slotLink{err: &dongle.Unsupported{Message: "unsupported"}})
		refused(t, err, "0.1.64")
	})
}

// slotLink answers everything with err.
type slotLink struct{ err error }

func (l slotLink) Exchange([]byte) ([]byte, error)     { return nil, l.err }
func (l slotLink) SendAAP([]byte) error                { return l.err }
func (l slotLink) Cached(byte) (*dongle.Cached, error) { return nil, l.err }

// The dongle's fixed packet list has no rename packet, so it refuses one with code 6 before
// sending anything. That is a refusal, not a send that may have arrived.
func TestUnlistedPacketsAreRefusedNotLost(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
	_, err := ExperimentalRename(client, "Alex")
	refused(t, err, "Nothing was sent")
	if len(a.Received) != 0 {
		t.Errorf("received %x", a.Received)
	}
}

func TestRenameGolden(t *testing.T) {
	a := proThree()
	client, _ := unlisted(t, a)
	result, err := ExperimentalRename(client, "Alex")
	if err != nil || result.Status != StatusSent {
		t.Fatalf("%+v %v", result, err)
	}
	samePackets(t, a.Received, [][]byte{{4, 0, 4, 0, 0x1a, 0, 1, 4, 0, 'A', 'l', 'e', 'x'}})

	a.Received = nil
	name := strings.Repeat("a", 64)
	if _, err := ExperimentalRename(client, name); err != nil {
		t.Fatal(err)
	}
	if len(a.Received[0]) != 9+64 || a.Received[0][7] != 64 {
		t.Errorf("%x", a.Received[0])
	}
	a.Received = nil
	if _, err := ExperimentalRename(client, "Müller"); err != nil {
		t.Fatal(err)
	}
	if a.Received[0][7] != 7 {
		t.Errorf("the size counts bytes: %d", a.Received[0][7])
	}
}

func TestRenameRefusals(t *testing.T) {
	for name, value := range map[string]string{
		"empty":   "",
		"blank":   "   ",
		"long":    strings.Repeat("a", 65),
		"bytes":   strings.Repeat("ü", 33),
		"control": "a\x00b",
		"newline": "a\nb",
		"invalid": "a\xffb",
	} {
		a := proThree()
		client, _ := rig(t, a)
		_, err := ExperimentalRename(client, value)
		refused(t, err, "UTF-8")
		if len(a.Received) != 0 {
			t.Errorf("%s sent", name)
		}
	}
}

func TestSendsReportTheDongleRefusing(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	dev.Update(func(c *sim.Config) { c.Fail = map[byte]byte{0: 7} })
	_, err := ExperimentalRename(client, "Alex")
	var unsupported *dongle.Unsupported
	if !errors.As(err, &unsupported) {
		t.Errorf("rename: %#v", err)
	}
	dev.Update(func(c *sim.Config) { c.Fail = nil; c.SetFailAt = 0 })
	dev.SetFlags(0x13)
	_, err = ExperimentalSetEQ(client, eqBaseline, EQ{On: true, Low: 30, Mid: 50, High: 70})
	refused(t, err, "recording")
	_, err = ExperimentalRename(client, "Alex")
	refused(t, err, "recording")
}

func TestLostSendIsUnconfirmed(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	dev.Update(func(c *sim.Config) { c.Fail = map[byte]byte{0: 5} })
	_, err := ExperimentalRename(client, "Alex")
	var u *dongle.Unconfirmed
	if !errors.As(err, &u) || !strings.Contains(err.Error(), "may or may not have arrived") {
		t.Errorf("err %#v", err)
	}
	// A transport failure of a listed packet can also be code 6, so that stays uncertain.
	if err := sendFailure("the packet", &dongle.RequestRejected{Code: 6}); !errors.As(err, &u) {
		t.Errorf("listed packet: %#v", err)
	}
}

// While hearing assistance is active, Custom Transparency may only be turned off. Every other
// change is refused before anything is sent.
func TestTransparencyEditsWaitForHearingAssistanceOff(t *testing.T) {
	voice := func(enrollment byte) []byte {
		pme := sim.PMERecord()
		pme[9], pme[10] = enrollment, 1
		return pme
	}
	active := map[string]func(*sim.HearingAirPods){
		"hearing aid on": func(a *sim.HearingAirPods) {
			a.Slots[2] = sim.CachedReport{Revision: 1, Packet: hearingOn}
			a.Slots[3] = sim.CachedReport{Revision: 1, Packet: assistOn}
		},
		"gate on": func(a *sim.HearingAirPods) { a.Slots[3] = sim.CachedReport{Revision: 1, Packet: assistOn} },
		// The gate alone is covered above; this is media assist with its curves in use.
		"media assist on": func(a *sim.HearingAirPods) { a.Slots[4] = sim.CachedReport{Revision: 1, Packet: voice(1)} },
		"no gate report":  func(a *sim.HearingAirPods) { delete(a.Slots, 3) },
	}
	for name, setup := range active {
		for _, edit := range []string{"tone", "enable", "off and tone"} {
			t.Run(name+"/"+edit, func(t *testing.T) {
				a := proThree()
				if edit == "off and tone" {
					a.Server.Find(sim.UUIDTransparency).Value = sim.TransparencyRecord(true)
				}
				setup(a)
				client, dev := rig(t, a)
				modes := new(Modes)
				expected, err := modes.ExperimentalReadTransparency(client)
				if err != nil {
					t.Fatal(err)
				}
				requested := expected
				switch edit {
				case "tone":
					requested.RightTone = 0.25
				case "enable":
					requested.Enabled = true
				default:
					requested.Enabled, requested.RightTone = false, 0.25
				}
				_, err = modes.ExperimentalWriteTransparency(client, expected, requested)
				refused(t, err, "must be reported off")
				if a.Server.Writes != 0 || len(a.Received) != 0 {
					t.Errorf("%d writes, sent %x", a.Server.Writes, a.Received)
				}
				for _, pdu := range dev.Exchanges() {
					if pdu[0] == attWrite || pdu[0] == attCommand {
						t.Errorf("write sent: %x", pdu)
					}
				}
			})
		}
	}
	t.Run("turning it off is allowed", func(t *testing.T) {
		for name, setup := range active {
			a := proThree()
			a.Server.Find(sim.UUIDTransparency).Value = sim.TransparencyRecord(true)
			setup(a)
			client, _ := rig(t, a)
			modes := new(Modes)
			expected, err := modes.ExperimentalReadTransparency(client)
			if err != nil {
				t.Fatal(err)
			}
			requested := expected
			requested.Enabled = false
			if got, err := modes.ExperimentalWriteTransparency(client, expected, requested); err != nil || got.Enabled {
				t.Errorf("%s: %+v %v", name, got, err)
			}
		}
	})
	t.Run("media assist without curves in use", func(t *testing.T) {
		for _, enrollment := range []byte{0, 2} {
			a := proThree()
			a.Slots[4] = sim.CachedReport{Revision: 1, Packet: voice(enrollment)}
			client, _ := rig(t, a)
			modes := new(Modes)
			expected, err := modes.ExperimentalReadTransparency(client)
			if err != nil {
				t.Fatal(err)
			}
			requested := expected
			requested.RightTone = 0.25
			if _, err := modes.ExperimentalWriteTransparency(client, expected, requested); err != nil {
				t.Errorf("enrollment %d: %v", enrollment, err)
			}
		}
	})
}

// A read must return 1 to 510 bytes; anything else is an error and nothing is written.
func TestReadValueLengthBounds(t *testing.T) {
	for name, value := range map[string][]byte{"empty": {}, "511 bytes": make([]byte, 511)} {
		t.Run(name, func(t *testing.T) {
			a := proThree()
			a.Server.Find(sim.UUIDLoudSound).Value = value
			a.Server.Find(sim.UUIDTransparency).Value = value
			client, _ := rig(t, a)
			if _, err := ExperimentalReadLoudSoundReduction(client); err == nil || !strings.Contains(err.Error(), "bytes") {
				t.Errorf("loud sound: %v", err)
			}
			if _, err := ExperimentalSetLoudSoundReduction(client, false, true); err == nil {
				t.Error("loud sound write")
			}
			if _, err := new(Modes).ExperimentalReadTransparency(client); err == nil || !strings.Contains(err.Error(), "bytes") {
				t.Errorf("transparency: %v", err)
			}
			if a.Server.Writes != 0 {
				t.Error("wrote")
			}
		})
	}
	a := proThree()
	a.Server.Find(sim.UUIDTransparency).Value = make([]byte, 510)
	client, _ := rig(t, a)
	if _, err := new(Modes).ExperimentalReadTransparency(client); err == nil || strings.Contains(err.Error(), "bytes") {
		t.Errorf("510 bytes is a valid read, rejected only as a record: %v", err)
	}
}
