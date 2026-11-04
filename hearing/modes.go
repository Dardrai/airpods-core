package hearing

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/model"
)

// Flag is a value the AirPods reported, or the absence of a report.
type Flag int8

// Reported values. FlagUnknown never means off.
const (
	FlagUnknown Flag = iota
	FlagOff
	FlagOn
)

func (f Flag) String() string { return [...]string{"unknown", "off", "on"}[f] }

func wireFlag(value byte) Flag {
	switch value {
	case 1:
		return FlagOn
	case 2:
		return FlagOff
	}
	return FlagUnknown
}

func boolFlag(on bool) Flag {
	if on {
		return FlagOn
	}
	return FlagOff
}

func wireByte(on bool) byte {
	if on {
		return 1
	}
	return 2
}

// Mode is one of the two hearing assistance modes.
type Mode int

// The two modes.
const (
	HearingAid Mode = iota + 1
	MediaAssist
)

func (m Mode) String() string {
	if m == MediaAssist {
		return "Media Assist"
	}
	return "Hearing Aid"
}

// ModeState is what the AirPods last reported. Media is the gate combined with either
// media-assist flag, the value a Media Assist switch should show.
type ModeState struct {
	HearingEnrolled Flag
	HearingEnabled  Flag
	AssistEnabled   Flag
	VoiceEnrolled   Flag
	VoiceEnabled    Flag
	PMEMediaEnabled Flag
	Media           Flag
	Revision        uint64
}

var (
	prefixHearing = []byte{4, 0, 4, 0, 9, 0, 0x2c}
	prefixAssist  = []byte{4, 0, 4, 0, 9, 0, 0x33}
	prefixPME     = []byte{4, 0, 4, 0, 0x53, 0}
	headerPME     = []byte{4, 0, 4, 0, 0x53, 0, 0x84, 0, 2}

	listeningTransparency = []byte{4, 0, 4, 0, 9, 0, 0x0d, 3, 0, 0, 0}
	requestReports        = []byte{4, 0, 4, 0, 0x0f, 0, 0xff, 0xff, 0xff, 0xff}
)

const pmeSize = 140

func hearingReport(packet []byte) []byte {
	if len(packet) == 11 && bytes.HasPrefix(packet, prefixHearing) && packet[7] <= 2 && packet[8] <= 2 && packet[9] == 0 && packet[10] == 0 {
		return bytes.Clone(packet)
	}
	return nil
}

func assistReport(packet []byte) Flag {
	if len(packet) == 11 && bytes.HasPrefix(packet, prefixAssist) && packet[8] == 0 && packet[9] == 0 && packet[10] == 0 {
		return wireFlag(packet[7])
	}
	return FlagUnknown
}

func pmeReport(packet []byte) []byte {
	if len(packet) == pmeSize && bytes.HasPrefix(packet, headerPME) && packet[9] <= 2 && packet[10] <= 2 && packet[11] <= 2 {
		return bytes.Clone(packet)
	}
	return nil
}

// hearingEnabled treats an unset enabled byte as off only when not enrolled.
func hearingEnabled(packet []byte) Flag {
	if packet == nil {
		return FlagUnknown
	}
	enrolled, enabled := packet[7], packet[8]
	if enrolled == 2 && enabled == 0 {
		return FlagOff
	}
	if (enrolled == 1 || enrolled == 2) && (enabled == 1 || enabled == 2) {
		return boolFlag(enrolled == 1 && enabled == 1)
	}
	return FlagUnknown
}

// modeCache holds the latest reports and, separately, the last good packets. A template
// outlives an invalidated report so that Off stays possible when no report comes back.
type modeCache struct {
	revision        uint64
	hearing         []byte
	hearingTemplate []byte
	assist          Flag
	pme             []byte
	pmeTemplate     []byte
}

func first(a, b []byte) []byte {
	if a != nil {
		return a
	}
	return b
}

