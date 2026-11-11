package hearing

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"slices"
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

// knownHearingPair accepts the enrolled and enabled bytes of a hearing report: enrolled (1)
// with Hearing Aid on (1) or off (2), or not enrolled (2) with it unset (0) or off (2). Not
// enrolled yet on, or enrolled with no state, is a report this app does not trust.
func knownHearingPair(enrolled, enabled byte) bool {
	return (enrolled == 1 && (enabled == 1 || enabled == 2)) || (enrolled == 2 && (enabled == 0 || enabled == 2))
}

func hearingReport(packet []byte) []byte {
	if len(packet) == 11 && bytes.HasPrefix(packet, prefixHearing) && knownHearingPair(packet[7], packet[8]) && packet[9] == 0 && packet[10] == 0 {
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

// pmeReport accepts a media assist record with both enable flags set and finite curves.
func pmeReport(packet []byte) []byte {
	if len(packet) != pmeSize || !bytes.HasPrefix(packet, headerPME) || packet[9] > 2 ||
		wireFlag(packet[10]) == FlagUnknown || wireFlag(packet[11]) == FlagUnknown {
		return nil
	}
	for offset := 12; offset < pmeSize; offset += 4 {
		if v := float64(floatAt(packet, offset)); math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
	}
	return bytes.Clone(packet)
}

// hearingEnabled reads a report hearingReport accepted; any state but on is off.
func hearingEnabled(packet []byte) Flag {
	if packet == nil {
		return FlagUnknown
	}
	return boolFlag(packet[8] == 1)
}

func allZero(value []byte) bool {
	return !slices.ContainsFunc(value, func(b byte) bool { return b != 0 })
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

// confirmed needs the cache to hold fresh reports of everything the plan intended, with the
// enrollment and the media assist curves unchanged. A Hearing Aid change that leaves both media
// assist flags off may also see the curves filled in or cleared: when either side is all zero,
// they are not in use.
func (p *ModePlan) confirmed(cache *modeCache) bool {
	if cache.hearing == nil || cache.hearing[7] != p.enrollment || cache.pme == nil {
		return false
	}
	state := cache.state()
	inactiveCurves := p.Mode == HearingAid && p.voice == FlagOff && p.media == FlagOff &&
		(allZero(p.pme[12:]) || allZero(cache.pme[12:]))
	return state.HearingEnabled == p.hearing && state.AssistEnabled == p.gate &&
		state.VoiceEnabled == p.voice && state.PMEMediaEnabled == p.media &&
		bytes.Equal(cache.pme[8:10], p.pme[8:10]) && (bytes.Equal(cache.pme[12:], p.pme[12:]) || inactiveCurves)
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
	confirmLimit = 8 * time.Second
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
// an enable it is to turn the mode off with Modes.StartRecover, which is possible without any
// new report. Nothing is resent automatically. Like dongle.Unconfirmed it never matches
// dongle.ErrNotSent, whatever Err does.
type ModeUnconfirmed struct {
	Mode     Mode
	On       bool
	Recovery *Recovery
	Message  string
	Err      error
}

func (e *ModeUnconfirmed) Error() string { return e.Message }

// Unwrap gives the cause as a dongle.Unconfirmed gives it, which is without dongle.ErrNotSent.
func (e *ModeUnconfirmed) Unwrap() error { return errors.Unwrap(unconfirmed(e.Message, e.Err)) }

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

	change *ModeChange   // under way, between its steps
	apply  *ProfileApply // under way, between its steps
}

func (m *Modes) bind(status dongle.Status) {
	if m.bound && m.generation == status.Generation && m.peer == status.Peer {
		return
	}
	var recovery *Recovery
	if m.bound && m.peer == status.Peer {
		recovery = m.recovery
	}
	// Work under way stays known, so nothing else starts. Its next step sees the new connection.
	*m = Modes{bound: true, generation: status.Generation, peer: status.Peer, recovery: recovery, change: m.change, apply: m.apply}
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

// Set turns Hearing Aid or Media Assist on or off, without writing the stored profile. It is
// StartSet followed by Step until done, blocking for up to 8 s with exclusive use of the
// client. An app with a device owner goroutine that also polls should not run it as one job:
// use StartSet and one job per Step instead. The checks and the outcomes are those of StartSet.
func (m *Modes) Set(c *dongle.Client, mode Mode, on bool) (*ModeResult, error) {
	change, err := m.StartSet(c, mode, on)
	if err != nil {
		return nil, err
	}
	return change.wait(c)
}

// Recover turns off the mode named by Recovery. It is StartRecover followed by Step until
// done, blocking for up to 8 s as Set does.
func (m *Modes) Recover(c *dongle.Client) (*ModeResult, error) {
	change, err := m.StartRecover(c)
	if err != nil {
		return nil, err
	}
	return change.wait(c)
}

// StartSet begins turning Hearing Aid or Media Assist on or off, without writing the stored
// profile. Everything is checked on fresh reports inside one lease, before any packet, and
// the lease is released before it returns. A failed check is a *Refused; a failed read during
// the checks comes back as it is, also before any packet:
//   - the model must have hearing assistance;
//   - enabling needs enrollment and complete, consistent mode reports;
//   - enabling reads the stored profile first and refuses unless it is a writable saved
//     fitting, then refuses unless Custom Transparency is known to be off (skipped only for
//     Hearing Aid when it is already reported on);
//   - the reports must not change during those reads;
//   - no other change on this Modes may be under way.
//
// Packets go out once, here. Step then follows the outcome and never sends a packet. A
// *ModeUnconfirmed, from here or from Step, means the state is unknown: offer StartRecover,
// and do not call StartSet again on its own. A failure on the first packet that never went out
// comes back as it is, since nothing changed.
//
// Gating: model.Profile.HearingAid is enforced here. Callers keep this behind the
// experimental switch, since it changes how loud sound is presented and has not run on real
// AirPods. Blocking for a moment; run it on the device owner goroutine.
func (m *Modes) StartSet(c *dongle.Client, mode Mode, on bool) (*ModeChange, error) {
	if mode != HearingAid && mode != MediaAssist {
		return nil, refuse("Unknown hearing mode.")
	}
	if err := m.requireIdle(); err != nil {
		return nil, err
	}
	o, err := m.beginHearing(c)
	if err != nil {
		return nil, err
	}
	defer o.close()
	change, err := m.startChange(o, mode, on)
	if err != nil {
		m.editable = false
	}
	return change, err
}

// StartRecover begins turning off the mode named by Recovery, using the last good packets if
// the AirPods have not reported since. It runs the same checks and confirmation as
// StartSet(mode, false), and it is an explicit user action, never automatic.
//
// Gating: as for StartSet. Blocking for a moment; run it on the device owner goroutine.
func (m *Modes) StartRecover(c *dongle.Client) (*ModeChange, error) {
	if m.recovery == nil {
		return nil, refuse("There is no hearing mode change to recover from.")
	}
	return m.StartSet(c, m.recovery.Mode, false)
}

func (m *Modes) requireIdle() error {
	if m.change != nil || m.apply != nil {
		return refuse("A hearing change is still being confirmed. Wait for it to finish.")
	}
	return nil
}

// startChange checks and sends one change inside the caller's lease.
func (m *Modes) startChange(o *operation, mode Mode, on bool) (*ModeChange, error) {
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
		if value[1] != 2 {
			return nil, refuse("These AirPods have no writable saved hearing profile, so hearing assistance stays as it is. Set it up from an iPhone or iPad.")
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
	change := &ModeChange{modes: m, plan: plan, original: original, start: o.start, deadline: o.deadline}
	if sent, err := m.sendPlan(o, plan); err != nil {
		if !sent {
			return nil, err // Nothing went out, so the state is what it was.
		}
		_, _, err = change.end(nil, err)
		return nil, err
	}
	change.confirmBy = o.clock.Now().Add(confirmLimit)
	m.change = change
	return change, nil
}

// sendPlan sends the plan once. Nothing here is ever repeated. sent reports whether anything
// may have gone out; when it is false the error is the reason, unchanged.
func (m *Modes) sendPlan(o *operation, plan *ModePlan) (sent bool, err error) {
	for i, packet := range plan.packets {
		slot, tracked := slotOf(packet)
		if tracked {
			index := slot - dongle.SlotHearingAid
			m.fences[index], m.fenced[index] = m.revisions[index], true
			m.cache.invalidate(slot)
		}
		if err := o.SendAAP(packet); err != nil {
			if i == 0 && errors.Is(err, dongle.ErrNotSent) {
				if tracked {
					m.fenced[slot-dongle.SlotHearingAid] = false // Nothing to wait for; the next read restores it.
				}
				return false, err
			}
			return true, fmt.Errorf("The hearing mode change was interrupted, so its state is unknown: %w", err)
		}
	}
	if len(plan.packets) > 0 {
		// This only asks for reports. It does not stand in for reading them.
		if err := o.SendAAP(requestReports); err != nil {
			return true, fmt.Errorf("The hearing mode change was sent, but its state is unknown: %w", err)
		}
	}
	return len(plan.packets) > 0, nil
}

// ModeChange follows one change that StartSet sent. Each Step is its own short device owner
// job with its own short lease, and the owner stays free in between, so status polling goes on
// through the whole wait. Its methods must not run concurrently, nor alongside other methods
// of the Modes it came from. While it runs, the Modes refuses to start another change.
type ModeChange struct {
	modes     *Modes
	plan      *ModePlan
	original  []byte
	start     dongle.Status
	deadline  time.Time
	confirmBy time.Time
	ended     bool
	result    *ModeResult
	err       error
}

// wait runs the steps one after the other, for the blocking calls.
func (ch *ModeChange) wait(c *dongle.Client) (*ModeResult, error) {
	for {
		result, done, err := ch.Step(c)
		if done {
			return result, err
		}
		c.Clock().Sleep(confirmPoll)
	}
}

// Step looks once at the change and returns without waiting. done means it has ended: a nil
// error says the AirPods reported the planned state with new reception revisions and, after an
// enable, that the stored profile read back byte for byte. A connection that changes, a
// recording that starts or a failed read ends it as a *ModeUnconfirmed, as does the 8 s limit.
// Another client holding the channel only delays the look, within that limit. It sends nothing
// to the AirPods. Run it on the device owner goroutine, every 100 ms or more.
func (ch *ModeChange) Step(c *dongle.Client) (*ModeResult, bool, error) {
	if ch.ended {
		return ch.result, true, ch.err
	}
	m := ch.modes
	o, err := reopenOperation(c, ch.start, ch.deadline)
	if err != nil {
		return ch.endUnlessBusy(c, err)
	}
	defer o.close()
	if err := m.refresh(o); err != nil {
		return ch.endUnlessBusy(c, err)
	}
	if !ch.plan.confirmed(&m.cache) {
		if !o.clock.Now().Before(ch.confirmBy) {
			return ch.end(nil, errors.New("The hearing mode change was sent, but the AirPods did not confirm it. Reconnect the AirPods and read the state again."))
		}
		return nil, false, nil
	}
	result := &ModeResult{Sent: len(ch.plan.packets) > 0}
	if ch.original != nil {
		characteristic, value, settings, err := readSettings(o.Exchange)
		if err != nil {
			return ch.endUnlessBusy(c, err)
		}
		if !bytes.Equal(value, ch.original) {
			return ch.end(nil, errors.New("The stored hearing profile is no longer the one read at the start. Read it again."))
		}
		if err := m.refresh(o); err != nil {
			return ch.endUnlessBusy(c, err)
		}
		if !ch.plan.confirmed(&m.cache) {
			return ch.end(nil, errors.New("The hearing modes changed before the request finished. Read them again."))
		}
		m.profile, m.editable = &settings, editableValue(value, characteristic)
		result.Profile = &Profile{Settings: settings, Editable: m.editable}
	}
	result.State = m.cache.state()
	return ch.end(result, nil)
}

// Absent stands in for Step when the owner has no device to run it with. The outcome cannot
// be followed any further, so it ends the change as Step would on a failed read, with cause.
// Nothing touches the device. Run it where Step would run.
func (ch *ModeChange) Absent(cause error) (*ModeResult, bool, error) {
	if ch.ended {
		return ch.result, true, ch.err
	}
	return ch.end(nil, cause)
}

// endUnlessBusy lets a look wait while another client holds the channel: this change gave up
// the lease between steps, and being asked to wait says nothing about its outcome.
func (ch *ModeChange) endUnlessBusy(c *dongle.Client, err error) (*ModeResult, bool, error) {
	var rejected *dongle.RequestRejected
	if errors.As(err, &rejected) && rejected.Code == 2 && c.Clock().Now().Before(ch.confirmBy) {
		return nil, false, nil
	}
	return ch.end(nil, err)
}

// end records the outcome. Once packets went out, a failure means the state is unknown.
func (ch *ModeChange) end(result *ModeResult, err error) (*ModeResult, bool, error) {
	m := ch.modes
	ch.ended = true
	if m.change == ch {
		m.change = nil
	}
	mode, on := ch.plan.Mode, ch.plan.On
	if err == nil {
		if m.recovery != nil && m.recovery.Mode == mode {
			m.recovery = nil
		}
		ch.result = result
		return result, true, nil
	}
	m.editable = false
	if len(ch.plan.packets) == 0 {
		ch.err = err
		return nil, true, err
	}
	if on {
		m.recovery = &Recovery{Mode: mode}
	}
	var recovery *Recovery
	if m.recovery != nil && m.recovery.Mode == mode {
		recovery = m.recovery
	}
	ch.err = &ModeUnconfirmed{Mode: mode, On: on, Recovery: recovery, Message: err.Error(), Err: err}
	return nil, true, ch.err
}
