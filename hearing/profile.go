package hearing

import (
	"bytes"
	"errors"
	"time"

	"github.com/Dardrai/airpods-core/dongle"
)

const (
	readbackTries = 5
	readbackWait  = 75 * time.Millisecond
)

// ReadProfile reads the stored hearing profile, which becomes the baseline ApplyProfile
// requires. It writes nothing and does not enable anything.
//
// Gating: model.Profile.HearingAid is enforced here. Callers keep this behind the
// experimental switch. UNVERIFIED on real AirPods. Blocking; run it on the device owner
// goroutine.
func (m *Modes) ReadProfile(c *dongle.Client) (*Profile, error) {
	o, err := m.beginHearing(c)
	if err != nil {
		return nil, err
	}
	defer o.close()
	m.editable = false
	characteristic, value, settings, err := readSettings(o.Exchange)
	if err != nil {
		return nil, err
	}
	m.profile, m.editable = &settings, editableValue(value, characteristic)
	return &Profile{Settings: settings, Editable: m.editable}, nil
}

// prepare reads the stored record again and insists it still matches what the edit began from.
func prepare(exchange Exchanger, expected Settings) (Characteristic, []byte, error) {
	characteristic, value, actual, err := readSettings(exchange)
	if err != nil {
		return Characteristic{}, nil, err
	}
	if !editableValue(value, characteristic) {
		return Characteristic{}, nil, refuse("This hearing profile can be read but not edited.")
	}
	if actual != expected {
		return Characteristic{}, nil, refuse("The hearing profile changed on the AirPods. Read it again before editing.")
	}
	return characteristic, value, nil
}

func (m *Modes) requireBaseline(expected, requested Settings) error {
	if !expected.Valid() || !requested.Valid() {
		return refuse("A complete, valid hearing profile is required.")
	}
	if m.profile == nil || *m.profile != expected || !m.editable {
		return refuse("The hearing profile changed or cannot be edited. Read it again before editing.")
	}
	return nil
}

// PrepareProfile checks that ApplyProfile would start from expected, on a fresh read, without
// changing anything. WillEnable warns that applying would turn Hearing Aid on.
//
// Gating: as for ApplyProfile. Blocking; run it on the device owner goroutine.
func (m *Modes) PrepareProfile(c *dongle.Client, expected Settings) (*PreparedProfile, error) {
	o, err := m.beginHearing(c)
	if err != nil {
		return nil, err
	}
	defer o.close()
	if err := m.requireBaseline(expected, expected); err != nil {
		return nil, err
	}
	if _, _, err := prepare(o.Exchange, expected); err != nil {
		m.editable = false
		return nil, err
	}
	enabled := m.cache.state().HearingEnabled
	if enabled == FlagUnknown {
		return nil, refuse("Wait for the AirPods to report Hearing Aid before editing.")
	}
	return &PreparedProfile{WillEnable: enabled != FlagOn}, nil
}

// PreparedProfile is the outcome of a successful PrepareProfile.
type PreparedProfile struct{ WillEnable bool }

// patchRecord returns the record to write: the baseline's bytes with only the requested
// fields replaced. A conversation boost that does not change keeps its stored bytes, since
// both signs of zero mean off.
func patchRecord(baseline []byte, requested Settings) []byte {
	value := bytes.Clone(baseline)
	for i := range Bands {
		putFloat(value, offLeftEQ+4*i, requested.LeftEQ[i])
		putFloat(value, offRightEQ+4*i, requested.RightEQ[i])
	}
	for offset, v := range map[int]float32{
		offLeftAmp: requested.LeftAmplification, offRightAmp: requested.RightAmplification,
		offLeftTone: requested.LeftTone, offRightTone: requested.RightTone,
		offLeftANR: requested.LeftAmbientNoiseReduction, offRightANR: requested.RightAmbientNoiseReduction,
		offOwnVoice: requested.OwnVoice,
	} {
		putFloat(value, offset, v)
	}
	for offset, on := range map[int]bool{offLeftConv: requested.LeftConversationBoost, offRightConv: requested.RightConversationBoost} {
		if (floatAt(value, offset) == 1) != on {
			v := float32(0)
			if on {
				v = 1
			}
			putFloat(value, offset, v)
		}
	}
	// The writer's header counts the 100 byte prefix, though a read may report 96.
	copy(value, []byte{2, 2, 0x64, 0})
	return value
}