func (c *modeCache) state() ModeState {
	s := ModeState{Revision: c.revision, HearingEnabled: hearingEnabled(c.hearing), AssistEnabled: c.assist}
	if p := first(c.hearing, c.hearingTemplate); p != nil {
		s.HearingEnrolled = wireFlag(p[7])
	}
	if p := first(c.pme, c.pmeTemplate); p != nil {
		s.VoiceEnrolled = wireFlag(p[9])
	}
	if c.pme != nil {
		s.VoiceEnabled, s.PMEMediaEnabled = wireFlag(c.pme[10]), wireFlag(c.pme[11])
	}
	if s.AssistEnabled != FlagUnknown && s.VoiceEnabled != FlagUnknown && s.PMEMediaEnabled != FlagUnknown {
		s.Media = boolFlag(s.AssistEnabled == FlagOn && (s.VoiceEnabled == FlagOn || s.PMEMediaEnabled == FlagOn))
	}
	return s
}

// invalidate forgets a report but keeps its template.
func (c *modeCache) invalidate(slot byte) {
	changed := false
	switch slot {
	case dongle.SlotHearingAid:
		changed, c.hearing = c.hearing != nil, nil
	case dongle.SlotHearingMedia:
		changed, c.assist = c.assist != FlagUnknown, FlagUnknown
	case dongle.SlotHearingPME:
		changed, c.pme = c.pme != nil, nil
	}
	if changed {
		c.revision++
	}
}

// observe takes a slot's packet; a missing or foreign packet clears the report and its
// template, since neither can be trusted any longer.
func (c *modeCache) observe(slot byte, packet []byte) {
	changed := false
	switch slot {
	case dongle.SlotHearingAid:
		value := hearingReport(packet)
		changed = !bytes.Equal(c.hearing, value)
		c.hearing, c.hearingTemplate = value, value
	case dongle.SlotHearingMedia:
		value := assistReport(packet)
		changed = c.assist != value
		c.assist = value
	case dongle.SlotHearingPME:
		value := pmeReport(packet)
		changed = !bytes.Equal(c.pme, value)
		c.pme, c.pmeTemplate = value, value
	}
	if changed {
		c.revision++
	}
}

// ModePlan is what changing a mode would send and what the AirPods must report afterwards.
type ModePlan struct {
	Mode       Mode
	On         bool
	enrollment byte
	hearing    Flag
	voice      Flag
	media      Flag
	gate       Flag
	pme        []byte
	packets    [][]byte
}

// Packets returns a copy of the AAP packets the plan sends, in order. It may be empty.
func (p *ModePlan) Packets() [][]byte {
	out := make([][]byte, len(p.packets))
	for i, packet := range p.packets {
		out[i] = bytes.Clone(packet)
	}
	return out
}

func planMode(cache *modeCache, mode Mode, on bool) (*ModePlan, error) {
	state := cache.state()
	if on && state.HearingEnrolled != FlagOn {
		return nil, refuse("Hearing assistance has not been set up on these AirPods. Set it up from an iPhone or iPad.")
	}
	unknown := refuse("Wait for the AirPods to report the other hearing mode before changing this one.")
	if on {
		if state.HearingEnabled == FlagUnknown || state.VoiceEnabled == FlagUnknown ||
			state.PMEMediaEnabled == FlagUnknown || state.AssistEnabled == FlagUnknown {
			return nil, unknown
		}
		active := state.HearingEnabled == FlagOn || state.VoiceEnabled == FlagOn || state.PMEMediaEnabled == FlagOn
		if boolFlag(active) != state.AssistEnabled {
			return nil, refuse("The AirPods' hearing modes disagree. Reconnect the AirPods before changing them.")
		}
	}
	hearing, voice, media := state.HearingEnabled, state.VoiceEnabled, state.PMEMediaEnabled
	if mode == HearingAid {
		hearing = boolFlag(on)
	} else {
		voice, media = boolFlag(on), boolFlag(on)
	}
	if hearing == FlagUnknown || voice == FlagUnknown || media == FlagUnknown {
		return nil, unknown
	}
	pme := first(cache.pme, cache.pmeTemplate)
	report := first(cache.hearing, cache.hearingTemplate)
	if pme == nil || report == nil {
		return nil, unknown
	}
	gate := boolFlag(hearing == FlagOn || voice == FlagOn || media == FlagOn)
	plan := &ModePlan{Mode: mode, On: on, enrollment: report[7], hearing: hearing, voice: voice, media: media, gate: gate, pme: pme}
	if state.AssistEnabled != gate {
		plan.packets = append(plan.packets, []byte{4, 0, 4, 0, 9, 0, 0x33, wireByte(gate == FlagOn), 0, 0, 0})
	}
	switch {
	case mode == HearingAid && state.HearingEnabled != boolFlag(on):
		packet := bytes.Clone(report)
		packet[8] = wireByte(on)
		plan.packets = append(plan.packets, packet)
		if on {
			plan.packets = append(plan.packets, bytes.Clone(listeningTransparency))
		}
	case mode == MediaAssist && (state.VoiceEnabled != boolFlag(on) || state.PMEMediaEnabled != boolFlag(on)):
		// Only the two enable flags change; the fitting in the rest of the record is kept.
		packet := bytes.Clone(pme)
		packet[10], packet[11] = wireByte(on), wireByte(on)
		plan.packets = append(plan.packets, packet)
	}
	return plan, nil
}

