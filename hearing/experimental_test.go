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
			func(s *TransparencySettings) { s.LeftAmplification = 1.5 },
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
	exchanges := dev.Exchanges()
	want := [][]byte{{0x0a, 0x1b, 0}, {0x0a, 0x1b, 0}, {0x12, 0x1b, 0, 1}, {0x0a, 0x1b, 0}}
	samePackets(t, exchanges, want)

	if _, err := ExperimentalSetLoudSoundReduction(client, false, false); err == nil {
		t.Error("a stale expected value was accepted")
	} else {
		refused(t, err, "changed")
	}
	writes := a.Server.Writes
	if _, err := ExperimentalSetLoudSoundReduction(client, true, true); err != nil || a.Server.Writes != writes {
		t.Errorf("no change: %v, %d writes", err, a.Server.Writes)
	}

	other := sim.NewHearingAirPods("A2564", "9A348")
	client, _ = rig(t, other)
	_, err = ExperimentalReadLoudSoundReduction(client)
	refused(t, err, "not supported")
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
		a := proThree()
		a.Server.Find(sim.UUIDLoudSound).Value = []byte{0, 0}
		client, _ := rig(t, a)
		if _, err := ExperimentalSetLoudSoundReduction(client, false, true); err == nil || a.Server.Writes != 0 {
			t.Errorf("%v, %d writes", err, a.Server.Writes)
		}
	})
	t.Run("did not stick", func(t *testing.T) {
		a := proThree()
		attribute := a.Server.Find(sim.UUIDLoudSound)
		attribute.OnWrite = func([]byte) { attribute.Value = []byte{0} }
		client, _ := rig(t, a)
		after, err := ExperimentalSetLoudSoundReduction(client, false, true)
		var u *dongle.Unconfirmed
		if !errors.As(err, &u) || after {
			t.Errorf("%v %v", after, err)
		}
		if a.Server.Writes != 1 {
			t.Errorf("writes %d", a.Server.Writes)
		}
	})
	t.Run("refused", func(t *testing.T) {
		a := proThree()
		a.BeforeExchange = func(pdu []byte) { a.Server.RefuseWrites = pdu[0] == attWrite }
		client, _ := rig(t, a)
		_, err := ExperimentalSetLoudSoundReduction(client, false, true)
		var u *dongle.Unconfirmed
		if !errors.As(err, &u) {
			t.Errorf("err %#v", err)
		}
	})
}

func TestEQGoldenAndNeverConfirmed(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
	result, err := ExperimentalSendEQ(client, EQ{Mode: 2, Low: 10, Mid: 50, High: 100})
	if err != nil {
		t.Fatal(err)
	}
	samePackets(t, a.Received, [][]byte{{4, 0, 4, 0, 0x63, 0, 5, 0, 1, 2, 10, 50, 100}})
	if result.Status != "sent" || strings.Contains(strings.ToLower(result.Note), "confirmed") {
		t.Errorf("%+v", result)
	}
	if a.Server.Writes != 0 {
		t.Error("an AAP send touched an attribute")
	}
}

func TestEQGates(t *testing.T) {
	for name, eq := range map[string]EQ{"mode 0": {Mode: 0}, "mode 3": {Mode: 3}, "low": {Mode: 1, Low: 101}, "mid": {Mode: 1, Mid: 101}, "high": {Mode: 1, High: 255}} {
		a := proThree()
		client, _ := rig(t, a)
		_, err := ExperimentalSendEQ(client, eq)
		refused(t, err, "between 0 and 100")
		if len(a.Received) != 0 {
			t.Errorf("%s sent", name)
		}
	}
	a := sim.NewHearingAirPods("A3063", "8A358")
	client, _ := rig(t, a)
	_, err := ExperimentalSendEQ(client, EQ{Mode: 1})
	refused(t, err, "firmware 9")
	a = sim.NewHearingAirPods("A2564", "9A348")
	client, _ = rig(t, a)
	_, err = ExperimentalSendEQ(client, EQ{Mode: 1})
	refused(t, err, "not supported")
	if len(a.Received) != 0 {
		t.Error("sent")
	}
}

func TestRenameGolden(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
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
	_, err = ExperimentalSendEQ(client, EQ{Mode: 1})
	refused(t, err, "recording")
	_, err = ExperimentalRename(client, "Alex")
	refused(t, err, "recording")
}

func TestLostSendIsUnconfirmed(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	dev.Update(func(c *sim.Config) { c.Fail = map[byte]byte{0: 6} })
	_, err := ExperimentalRename(client, "Alex")
	var u *dongle.Unconfirmed
	if !errors.As(err, &u) {
		t.Errorf("err %#v", err)
	}
}