// writeSettings sends the record once, then requires it to read back whole. Everything from
// the moment the write leaves is an Unconfirmed error, because it may have applied.
func writeSettings(exchange Exchanger, clock dongle.Clock, c Characteristic, baseline []byte, requested Settings) (Settings, error) {
	if !requested.Valid() {
		return Settings{}, refuse("A complete, valid hearing profile is required.")
	}
	if _, err := parseSettings(baseline); err != nil {
		return Settings{}, err
	}
	if !editableValue(baseline, c) {
		return Settings{}, refuse("This hearing profile can be read but not edited.")
	}
	written := patchRecord(baseline, requested)
	pdu := append([]byte{c.WriteOpcode, byte(c.Handle), byte(c.Handle >> 8)}, written...)
	response, err := exchange(pdu)
	if err != nil {
		return Settings{}, unconfirmed("No reply to the hearing profile write. It may have applied; read the profile again.", err)
	}
	if c.WriteOpcode == attWrite && !bytes.Equal(response, []byte{attWriteR}) {
		return Settings{}, unconfirmed("The AirPods did not acknowledge the hearing profile write. Read the profile again.", errorText("Hearing profile write.", response))
	}
	for attempt := 1; ; attempt++ {
		value, settings, err := readRecord(exchange, c.Handle)
		if err != nil {
			return Settings{}, unconfirmed("The hearing profile write could not be read back. Read the profile again.", err)
		}
		if value[1] != 2 {
			return Settings{}, unconfirmed("The AirPods reported a different hearing profile format after the write.", nil)
		}
		if bytes.Equal(value[4:], written[4:]) {
			return settings, nil
		}
		// A write may land after the first read, so the old value may still show. Anything
		// else means something else changed it, and that is never accepted or overwritten.
		if !bytes.Equal(value[4:], baseline[4:]) || attempt == readbackTries {
			return Settings{}, unconfirmed("The AirPods did not apply the hearing profile. Read it again before editing.", nil)
		}
		clock.Sleep(readbackWait)
	}
}

// ProfileResult is a confirmed profile write. Settings is what read back.
type ProfileResult struct {
	Settings Settings
	State    ModeState
	// Enabled is true when this call turned Hearing Aid on to make the write. It stays on.
	Enabled bool
}

// ApplyProfile writes a hearing profile, once, as an explicit user action. expected must be
// what ReadProfile returned on this connection; the stored record is read again before the
// write and must still equal it, and the whole record must read back afterwards. Fields the
// profile does not model are kept byte for byte.
//
// The profile can only be written with Hearing Aid on, so if it is off this runs Set's full
// enable, with its enrollment and Custom Transparency checks, and it stays on after success.
// If anything fails after that enable, one attempt is made to turn it off again and the
// error says how that went; the profile write itself is never repeated.
//
// Errors: *Refused means nothing changed. *dongle.Unconfirmed and *ModeUnconfirmed mean the
// outcome is unknown: read again before doing anything else, and offer Modes.Recover when
// Modes.Recovery returns one.
//
// Gating: model.Profile.HearingAid is enforced here. Callers keep this behind the
// experimental switch, since it changes how loud sound is presented. UNVERIFIED on real
// AirPods. Blocking for up to 60 s; run it on the device owner goroutine.
func (m *Modes) ApplyProfile(c *dongle.Client, expected, requested Settings) (*ProfileResult, error) {
	o, err := m.beginHearing(c)
	if err != nil {
		return nil, err
	}
	defer o.close()
	if err := m.requireBaseline(expected, requested); err != nil {
		return nil, err
	}
	// Read before enabling: changing modes must not replace or rewrite the fitting.
	if _, _, err := prepare(o.Exchange, expected); err != nil {
		m.editable = false
		return nil, err
	}
	if err := m.refresh(o); err != nil {
		return nil, err
	}
	was := m.cache.state().HearingEnabled
	if was == FlagUnknown {
		return nil, refuse("Wait for the AirPods to report Hearing Aid before editing.")
	}
	enableStarted := false
	result, err := func() (*ProfileResult, error) {
		if was != FlagOn {
			_, err := m.setMode(o, HearingAid, true)
			var partial *ModeUnconfirmed
			enableStarted = err == nil || errors.As(err, &partial)
			if err != nil {
				return nil, err
			}
		}
		modes := m.cache.state()
		characteristic, baseline, err := prepare(o.Exchange, expected)
		if err != nil {
			return nil, err
		}
		if err := m.refresh(o); err != nil {
			return nil, err
		}
		if state := m.cache.state(); state != modes || state.HearingEnabled != FlagOn {
			return nil, refuse("The hearing modes changed before the profile write. Read the settings again.")
		}
		m.editable = false
		confirmed, err := writeSettings(o.Exchange, o.clock, characteristic, baseline, requested)
		if err != nil {
			return nil, err
		}
		if err := m.refresh(o); err != nil {
			return nil, unconfirmed("The profile was written, but the hearing modes could not be read. Read the settings again.", err)
		}
		if state := m.cache.state(); state != modes {
			return nil, unconfirmed("The profile was written, but the hearing modes changed. Read the settings again.", nil)
		}
		m.profile, m.editable = &confirmed, true
		return &ProfileResult{Settings: confirmed, State: m.cache.state(), Enabled: was != FlagOn}, nil
	}()
	if err == nil {
		return result, nil
	}
	m.editable = false
	if !enableStarted {
		return nil, err
	}
	if checkErr := o.check(); checkErr != nil {
		m.recovery = &Recovery{Mode: HearingAid}
		return nil, &noted{err, "Hearing Aid may still be active: " + checkErr.Error()}
	}
	if _, offErr := m.setMode(o, HearingAid, false); offErr != nil {
		m.recovery = &Recovery{Mode: HearingAid}
		return nil, &noted{err, "Hearing Aid may still be active: " + offErr.Error()}
	}
	m.recovery = nil
	return nil, &noted{err, "Hearing Aid is confirmed off again."}
}

