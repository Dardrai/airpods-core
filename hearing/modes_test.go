package hearing

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/dongle/sim"
)

func rig(t *testing.T, a *sim.HearingAirPods) (*dongle.Client, *sim.Device) {
	t.Helper()
	dev := sim.New(a.Config())
	client := dongle.NewClient(dev)
	client.SetClock(sim.NewVirtualClock())
	return client, dev
}

func refused(t *testing.T, err error, want string) {
	t.Helper()
	var r *Refused
	if !errors.As(err, &r) || !strings.Contains(r.Reason, want) {
		t.Fatalf("got %v, want a refusal mentioning %q", err, want)
	}
}

func proThree() *sim.HearingAirPods { return sim.NewHearingAirPods("A3063", "9A348") }

var (
	assistOn   = []byte{4, 0, 4, 0, 9, 0, 0x33, 1, 0, 0, 0}
	assistOff  = []byte{4, 0, 4, 0, 9, 0, 0x33, 2, 0, 0, 0}
	hearingOn  = []byte{4, 0, 4, 0, 9, 0, 0x2c, 1, 1, 0, 0}
	hearingOff = []byte{4, 0, 4, 0, 9, 0, 0x2c, 1, 2, 0, 0}
	ancClean   = []byte{4, 0, 4, 0, 9, 0, 0x0d, 3, 0, 0, 0}
	askReports = []byte{4, 0, 4, 0, 0x0f, 0, 0xff, 0xff, 0xff, 0xff}
)

func samePackets(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d packets %x, want %d %x", len(got), got, len(want), want)
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("packet %d: %x, want %x", i, got[i], want[i])
		}
	}
}

func TestEnableHearingAidGoldenPacketsAndConfirmation(t *testing.T) {
	a := proThree()
	client, dev := rig(t, a)
	modes := new(Modes)
	state, err := modes.Read(client)
	if err != nil {
		t.Fatal(err)
	}
	if state.HearingEnabled != FlagOff || state.HearingEnrolled != FlagOn || state.Media != FlagOff {
		t.Fatalf("state %+v", state)
	}
	result, err := modes.Set(client, HearingAid, true)
	if err != nil {
		t.Fatal(err)
	}
	samePackets(t, a.Received, [][]byte{assistOn, hearingOn, ancClean, askReports})
	if result.State.HearingEnabled != FlagOn || result.Profile == nil || !result.Sent {
		t.Errorf("result %+v", result)
	}
	if a.Server.Writes != 0 {
		t.Error("changing a mode wrote an attribute")
	}
	if got := strings.Join(dev.LeaseEvents(), " "); strings.Count(got, "acquire") != strings.Count(got, "release") {
		t.Errorf("every lease must be given back: %s", got)
	}
	if dev.LeaseHeld() {
		t.Error("lease kept")
	}
}

func TestMediaAssistClonesOnlyTheTwoFlags(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
	modes := new(Modes)
	if _, err := modes.Set(client, MediaAssist, true); err != nil {
		t.Fatal(err)
	}
	on := sim.PMERecord()
	on[10], on[11] = 1, 1
	samePackets(t, a.Received, [][]byte{assistOn, on, askReports})
	a.Received = nil
	result, err := modes.Set(client, MediaAssist, false)
	if err != nil {
		t.Fatal(err)
	}
	samePackets(t, a.Received, [][]byte{assistOff, sim.PMERecord(), askReports})
	if result.State.Media != FlagOff {
		t.Errorf("state %+v", result.State)
	}
}

func TestGateStaysOnWhileTheOtherModeNeedsIt(t *testing.T) {
	a := proThree()
	client, _ := rig(t, a)
	modes := new(Modes)
	if _, err := modes.Set(client, HearingAid, true); err != nil {
		t.Fatal(err)
	}
	a.Received = nil
	if _, err := modes.Set(client, MediaAssist, true); err != nil {
		t.Fatal(err)
	}
	on := sim.PMERecord()
	on[10], on[11] = 1, 1
	samePackets(t, a.Received, [][]byte{on, askReports})
	a.Received = nil
	if _, err := modes.Set(client, HearingAid, false); err != nil {
		t.Fatal(err)
	}
	samePackets(t, a.Received, [][]byte{hearingOff, askReports})
}

func TestEnableRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(a *sim.HearingAirPods)
		want  string
	}{
		{"not enrolled", func(a *sim.HearingAirPods) {
			a.Slots[2] = sim.CachedReport{Revision: 1, Packet: []byte{4, 0, 4, 0, 9, 0, 0x2c, 2, 0, 0, 0}}
		}, "iPhone or iPad"},
		{"no hearing report", func(a *sim.HearingAirPods) { delete(a.Slots, 2) }, "iPhone or iPad"},
		{"foreign hearing report", func(a *sim.HearingAirPods) { a.Slots[2] = sim.CachedReport{Revision: 1, Packet: assistOn} }, "iPhone or iPad"},
		{"no gate report", func(a *sim.HearingAirPods) { delete(a.Slots, 3) }, "report the other"},
		{"no media report", func(a *sim.HearingAirPods) { delete(a.Slots, 4) }, "report the other"},
		{"short media report", func(a *sim.HearingAirPods) { a.Slots[4] = sim.CachedReport{Revision: 1, Packet: sim.PMERecord()[:139]} }, "report the other"},
		{"disagreeing gate", func(a *sim.HearingAirPods) { a.Slots[3] = sim.CachedReport{Revision: 1, Packet: assistOn} }, "disagree"},
		{"custom transparency on", func(a *sim.HearingAirPods) { a.Server.Find(sim.UUIDTransparency).Value = sim.TransparencyRecord(true) }, "Custom Transparency"},
		{"custom transparency hidden", func(a *sim.HearingAirPods) {
			attributes := a.Server.Attributes[:0]
			for _, attribute := range a.Server.Attributes {
				if attribute.UUID != sim.UUIDTransparency {
					attributes = append(attributes, attribute)
				}
			}
			a.Server.Attributes = attributes
		}, "Could not confirm Custom Transparency"},
		{"no stored profile", func(a *sim.HearingAirPods) {
			attributes := a.Server.Attributes[:0]
			for _, attribute := range a.Server.Attributes {
				if attribute.UUID != sim.UUIDHearing {
					attributes = append(attributes, attribute)
				}
			}
			a.Server.Attributes = attributes
		}, "not available"},
		{"modes change during the checks", func(a *sim.HearingAirPods) {
			a.BeforeExchange = func(pdu []byte) {
				if pdu[0] == attRead {
					a.Slots[3] = sim.CachedReport{Revision: 9, Packet: assistOn}
				}
			}
		}, "moved while"},
	}
	for _, mode := range []Mode{HearingAid, MediaAssist} {
		for _, tc := range cases {
			t.Run(mode.String()+"/"+tc.name, func(t *testing.T) {
				a := proThree()
				tc.setup(a)
				client, _ := rig(t, a)
				_, err := new(Modes).Set(client, mode, true)
				refused(t, err, tc.want)
				if len(a.Received) != 0 {
					t.Errorf("sent %x", a.Received)
				}
			})
		}
	}
}

func TestStoredProfileFormatProblemsStopAnEnable(t *testing.T) {
	a := proThree()
	a.Server.Find(sim.UUIDHearing).Value = sim.HearingRecord()[:103]
	client, _ := rig(t, a)
	if _, err := new(Modes).Set(client, HearingAid, true); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("err %v", err)
	}
	if len(a.Received) != 0 {
		t.Errorf("sent %x", a.Received)
	}
}

