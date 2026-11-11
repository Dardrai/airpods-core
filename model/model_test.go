package model

import "testing"

func view(model, firmware string, reported map[byte]uint32) View {
	if reported == nil {
		reported = map[byte]uint32{0x0d: 2, 0x1a: 6, 0x28: 1, 0x0a: 1}
	}
	return View{Model: model, Firmware: firmware, Reported: reported}
}

func TestFindIgnoresCaseAndSpace(t *testing.T) {
	if p, ok := Find(" a3065 "); !ok || p.Name != "AirPods Pro 3" {
		t.Errorf("%+v %v", p, ok)
	}
	if _, ok := Find("future"); ok {
		t.Error("unknown found")
	}
}

func TestProOneHasThreeModesAndNoCycleAdaptive(t *testing.T) {
	v := view("A2083", "6F21", nil)
	for mode := uint32(1); mode <= 3; mode++ {
		if !v.ValueAllowed(ControlMode, mode) {
			t.Errorf("mode %d", mode)
		}
	}
	if v.ValueAllowed(ControlMode, 4) || !v.ValueAllowed(ControlCycle, 7) || v.ValueAllowed(ControlCycle, 14) {
		t.Error("adaptive must be out")
	}
	if v.SupportsControl(0x28) {
		t.Error("no conversation awareness on this model")
	}
}

func TestAdaptiveNeedsModelAndFirmware(t *testing.T) {
	if !view("A3063", "9A348", nil).Adaptive() {
		t.Error("pro 3")
	}
	if view("A3063", "5A1", nil).Adaptive() || view("A3063", "6619472", nil).Adaptive() {
		t.Error("old or internal firmware")
	}
	if view("A2096", "9A1", nil).Adaptive() {
		t.Error("original Max")
	}
}

func TestNonListeningModels(t *testing.T) {
	v := view("A2564", "6A1", map[byte]uint32{})
	if v.Listening() || v.OffAvailable() {
		t.Error("no modes on AirPods 3")
	}
	if v.ValueAllowed(ControlHold, 0x0105) || v.ValueAllowed(ControlHold, 0x0501) || !v.ValueAllowed(ControlHold, 0x0505) {
		t.Error("no noise-control hold on non-listening models")
	}
	if v.SupportsControl(ControlMode) || !v.SupportsControl(0x0a) {
		t.Error("controls")
	}
}