// ProfileEdit is a draft of a profile being edited, with the combined sliders. Nothing here
// talks to the device; pass Baseline and Draft to Modes.ApplyProfile.
//
// Amplification runs 0 to 1 and Balance -1 to 1. Each ear's stored gain is
// amplification*2-1 minus or plus balance*0.5, clamped to -1.5 to 1.5. Stored gains stay
// exact until a gain slider is edited, even when they are outside the sliders' combined range.
type ProfileEdit struct {
	Draft         Settings
	Amplification float32
	Balance       float32
	Dirty         bool
	InvalidCurve  bool
	baseline      Settings
	editable      bool
}

func clamp(v, low, high float32) float32 { return min(max(v, low), high) }

// NewProfileEdit starts a draft from a profile read from the AirPods.
func NewProfileEdit(p Profile) *ProfileEdit {
	s := p.Settings
	return &ProfileEdit{
		Draft:         s,
		baseline:      s,
		editable:      p.Editable,
		Amplification: clamp((s.LeftAmplification+s.RightAmplification+2)/4, 0, 1),
		Balance:       clamp(s.RightAmplification-s.LeftAmplification, -1, 1),
	}
}

// Baseline returns the profile the draft began from.
func (e *ProfileEdit) Baseline() Settings { return e.baseline }

// CanApply reports whether the draft could be sent. It says nothing about the device, which
// ApplyProfile checks again.
func (e *ProfileEdit) CanApply() bool { return e.editable && e.Draft.Valid() && !e.InvalidCurve }

func (e *ProfileEdit) edit(value Settings) bool {
	if !e.CanApply() || !value.Valid() {
		return false
	}
	e.Draft, e.Dirty = value, true
	return true
}

// EditCurve replaces both audiograms: 8 points per ear, each 0 to 90. Anything else marks
// the curve invalid, which blocks applying until a valid one replaces it.
func (e *ProfileEdit) EditCurve(left, right []float64) bool {
	if !e.editable {
		return false
	}
	e.InvalidCurve = len(left) != Bands || len(right) != Bands
	for _, curve := range [][]float64{left, right} {
		for _, v := range curve {
			if !(v >= 0 && v <= 90) { // Also catches NaN.
				e.InvalidCurve = true
			}
		}
	}
	if e.InvalidCurve {
		return false
	}
	value := e.Draft
	for i := range Bands {
		value.LeftEQ[i], value.RightEQ[i] = float32(left[i]), float32(right[i])
	}
	return e.edit(value)
}

func (e *ProfileEdit) gains(amplification, balance float32) bool {
	value := e.Draft
	value.LeftAmplification = clamp(amplification*2-1-balance*0.5, -1.5, 1.5)
	value.RightAmplification = clamp(amplification*2-1+balance*0.5, -1.5, 1.5)
	if !e.edit(value) {
		return false
	}
	e.Amplification, e.Balance = amplification, balance
	return true
}

// EditAmplification moves the amplification slider, 0 to 1.
func (e *ProfileEdit) EditAmplification(amplification float32) bool {
	return within(amplification, 0, 1) && e.gains(amplification, e.Balance)
}

// EditBalance moves the balance slider, -1 to 1.
func (e *ProfileEdit) EditBalance(balance float32) bool {
	return within(balance, -1, 1) && e.gains(e.Amplification, balance)
}

// EditTone sets both ears' tone, -1 to 1.
func (e *ProfileEdit) EditTone(tone float32) bool {
	value := e.Draft
	value.LeftTone, value.RightTone = tone, tone
	return e.edit(value)
}

// EditConversationBoost sets Conversation Boost for both ears.
func (e *ProfileEdit) EditConversationBoost(on bool) bool {
	value := e.Draft
	value.LeftConversationBoost, value.RightConversationBoost = on, on
	return e.edit(value)
}