func TestModelAndDongleGates(t *testing.T) {
	a := sim.NewHearingAirPods("A2564", "9A348") // AirPods 3 have no hearing assistance.
	client, _ := rig(t, a)
	_, err := new(Modes).Set(client, HearingAid, true)
	refused(t, err, "not supported")

	a = proThree()
	delete(a.Slots, 1)
	client, _ = rig(t, a)
	_, err = new(Modes).Set(client, HearingAid, false)
	refused(t, err, "not supported")

	for name, flags := range map[string]byte{"disconnected": 0, "not ready": 1, "recording": 0x13} {
		a = proThree()
		client, dev := rig(t, a)
		dev.SetFlags(flags)
		_, err = new(Modes).Set(client, HearingAid, true)
		var r *Refused
		if !errors.As(err, &r) {
			t.Errorf("%s: %v", name, err)
		}
		if len(a.Received) != 0 || len(dev.LeaseEvents()) != 0 {
			t.Errorf("%s: touched the device", name)
		}
	}
}

func TestUnconfirmedEnableOffersOffRecoveryAndNeverResends(t *testing.T) {
	a := proThree()
	a.Silent = true
	client, _ := rig(t, a)
	modes := new(Modes)
	_, err := modes.Set(client, HearingAid, true)
	var unconfirmed *ModeUnconfirmed
	if !errors.As(err, &unconfirmed) || unconfirmed.Recovery == nil || unconfirmed.Recovery.Mode != HearingAid {
		t.Fatalf("err %#v", err)
	}
	samePackets(t, a.Received, [][]byte{assistOn, hearingOn, ancClean, askReports})
	if modes.Recovery() == nil || !strings.Contains(modes.Recovery().Text(), "Hearing Aid") {
		t.Fatalf("recovery %+v", modes.Recovery())
	}
	// The reports are invalid now, and a stale one must not stand in for a new one.
	state, err := modes.Read(client)
	if err != nil || state.HearingEnabled != FlagUnknown || state.AssistEnabled != FlagUnknown || state.HearingEnrolled != FlagOn {
		t.Fatalf("state %+v %v", state, err)
	}
	if _, err := modes.Set(client, HearingAid, true); err == nil {
		t.Error("enable without reports")
	}
	// Off needs no report, so a silent pair can still be told to stop.
	a.Received = nil
	a.Silent = false
	result, err := modes.Recover(client)
	if err != nil {
		t.Fatal(err)
	}
	samePackets(t, a.Received, [][]byte{assistOff, hearingOff, askReports})
	if result.State.HearingEnabled != FlagOff || modes.Recovery() != nil {
		t.Errorf("result %+v, recovery %+v", result, modes.Recovery())
	}
	if _, err := modes.Recover(client); err == nil {
		t.Error("nothing left to recover")
	}
}

func TestSameBytesWithAnOldRevisionDoNotConfirm(t *testing.T) {
	a := proThree()
	a.Slots[2] = sim.CachedReport{Revision: 5, Packet: hearingOn}
	a.Slots[3] = sim.CachedReport{Revision: 5, Packet: assistOn}
	a.SilentSlots = map[byte]bool{2: true, 3: true}
	client, _ := rig(t, a)
	modes := new(Modes)
	_, err := modes.Set(client, HearingAid, false)
	var unconfirmed *ModeUnconfirmed
	if !errors.As(err, &unconfirmed) {
		t.Fatalf("err %v", err)
	}
	if unconfirmed.Recovery != nil {
		t.Error("an unconfirmed off that followed no enable has nothing to recover")
	}
}

func TestFenceRules(t *testing.T) {
	modes := new(Modes)
	report := func(revision uint32) *dongle.Cached {
		return &dongle.Cached{Revision: revision, Packet: hearingOn}
	}
	modes.observeSlot(dongle.SlotHearingAid, report(7))
	if modes.cache.state().HearingEnabled != FlagOn {
		t.Fatal("report ignored")
	}
	modes.fences[0], modes.fenced[0] = 7, true
	modes.observeSlot(dongle.SlotHearingAid, report(7))
	if modes.cache.state().HearingEnabled != FlagUnknown {
		t.Error("old revision accepted")
	}
	modes.observeSlot(dongle.SlotHearingAid, nil)
	if modes.cache.state().HearingEnabled != FlagUnknown || modes.cache.hearingTemplate == nil {
		t.Error("a missing report dropped the template while fenced")
	}
	modes.observeSlot(dongle.SlotHearingAid, report(8))
	if modes.cache.state().HearingEnabled != FlagOn || modes.fenced[0] {
		t.Error("new revision refused")
	}
}