// confirmed needs the cache to hold fresh reports of everything the plan intended.
func (p *ModePlan) confirmed(cache *modeCache) bool {
	if cache.hearing == nil || cache.hearing[7] != p.enrollment || cache.pme == nil {
		return false
	}
	state := cache.state()
	return state.HearingEnabled == p.hearing && state.AssistEnabled == p.gate &&
		state.VoiceEnabled == p.voice && state.PMEMediaEnabled == p.media &&
		bytes.Equal(cache.pme[8:10], p.pme[8:10]) && bytes.Equal(cache.pme[12:], p.pme[12:])
}

func slotOf(packet []byte) (byte, bool) {
	switch {
	case bytes.HasPrefix(packet, prefixHearing):
		return dongle.SlotHearingAid, true
	case bytes.HasPrefix(packet, prefixAssist):
		return dongle.SlotHearingMedia, true
	case bytes.HasPrefix(packet, prefixPME):
		return dongle.SlotHearingPME, true
	}
	return 0, false
}

const (
	confirmLimit = 15 * time.Second
	confirmPoll  = 100 * time.Millisecond
)

// ModeResult is a confirmed mode change. Profile is set after enabling, when the stored
// profile was read again and found unchanged.
type ModeResult struct {
	State   ModeState
	Sent    bool
	Profile *Profile
}

// ModeUnconfirmed means packets went out but the AirPods have not confirmed the outcome,
// so the mode may be in either state. Recovery says what the user should be offered; for
// an enable it is to turn the mode off with Modes.Recover, which is possible without any
// new report. Nothing is resent automatically.
type ModeUnconfirmed struct {
	Mode     Mode
	On       bool
	Recovery *Recovery
	Message  string
	Err      error
}

func (e *ModeUnconfirmed) Error() string { return e.Message }
func (e *ModeUnconfirmed) Unwrap() error { return e.Err }

// Recovery is the action offered after an unconfirmed change: turn this mode off.
type Recovery struct{ Mode Mode }

// Text is a user-facing explanation of the action.
func (r Recovery) Text() string {
	return fmt.Sprintf("%s may still be active. Turn it off to be sure.", r.Mode)
}

// Profile is a stored hearing profile as read. Editable false means it can be shown but
// this app does not know how to write that record back.
type Profile struct {
	Settings Settings
	Editable bool
}

// Modes tracks hearing mode reports across operations for one connected pair of AirPods.
// Create one with new(Modes) and keep it on the goroutine that owns the device; it is not
// safe for concurrent use. It forgets everything except a pending Recovery when the
// connection generation changes, and forgets that too if the AirPods themselves change.
//
// UNVERIFIED: no method here has run against real AirPods. Packets and checks are derived
// from the documented behaviour and tested only against the simulator in dongle/sim.
type Modes struct {
	bound      bool
	generation uint32
	peer       [6]byte
	model      string
	firmware   string

	cache     modeCache
	revisions [3]uint32 // by slot; 0 means no report, as a kept report's revision is nonzero
	fences    [3]uint32
	fenced    [3]bool

	profile  *Settings
	editable bool
	recovery *Recovery
}

func (m *Modes) bind(status dongle.Status) {
	if m.bound && m.generation == status.Generation && m.peer == status.Peer {
		return
	}
	var recovery *Recovery
	if m.bound && m.peer == status.Peer {
		recovery = m.recovery
	}
	*m = Modes{bound: true, generation: status.Generation, peer: status.Peer, recovery: recovery}
}

