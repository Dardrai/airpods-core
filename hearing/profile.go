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

// ReadProfile reads the stored hearing profile, which becomes the baseline StartApplyProfile
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

// PrepareProfile checks, on a fresh read and without changing anything, that StartApplyProfile
// could start from expected: the stored profile still equals it and can be written, and the
// AirPods report hearing assistance as set up. A nil error means it could.
//
// Gating: as for StartApplyProfile. Blocking; run it on the device owner goroutine.
func (m *Modes) PrepareProfile(c *dongle.Client, expected Settings) error {
	o, err := m.beginHearing(c)
	if err != nil {
		return err
	}
	defer o.close()
	if err := m.requireBaseline(expected, expected); err != nil {
		return err
	}
	if _, _, err := prepare(o.Exchange, expected); err != nil {
		m.editable = false
		return err
	}
	return requireEnrolled(m.cache.state())
}

// requireEnrolled refuses a profile write unless the AirPods report hearing assistance as set
// up and their Hearing Aid state is known. Whether Hearing Aid is on does not matter: a write
// never changes a mode.
func requireEnrolled(state ModeState) error {
	if state.HearingEnrolled != FlagOn {
		return refuse("Hearing assistance has not been set up on these AirPods. Set it up from an iPhone or iPad.")
	}
	if state.HearingEnabled == FlagUnknown {
		return refuse("Wait for the AirPods to report Hearing Aid before editing.")
	}
	return nil
}

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
	request := writeRequest(c, written)
	response, err := exchange(request)
	if err != nil {
		return Settings{}, unconfirmedWrite("No reply to the hearing profile write. It may have applied; read the profile again.", err)
	}
	if err := writeAcknowledged("Hearing profile write.", request, response); err != nil {
		return Settings{}, unconfirmed("The AirPods did not acknowledge the hearing profile write. Read the profile again.", err)
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

// ProfileResult is a confirmed profile write. Settings is what read back; State is the hearing
// modes, which the write left as they were. Written is false when the stored record already
// held the requested values, so nothing was sent.
type ProfileResult struct {
	Settings Settings
	State    ModeState
	Written  bool
}

// ApplyProfile writes a hearing profile. It is StartApplyProfile followed by Step until done,
// with exclusive use of the client. The checks and the outcomes are those of StartApplyProfile.
func (m *Modes) ApplyProfile(c *dongle.Client, expected, requested Settings) (*ProfileResult, error) {
	apply, err := m.StartApplyProfile(c, expected, requested)
	if err != nil {
		return nil, err
	}
	for {
		result, done, err := apply.Step(c)
		if done {
			return result, err
		}
		c.Clock().Sleep(confirmPoll)
	}
}

// ProfileApply follows one profile write. Its Step is a short device owner job with its own
// short lease, and the owner stays free before it. Its methods must not run concurrently, nor
// alongside other methods of the Modes it came from. While it runs, the Modes refuses to start
// another change.
type ProfileApply struct {
	modes     *Modes
	expected  Settings
	requested Settings
	start     dongle.Status
	deadline  time.Time
	writeBy   time.Time
	ended     bool
	result    *ProfileResult
	err       error
}

// StartApplyProfile begins writing a hearing profile, once, as an explicit user action.
// expected must be what ReadProfile returned on this connection. It checks, on a fresh read
// inside one lease, that the stored record still equals expected and is a writable saved
// fitting, and that the AirPods report hearing assistance as set up with a known Hearing Aid
// state. It never changes a hearing mode: Hearing Aid may be on or off, and stays as it is.
//
// Step then makes the write, after checking all of that again: the record goes out once, must
// read back whole, and the hearing modes must not move around it. Fields the profile does not
// model are kept byte for byte. A request the stored record already holds sends nothing.
//
// Errors, from here or from Step: *Refused means nothing changed. *dongle.Unconfirmed means
// the write went out and its outcome is unknown: read again before doing anything else.
//
// Gating: model.Profile.HearingAid is enforced here. Callers keep this behind the
// experimental switch, since it changes how loud sound is presented. UNVERIFIED on real
// AirPods. Blocking for a moment; run it on the device owner goroutine.
func (m *Modes) StartApplyProfile(c *dongle.Client, expected, requested Settings) (*ProfileApply, error) {
	if err := m.requireIdle(); err != nil {
		return nil, err
	}
	o, err := m.beginHearing(c)
	if err != nil {
		return nil, err
	}
	defer o.close()
	if err := m.requireBaseline(expected, requested); err != nil {
		return nil, err
	}
	if _, _, err := prepare(o.Exchange, expected); err != nil {
		m.editable = false
		return nil, err
	}
	if err := m.refresh(o); err != nil {
		return nil, err
	}
	if err := requireEnrolled(m.cache.state()); err != nil {
		return nil, err
	}
	a := &ProfileApply{modes: m, expected: expected, requested: requested,
		start: o.start, deadline: o.deadline, writeBy: o.clock.Now().Add(confirmLimit)}
	m.apply = a
	return a, nil
}

// Step makes the write and returns without waiting, unless another client holds the channel:
// then it returns not done, and a later Step tries again within 8 s. done means it has ended.
// The profile write goes out at most once. Run it on the device owner goroutine, every 100 ms
// or more.
func (a *ProfileApply) Step(c *dongle.Client) (*ProfileResult, bool, error) {
	if a.ended {
		return a.result, true, a.err
	}
	o, err := reopenOperation(c, a.start, a.deadline)
	if err != nil {
		var rejected *dongle.RequestRejected
		if errors.As(err, &rejected) && rejected.Code == 2 && c.Clock().Now().Before(a.writeBy) {
			return nil, false, nil // Nothing was written, and another client holds the channel.
		}
		return a.finish(nil, err)
	}
	defer o.close()
	return a.finish(a.write(o))
}

// Absent stands in for Step when the owner has no device to run it with. The write has not
// gone out, so it ends with cause and nothing changed. Run it where Step would run.
func (a *ProfileApply) Absent(cause error) (*ProfileResult, bool, error) {
	if a.ended {
		return a.result, true, a.err
	}
	return a.finish(nil, cause)
}

// write is the one piece that sends the profile, under one lease.
func (a *ProfileApply) write(o *operation) (*ProfileResult, error) {
	m := a.modes
	if err := m.refresh(o); err != nil {
		return nil, err
	}
	modes := m.cache.state()
	if err := requireEnrolled(modes); err != nil {
		return nil, err
	}
	characteristic, baseline, err := prepare(o.Exchange, a.expected)
	if err != nil {
		return nil, err
	}
	if err := m.refresh(o); err != nil {
		return nil, err
	}
	if m.cache.state() != modes {
		return nil, refuse("The hearing modes changed before the profile write. Read the settings again.")
	}
	if bytes.Equal(patchRecord(baseline, a.requested)[4:], baseline[4:]) {
		return &ProfileResult{Settings: a.expected, State: modes}, nil
	}
	m.editable = false
	confirmed, err := writeSettings(o.Exchange, o.clock, characteristic, baseline, a.requested)
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
	return &ProfileResult{Settings: confirmed, State: m.cache.state(), Written: true}, nil
}

func (a *ProfileApply) finish(result *ProfileResult, err error) (*ProfileResult, bool, error) {
	a.ended, a.result, a.err = true, result, err
	if a.modes.apply == a {
		a.modes.apply = nil
	}
	if err != nil {
		a.modes.editable = false
	}
	return result, true, err
}

// ProfileEdit is a draft of a profile being edited, with the combined gain controls. Nothing
// here talks to the device; pass Baseline and Draft to Modes.StartApplyProfile.
//
// Amplification is the mean of the two ears' gains, -1.5 to 1.5, and Balance is right minus
// left, -3 to 3. Each ear's stored gain is amplification minus or plus half the balance. An
// edit that would put either ear outside -1.5 to 1.5 is rejected, never clamped. Stored gains
// stay exact until a gain control is edited.
type ProfileEdit struct {
	Draft         Settings
	Amplification float32
	Balance       float32
	Dirty         bool
	InvalidCurve  bool
	baseline      Settings
	editable      bool
}

// NewProfileEdit starts a draft from a profile read from the AirPods.
func NewProfileEdit(p Profile) *ProfileEdit {
	s := p.Settings
	return &ProfileEdit{
		Draft:         s,
		baseline:      s,
		editable:      p.Editable,
		Amplification: (s.LeftAmplification + s.RightAmplification) / 2,
		Balance:       s.RightAmplification - s.LeftAmplification,
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

// gains sets both ears from the combined controls; edit rejects an ear out of range.
func (e *ProfileEdit) gains(amplification, balance float32) bool {
	value := e.Draft
	value.LeftAmplification = amplification - balance/2
	value.RightAmplification = amplification + balance/2
	if !e.edit(value) {
		return false
	}
	e.Amplification, e.Balance = amplification, balance
	return true
}

// EditAmplification sets the mean gain, -1.5 to 1.5, keeping the balance. It returns false,
// changing nothing, when either ear would leave -1.5 to 1.5.
func (e *ProfileEdit) EditAmplification(amplification float32) bool {
	return within(amplification, -1.5, 1.5) && e.gains(amplification, e.Balance)
}

// EditBalance sets right minus left, -3 to 3, keeping the mean gain. It returns false,
// changing nothing, when either ear would leave -1.5 to 1.5.
func (e *ProfileEdit) EditBalance(balance float32) bool {
	return within(balance, -3, 3) && e.gains(e.Amplification, balance)
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