func TestEnableStopsWhenTheProfileChanges(t *testing.T) {
	a := proThree()
	reads := 0
	a.BeforeExchange = func(pdu []byte) {
		if pdu[0] == attRead && pdu[1] == 0x1f {
			reads++
			if reads == 2 {
				a.Server.Find(sim.UUIDHearing).Value[6] = 0x30 // 10 dB becomes 11 dB
			}
		}
	}
	client, _ := rig(t, a)
	modes := new(Modes)
	_, err := modes.Set(client, HearingAid, true)
	var unconfirmed *ModeUnconfirmed
	if !errors.As(err, &unconfirmed) || !strings.Contains(err.Error(), "no longer the one") || unconfirmed.Recovery == nil {
		t.Fatalf("err %v", err)
	}
}

func TestNewConnectionForgetsReportsButKeepsTheRecoveryHint(t *testing.T) {
	a := proThree()
	a.Silent = true
	client, dev := rig(t, a)
	modes := new(Modes)
	if _, err := modes.Set(client, HearingAid, true); err == nil {
		t.Fatal("should not confirm")
	}
	dev.BumpGeneration()
	a.Silent = false
	a.Report(2, hearingOn)
	a.Report(3, assistOn)
	state, err := modes.Read(client)
	if err != nil || state.HearingEnabled != FlagOn {
		t.Fatalf("state %+v %v", state, err)
	}
	if modes.Recovery() == nil {
		t.Error("the hint should survive a reconnect of the same AirPods")
	}
	if modes.fenced != [3]bool{} {
		t.Error("old fences kept")
	}
}

func TestSettingsParsing(t *testing.T) {
	settings, err := parseSettings(sim.HearingRecord())
	if err != nil {
		t.Fatal(err)
	}
	if settings.LeftEQ[7] != 17 || settings.RightEQ[0] != 20 || settings.LeftAmplification != 0.25 ||
		settings.RightConversationBoost != true || settings.LeftConversationBoost || settings.OwnVoice != 0.625 {
		t.Errorf("%+v", settings)
	}
	mutate := func(offset int, v float32) []byte {
		value := sim.HearingRecord()
		putFloat(value, offset, v)
		return value
	}
	for name, value := range map[string][]byte{
		"short":        sim.HearingRecord()[:100],
		"curve high":   mutate(offLeftEQ, 91),
		"curve low":    mutate(offRightEQ, -1),
		"gain":         mutate(offLeftAmp, 1.6),
		"tone":         mutate(offRightTone, 1.5),
		"conversation": mutate(offLeftConv, 0.5),
		"own voice":    mutate(offOwnVoice, 2),
		"version":      append([]byte{3}, sim.HearingRecord()[1:]...),
	} {
		if _, err := parseSettings(value); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	readOnlyRecord := sim.HearingRecord()
	readOnlyRecord[1] = 0
	if _, err := parseSettings(readOnlyRecord); err != nil {
		t.Error(err)
	}
	if editableValue(readOnlyRecord, Characteristic{WriteOpcode: attWrite}) {
		t.Error("an older record is read-only")
	}
	if editableValue(sim.HearingRecord(), Characteristic{}) {
		t.Error("no write property")
	}
}

func TestUUIDWireOrder(t *testing.T) {
	if hearingUUID != UUID(sim.UUIDHearing) || transparencyUUID != UUID(sim.UUIDTransparency) {
		t.Error("wire order differs")
	}
	want := []byte{0x90, 0x53, 0x4d, 0x74, 0xaf, 0x9e, 0x03, 0x94, 0xdb, 0x45, 0xab, 0xf7, 0xc1, 0x1c, 0x62, 0xd5}
	if !bytes.Equal(transparencyUUID[:], want) {
		t.Errorf("transparency %x", transparencyUUID)
	}
}