func TestOffAvailable(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		view View
		want bool
	}{
		{"no option, allow-off not refused", view("A2084", "6A1", map[byte]uint32{0x0d: 2}), true},
		{"no option, refused", view("A2084", "6A1", map[byte]uint32{0x34: 2}), false},
		{"reported on", view("A3063", "9A348", map[byte]uint32{0x34: 1}), true},
		{"reported off", view("A3063", "9A348", map[byte]uint32{0x34: 2}), false},
		{"requested wins", View{Model: "A3063", Firmware: "9A348", Reported: map[byte]uint32{0x34: 1}, AllowOffRequested: &no}, false},
		{"requested on", View{Model: "A3063", Firmware: "9A348", AllowOffRequested: &yes}, true},
		{"old firmware", view("A2698", "6A1", map[byte]uint32{}), true},
		{"in Off now", view("A3063", "9A348", map[byte]uint32{0x0d: 1}), true},
		{"nothing known", view("A3063", "9A348", map[byte]uint32{0x0d: 2}), false},
		{"non listening", view("A2564", "6A1", map[byte]uint32{0x0d: 1}), false},
	}
	for _, c := range cases {
		if got := c.view.OffAvailable(); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func TestCycleMaskFollowsAvailableModes(t *testing.T) {
	v := view("A3063", "9A348", map[byte]uint32{0x34: 2})
	if v.ValueAllowed(ControlCycle, 0b0111) {
		t.Error("off bit without Allow Off")
	}
	if !v.ValueAllowed(ControlCycle, 0b1110) {
		t.Error("anc, transparency, adaptive")
	}
	if view("A2096", "9A1", map[byte]uint32{}).ValueAllowed(ControlCycle, 0b1100) {
		t.Error("adaptive bit on a model without it")
	}
}

func TestSleepControlNeedsFirmwareEight(t *testing.T) {
	if view("A3063", "7A1", nil).SupportsControl(0x35) || !view("A3063", "8A1", nil).SupportsControl(0x35) {
		t.Error("firmware gate")
	}
}

func TestUnlistedModelNeedsAReport(t *testing.T) {
	v := view("future", "9A348", nil)
	if !v.SupportsControl(0x0a) || !v.ValueAllowed(ControlMode, 2) || v.ValueAllowed(ControlMode, 4) {
		t.Error("reported controls stay")
	}
	if v.SupportsControl(0x01) || v.CanChoose(0x01) {
		t.Error("unreported control on an unlisted model")
	}
	if v.Headphone() {
		t.Error("headphone")
	}
}

func TestCanChoose(t *testing.T) {
	pro3 := view("A3063", "9A348", map[byte]uint32{})
	for _, control := range []byte{0x01, 0x0a, 0x16, 0x1a, 0x23, 0x31, 0x40, 0x34} {
		if !pro3.CanChoose(control) {
			t.Errorf("0x%02x on Pro 3", control)
		}
	}
	if pro3.CanChoose(0x1c) || pro3.CanChoose(0x17) {
		t.Error("needs a report")
	}
	if view("A2698", "9A348", map[byte]uint32{}).CanChoose(0x34) {
		t.Error("allow off without a report is only for Pro 3")
	}
	if !(View{}).CanChoose(0x0a) || (View{}).CanChoose(0x34) {
		t.Error("before identification only the explicit list")
	}
	if view("A2096", "9A1", map[byte]uint32{}).CanChoose(0x01) {
		t.Error("Max has no microphone choice")
	}
}

func TestHeadphoneOnlyOnMax(t *testing.T) {
	if !view("A3184", "7A1", nil).Headphone() || view("A3063", "9A348", nil).Headphone() {
		t.Error("headphone")
	}
}

func TestNeedsAllowOffResend(t *testing.T) {
	pro3 := view("A3063", "9A348", map[byte]uint32{0x0d: 2})
	for _, tc := range []struct {
		name    string
		v       View
		control byte
		value   uint32
		sent    bool
		want    bool
	}{
		{"mode off", pro3, ControlMode, 1, false, true},
		{"cycle with off", pro3, ControlCycle, 7, false, true},
		{"already sent", pro3, ControlMode, 1, true, false},
		{"mode anc", pro3, ControlMode, 2, false, false},
		{"cycle without off", pro3, ControlCycle, 6, false, false},
		{"other control", pro3, 0x28, 1, false, false},
		{"pro 2 reports it", view("A3048", "9A348", map[byte]uint32{0x0d: 2, 0x34: 1}), ControlMode, 1, false, false},
		{"silent model reported anyway", view("A3063", "9A348", map[byte]uint32{0x34: 1}), ControlMode, 1, false, false},
		{"mode already off", view("A3063", "9A348", map[byte]uint32{0x0d: 1}), ControlCycle, 3, false, false},
		{"old firmware", view("A3063", "6A321", map[byte]uint32{0x0d: 2}), ControlMode, 1, false, false},
		{"unidentified", View{}, ControlMode, 1, false, false},
	} {
		if got := tc.v.NeedsAllowOffResend(tc.control, tc.value, tc.sent); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// Gain Swipe (0x2f) exists on AirPods Pro 2 and 3, EN 352 protection (0x37) on Pro 3 only, as
// the dongle's model masks say. Both need a report before they can be written.
func TestPro2AndPro3Controls(t *testing.T) {
	for _, model := range []string{"A2931", "A2699", "A2698", "A3047", "A3048", "A3049", "A3063", "A3064", "A3065"} {
		p, _ := Find(model)
		pro3 := p.Family == familyPro3
		if !p.HasControl(0x2f) || p.HasControl(0x37) != pro3 {
			t.Errorf("%s: gain swipe %v, en352 %v", model, p.HasControl(0x2f), p.HasControl(0x37))
		}
		if v := view(model, "9A348", map[byte]uint32{}); v.CanChoose(0x2f) || v.CanChoose(0x37) {
			t.Errorf("%s: chosen without a report", model)
		}
	}
	for i := range profiles {
		if p := &profiles[i]; p.Family != familyPro2 && p.Family != familyPro3 && (p.HasControl(0x2f) || p.HasControl(0x37)) {
			t.Errorf("%s lists a Pro control", p.Name)
		}
	}
	if !view("A3063", "9A348", map[byte]uint32{0x37: 1}).SupportsControl(0x37) || view("A2931", "9A348", map[byte]uint32{0x37: 1}).SupportsControl(0x37) {
		t.Error("en352 support")
	}
	if view("future", "9A348", map[byte]uint32{0x2f: 1}).SupportsControl(0x2f) {
		t.Error("an unlisted model never shows gain swipe")
	}
}