// observeSlot applies a kept report. While a slot is fenced only a report with a new
// reception revision counts: the same bytes may simply predate the request.
func (m *Modes) observeSlot(slot byte, report *dongle.Cached) {
	i := slot - dongle.SlotHearingAid
	if m.fenced[i] {
		if report == nil || report.Revision == m.fences[i] {
			m.cache.invalidate(slot)
			return
		}
		m.fenced[i] = false
	}
	if report == nil {
		m.revisions[i] = 0
		m.cache.observe(slot, nil)
		return
	}
	m.revisions[i] = report.Revision
	m.cache.observe(slot, report.Packet)
}

func (m *Modes) refresh(link Link) error {
	for slot := dongle.SlotHearingAid; slot <= dongle.SlotHearingPME; slot++ {
		report, err := link.Cached(slot)
		if err != nil {
			return err
		}
		m.observeSlot(slot, report)
	}
	return nil
}

// begin opens one operation and establishes what the AirPods report about themselves.
func (m *Modes) begin(c *dongle.Client) (*operation, error) {
	o, err := openOperation(c)
	if err != nil {
		return nil, err
	}
	m.bind(o.start)
	meta, err := identity(o)
	if err == nil {
		if meta.Model != m.model || meta.Firmware != m.firmware {
			m.profile, m.editable = nil, false
		}
		m.model, m.firmware = meta.Model, meta.Firmware
		err = m.refresh(o)
	}
	if err != nil {
		o.close()
		return nil, err
	}
	return o, nil
}

func (m *Modes) requireHearing() error {
	if profile, ok := model.Find(m.model); !ok || !profile.HearingAid {
		return refuse("Hearing assistance is not supported by the model these AirPods report.")
	}
	return nil
}

func (m *Modes) beginHearing(c *dongle.Client) (*operation, error) {
	o, err := m.begin(c)
	if err != nil {
		return nil, err
	}
	if err := m.requireHearing(); err != nil {
		o.close()
		return nil, err
	}
	return o, nil
}

// Read returns the kept mode reports, read from the dongle's slots 2 to 4. It sends nothing
// to the AirPods and works on any model; the result stays unknown when nothing was reported.
//
// Gating: none needed to read, though callers show the result only when
// model.Profile.HearingAid is set. Blocking; run it on the device owner goroutine.
func (m *Modes) Read(c *dongle.Client) (ModeState, error) {
	o, err := m.begin(c)
	if err != nil {
		return ModeState{}, err
	}
	defer o.close()
	return m.cache.state(), nil
}

// Plan works out what Set would send, from the state last read, without talking to the
// device. An error is the reason Set would refuse. Set repeats every check on fresh reports.
func (m *Modes) Plan(mode Mode, on bool) (*ModePlan, error) {
	return planMode(&m.cache, mode, on)
}

// Recovery returns the action to offer after an unconfirmed change, or nil. It is cleared
// when the mode is next confirmed.
func (m *Modes) Recovery() *Recovery { return m.recovery }

// Set turns Hearing Aid or Media Assist on or off, without writing the stored profile.
// Everything is checked on fresh reports inside one lease, before any packet. A failed check
// is a *Refused; a failed read during the checks comes back as it is, also before any packet:
//   - the model must have hearing assistance;
//   - enabling needs enrollment and complete, consistent mode reports;
//   - enabling reads the stored profile first, and refuses unless Custom Transparency is
//     known to be off (skipped only for Hearing Aid when it is already reported on);
//   - the reports must not change during those reads.
//
// Packets go out once. A nil error means the AirPods reported the planned state with new
// reception revisions and, after an enable, the stored profile read back byte for byte. A
// *ModeUnconfirmed means the state is unknown: offer Modes.Recover, and do not call Set
// again on its own.
//
// Gating: model.Profile.HearingAid is enforced here. Callers keep this behind the
// experimental switch, since it changes how loud sound is presented and has not run on real
// AirPods. Blocking for up to 15 s; run it on the device owner goroutine.
func (m *Modes) Set(c *dongle.Client, mode Mode, on bool) (*ModeResult, error) {
	if mode != HearingAid && mode != MediaAssist {
		return nil, refuse("Unknown hearing mode.")
	}
	o, err := m.beginHearing(c)
	if err != nil {
		return nil, err
	}
	defer o.close()
	result, err := m.setMode(o, mode, on)
	if err != nil {
		m.editable = false
	}
	return result, err
}

