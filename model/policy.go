package model

import "github.com/Dardrai/airpods-core/dongle"

// Controls the gates refer to by name.
const (
	ControlMode     byte = 0x0d
	ControlAllowOff byte = 0x34
	ControlStrength byte = 0x2e
	ControlHold     byte = 0x16
	ControlCycle    byte = 0x1a
	controlSleep    byte = 0x35
)

// View is what the gates look at: the reported identity and settings.
type View struct {
	Model    string
	Firmware string
	// Reported holds the settings the AirPods have reported, by control.
	Reported map[byte]uint32
	// AllowOffRequested is the Allow Off value last sent, when it has no report.
	AllowOffRequested *bool
}

func (v View) profile() (*Profile, bool) { return Find(v.Model) }

func (v View) major() int { return dongle.FirmwareMajor(v.Firmware) }

// Identified reports whether the AirPods have said what model they are, known or not.
func (v View) Identified() bool { return v.Model != "" }

// Listening reports whether the AirPods have noise control modes. An unlisted model
// proves it by reporting a mode.
func (v View) Listening() bool {
	if p, ok := v.profile(); ok {
		return p.Listening
	}
	mode, reported := v.Reported[ControlMode]
	return reported && mode >= 1 && mode <= 3
}

// Adaptive reports whether Adaptive mode exists on this model and firmware.
func (v View) Adaptive() bool {
	p, ok := v.profile()
	return ok && p.Adaptive && v.major() >= 6
}

// Headphone reports over-ear models, which name some controls differently.
func (v View) Headphone() bool {
	p, ok := v.profile()
	return ok && p.Headphone
}

// AllowOffOption reports whether the Allow Off setting exists here.
func (v View) AllowOffOption() bool {
	p, ok := v.profile()
	if !ok || !p.AllowOffOption {
		return false
	}
	_, reported := v.Reported[ControlAllowOff]
	return p.Family == familyPro3 || v.major() >= 7 || reported
}

// OffAvailable reports whether Off may be offered as a listening mode.
func (v View) OffAvailable() bool {
	if !v.Listening() {
		return false
	}
	p, known := v.profile()
	if known && !p.AllowOffOption {
		return v.Reported[ControlAllowOff] != 2
	}
	if v.AllowOffRequested != nil {
		return *v.AllowOffRequested
	}
	if value, reported := v.Reported[ControlAllowOff]; reported && (value == 1 || value == 2) {
		return value == 1
	}
	if major := v.major(); known && major > 0 && major <= 6 {
		return true
	}
	return v.Reported[ControlMode] == 1
}

// SilentAllowOff reports models that apply Allow Off without echoing it.
func SilentAllowOff(model string) bool {
	p, ok := Find(model)
	return ok && p.Family == familyPro3
}

// fallbackControl lists what an unlisted model may show once it reports the value.
func fallbackControl(control byte) bool {
	switch control {
	case 0x01, 0x0a, 0x16, 0x17, 0x18, 0x1a, 0x1b, 0x1f, 0x23, 0x24, 0x31, 0x3b, 0x40:
		return true
	}
	return false
}

// SupportsControl decides whether a setting belongs on screen. Before the AirPods
// have identified themselves nothing is hidden, since that may never happen.
func (v View) SupportsControl(control byte) bool {
	if !v.Identified() {
		return true
	}
	p, known := v.profile()
	if !known {
		_, reported := v.Reported[control]
		return (fallbackControl(control) || control == ControlMode) && reported
	}
	if !p.HasControl(control) {
		return false
	}
	if control == ControlAllowOff && !v.AllowOffOption() {
		return false
	}
	return control != controlSleep || v.major() >= 8
}

// ExplicitControl lists settings that can be chosen without a report to start from.
func ExplicitControl(control byte) bool {
	switch control {
	case 0x01, 0x0a, 0x16, 0x1a, 0x23, 0x31, 0x40:
		return true
	}
	return false
}

// CanChoose reports whether a setting without a report may still be written.
func (v View) CanChoose(control byte) bool {
	if !ExplicitControl(control) && (control != ControlAllowOff || !SilentAllowOff(v.Model)) {
		return false
	}
	if !v.Identified() {
		return ExplicitControl(control)
	}
	p, known := v.profile()
	return known && p.HasControl(control) && v.SupportsControl(control)
}

// ValueAllowed applies the value-level rules to one write: modes and cycle bits the
// model lacks, and a hold action it cannot perform.
func (v View) ValueAllowed(control byte, value uint32) bool {
	switch control {
	case ControlHold:
		if p, known := v.profile(); known && !p.Listening && (value&0xff == 1 || value>>8&0xff == 1) {
			return false
		}
	case ControlMode:
		if value == 4 && v.Identified() && !v.Adaptive() {
			return false
		}
		if value == 1 && v.Identified() && !v.OffAvailable() {
			return false
		}
	case ControlCycle:
		if v.Identified() && ((value&8 != 0 && !v.Adaptive()) || (value&1 != 0 && !v.OffAvailable())) {
			return false
		}
	case ControlStrength:
		if v.Identified() && !v.Adaptive() {
			return false
		}
	}
	return true
}

// NeedsAllowOffResend reports whether a write must be preceded by allow-off on (0x34 = 1)
// in this connection generation. The dongle forgets an Allow Off write whenever the AirPods
// connect again, and for models that never report Allow Off it then refuses mode off and
// hold-modes with off as unsupported until Allow Off is sent again. sentThisGeneration says
// whether allow-off on was already sent since the generation began.
func (v View) NeedsAllowOffResend(control byte, value uint32, sentThisGeneration bool) bool {
	wantsOff := (control == ControlMode && value == 1) || (control == ControlCycle && value&1 != 0)
	if !wantsOff || sentThisGeneration || !SilentAllowOff(v.Model) {
		return false
	}
	if _, reported := v.Reported[ControlAllowOff]; reported {
		return false
	}
	// The dongle also allows Off while the AirPods report it, and on firmware before 7.
	if major := v.major(); v.Reported[ControlMode] == 1 || (major >= 1 && major <= 6) {
		return false
	}
	return true
}