// Recover turns off the mode named by Recovery, using the last good packets if the AirPods
// have not reported since. It runs the same checks and confirmation as Set(mode, false),
// and it is an explicit user action, never automatic.
//
// Gating: as for Set. Blocking for up to 15 s; run it on the device owner goroutine.
func (m *Modes) Recover(c *dongle.Client) (*ModeResult, error) {
	if m.recovery == nil {
		return nil, refuse("There is no hearing mode change to recover from.")
	}
	return m.Set(c, m.recovery.Mode, false)
}

func (m *Modes) setMode(o *operation, mode Mode, on bool) (*ModeResult, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	if err := m.refresh(o); err != nil {
		return nil, err
	}
	initial := m.cache.state()
	plan, err := planMode(&m.cache, mode, on)
	if err != nil {
		return nil, err
	}
	m.editable = false
	var original []byte
	if on {
		_, value, _, err := readSettings(o.Exchange)
		if err != nil {
			return nil, err
		}
		if mode == MediaAssist || initial.HearingEnabled != FlagOn {
			if err := requireCleanTransparency(o.Exchange); err != nil {
				return nil, err
			}
		}
		original = value
	}
	if err := m.refresh(o); err != nil {
		return nil, err
	}
	if m.cache.state() != initial {
		return nil, refuse("The hearing modes moved while the request was being checked. Read them again.")
	}
	for _, packet := range plan.packets {
		if slot, ok := slotOf(packet); ok && m.revisions[slot-dongle.SlotHearingAid] == 0 {
			return nil, refuse("The current hearing modes are not known yet. Read them first.")
		}
	}
	result, err := m.applyPlan(o, plan, original)
	if err == nil {
		if m.recovery != nil && m.recovery.Mode == mode {
			m.recovery = nil
		}
		return result, nil
	}
	if len(plan.packets) == 0 {
		return nil, err
	}
	if on {
		m.recovery = &Recovery{Mode: mode}
	}
	var recovery *Recovery
	if m.recovery != nil && m.recovery.Mode == mode {
		recovery = m.recovery
	}
	return nil, &ModeUnconfirmed{Mode: mode, On: on, Recovery: recovery, Message: err.Error(), Err: err}
}

// applyPlan sends the plan once and waits for the AirPods to report its outcome.
func (m *Modes) applyPlan(o *operation, plan *ModePlan, original []byte) (*ModeResult, error) {
	for _, packet := range plan.packets {
		if slot, ok := slotOf(packet); ok {
			i := slot - dongle.SlotHearingAid
			m.fences[i], m.fenced[i] = m.revisions[i], true
			m.cache.invalidate(slot)
		}
		if err := o.SendAAP(packet); err != nil {
			return nil, fmt.Errorf("The hearing mode change was interrupted, so its state is unknown: %w", err)
		}
	}
	if len(plan.packets) > 0 {
		// This only asks for reports. It does not stand in for reading them.
		if err := o.SendAAP(requestReports); err != nil {
			return nil, fmt.Errorf("The hearing mode change was sent, but its state is unknown: %w", err)
		}
	}
	deadline := o.clock.Now().Add(confirmLimit)
	for {
		if err := o.check(); err != nil {
			return nil, err
		}
		if err := m.refresh(o); err != nil {
			return nil, err
		}
		if plan.confirmed(&m.cache) {
			break
		}
		if !o.clock.Now().Before(deadline) {
			return nil, errors.New("The hearing mode change was sent, but the AirPods did not confirm it. Reconnect the AirPods and read the state again.")
		}
		o.clock.Sleep(confirmPoll)
	}
	result := &ModeResult{Sent: len(plan.packets) > 0}
	if original != nil {
		c, value, settings, err := readSettings(o.Exchange)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(value, original) {
			return nil, errors.New("The stored hearing profile is no longer the one read at the start. Read it again.")
		}
		if err := m.refresh(o); err != nil {
			return nil, err
		}
		if !plan.confirmed(&m.cache) {
			return nil, errors.New("The hearing modes changed before the request finished. Read them again.")
		}
		m.profile, m.editable = &settings, editableValue(value, c)
		result.Profile = &Profile{Settings: settings, Editable: m.editable}
	}
	result.State = m.cache.state()
	return result, nil
}
