package hearing

import (
	"bytes"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/model"
)

// Everything in this file is EXPERIMENTAL AND UNVERIFIED OVER THE DONGLE. These features are
// not known to work through this path: the dongle firmware or the AirPods may refuse them or
// drop them silently, and none of them has run on real hardware. Callers must keep them behind
// the experimental switch and must word them as unverified. They report what really happened,
// including failure; a nil error never claims more than its doc comment states.

// StatusSent is the only success a send without readback can report.
const StatusSent = "sent"

// SendResult is the outcome of a packet the AirPods never acknowledge, which is only a
// rename. Status is always StatusSent: the dongle took it. It does not mean the AirPods
// applied it, so it must never be shown as confirmed.
type SendResult struct {
	Status string
	Note   string
}

// loudSoundUUID is Loud Sound Reduction in the settings service. Its handle is found by
// discovery in every request and never assumed.
var loudSoundUUID = ParseUUID("a48fec08392143db82aaafbce8ebb4fb")

func beginFlagged(c *dongle.Client, allowed func(*model.Profile) bool, unsupported string) (*operation, dongle.Metadata, error) {
	o, err := openOperation(c)
	if err != nil {
		return nil, dongle.Metadata{}, err
	}
	meta, err := identity(o)
	if err == nil {
		profile, ok := model.Find(meta.Model)
		if !ok || !allowed(profile) {
			err = refuse("%s", unsupported)
		}
	}
	if err != nil {
		o.close()
		return nil, dongle.Metadata{}, err
	}
	return o, meta, nil
}

func beginTransparency(m *Modes, c *dongle.Client) (*operation, error) {
	o, err := m.begin(c)
	if err != nil {
		return nil, err
	}
	if profile, ok := model.Find(m.model); !ok || !profile.CustomTransparency {
		o.close()
		return nil, refuse("Custom Transparency is not supported by the model these AirPods report.")
	}
	return o, nil
}

// ExperimentalReadTransparency reads Custom Transparency. It writes nothing.
//
// Gating: model.Profile.CustomTransparency is enforced here; callers need the experimental
// switch. UNVERIFIED over the dongle. Blocking; run it on the device owner goroutine.
func (m *Modes) ExperimentalReadTransparency(c *dongle.Client) (TransparencySettings, error) {
	o, err := beginTransparency(m, c)
	if err != nil {
		return TransparencySettings{}, err
	}
	defer o.close()
	found, _, settings, err := readTransparency(readOnly(o.Exchange))
	if err != nil {
		return TransparencySettings{}, err
	}
	if found == nil {
		return TransparencySettings{}, refuse("Custom Transparency is not available on these AirPods.")
	}
	return settings, nil
}

// patchTransparency changes only the editable fields; both curves and the own-voice value
// keep their stored bytes, as do fields whose value is unchanged.
func patchTransparency(baseline []byte, requested TransparencySettings) []byte {
	value := bytes.Clone(baseline)
	flag := func(on bool) float32 {
		if on {
			return 1
		}
		return 0
	}
	for offset, v := range map[int]float32{
		0:          flag(requested.Enabled),
		offLeftAmp: requested.LeftAmplification, offRightAmp: requested.RightAmplification,
		offLeftTone: requested.LeftTone, offRightTone: requested.RightTone,
		offLeftConv: flag(requested.LeftConversationBoost), offRightConv: flag(requested.RightConversationBoost),
		offLeftANR: requested.LeftAmbientNoiseReduction, offRightANR: requested.RightAmbientNoiseReduction,
	} {
		if floatAt(value, offset) != v {
			putFloat(value, offset, v)
		}
	}
	return value
}

// ExperimentalWriteTransparency writes Custom Transparency once and requires the whole record
// to read back, in the length it was read: 100 bytes, or 104 with Own Voice. expected must be
// what was last read; the record is read again first and must still match. A request equal to
// the stored values writes nothing. On a model with hearing assistance, any change other than
// turning it off is refused, before anything is sent, unless Hearing Aid, the hearing gate and
// any media assist in use are all reported off.
//
// Errors: *Refused means nothing was written. *dongle.Unconfirmed means the write went out and
// its effect is unknown: read again, and do not resend on your own.
//
// Gating: model.Profile.CustomTransparency is enforced here; callers need the experimental
// switch. UNVERIFIED over the dongle, and it changes how loud sound is presented. Blocking;
// run it on the device owner goroutine.
func (m *Modes) ExperimentalWriteTransparency(c *dongle.Client, expected, requested TransparencySettings) (TransparencySettings, error) {
	if !requested.Valid() {
		return TransparencySettings{}, refuse("Custom Transparency values are out of range.")
	}
	if !expected.Valid() {
		return TransparencySettings{}, refuse("Read Custom Transparency before changing it.")
	}
	o, err := beginTransparency(m, c)
	if err != nil {
		return TransparencySettings{}, err
	}
	defer o.close()
	found, baseline, current, err := readTransparency(readOnly(o.Exchange))
	if err != nil {
		return TransparencySettings{}, err
	}
	if found == nil {
		return TransparencySettings{}, refuse("Custom Transparency is not available on these AirPods.")
	}
	if current != expected {
		return TransparencySettings{}, refuse("Custom Transparency changed on the AirPods. Read it again before changing it.")
	}
	if requested == current {
		return current, nil
	}
	if err := m.requireTransparencyAllowed(current, requested); err != nil {
		return TransparencySettings{}, err
	}
	if found.WriteOpcode == 0 {
		return TransparencySettings{}, refuse("Custom Transparency is read-only on these AirPods.")
	}
	written := patchTransparency(baseline, requested)
	request := writeRequest(*found, written)
	response, err := o.Exchange(request)
	if err != nil {
		return TransparencySettings{}, unconfirmedWrite("No reply to the Custom Transparency write. It may have applied; read it again.", err)
	}
	if err := writeAcknowledged("Custom Transparency write.", request, response); err != nil {
		return TransparencySettings{}, unconfirmed("The AirPods did not acknowledge Custom Transparency. Read it again.", err)
	}
	for attempt := 1; ; attempt++ {
		value, err := readValue(o.Exchange, found.Handle, "Could not read Custom Transparency back.")
		if err != nil {
			return TransparencySettings{}, unconfirmed("The Custom Transparency write could not be read back. Read it again.", err)
		}
		if bytes.Equal(value, written) {
			settings, err := parseTransparency(value)
			if err != nil {
				return TransparencySettings{}, unconfirmed("The Custom Transparency write read back in a format this app does not know.", err)
			}
			return settings, nil
		}
		// A write may land after the first read. Any other value is not ours to accept.
		if !bytes.Equal(value, baseline) || attempt == readbackTries {
			return TransparencySettings{}, unconfirmed("The AirPods did not confirm Custom Transparency. Read it again before changing it.", nil)
		}
		o.clock.Sleep(readbackWait)
	}
}

// requireTransparencyAllowed refuses, on a model with hearing assistance, any Custom
// Transparency change other than turning it off while Hearing Aid is on, the hearing gate is
// on, or media assist is on with the curves in use (enrollment byte 1). Every one of those
// reports must be known, or the change is refused too.
func (m *Modes) requireTransparencyAllowed(current, requested TransparencySettings) error {
	off := current
	off.Enabled = false
	if requested == off {
		return nil // Turning it off is always allowed.
	}
	if profile, ok := model.Find(m.model); !ok || !profile.HearingAid {
		return nil
	}
	state := m.cache.state()
	pme := m.cache.pme
	if state.HearingEnabled == FlagUnknown || state.AssistEnabled == FlagUnknown || pme == nil {
		return refuse("Hearing Aid and Media Assist must be reported off before Custom Transparency can be changed, except to turn it off. If they are off, reconnect the AirPods.")
	}
	mediaInUse := (state.VoiceEnabled == FlagOn || state.PMEMediaEnabled == FlagOn) && pme[9] == 1
	if state.HearingEnabled == FlagOn || state.AssistEnabled == FlagOn || mediaInUse {
		return refuse("Hearing Aid and Media Assist must be reported off before Custom Transparency can be changed, except to turn it off.")
	}
	return nil
}

func loudSoundAllowed(p *model.Profile) bool { return p.LoudSoundReduction }

const loudSoundUnsupported = "Loud Sound Reduction is not supported by the model these AirPods report."

// findLoudSound discovers Loud Sound Reduction. A malformed or duplicated discovery result is
// an error and never falls back to any fixed handle.
func findLoudSound(exchange Exchanger) (Characteristic, error) {
	found, err := DiscoverRead(exchange, "Loud Sound Reduction", loudSoundUUID)
	if err != nil {
		return Characteristic{}, err
	}
	if found[0] == nil {
		return Characteristic{}, refuse("Loud Sound Reduction is not available on these AirPods.")
	}
	return *found[0], nil
}

// readLoudSound reads the setting, which must be exactly one byte, 0 or 1.
func readLoudSound(exchange Exchanger, c Characteristic) (bool, error) {
	value, err := readValue(exchange, c.Handle, "Could not read Loud Sound Reduction.")
	if err != nil {
		return false, err
	}
	if len(value) != 1 || value[0] > 1 {
		return false, errors.New("Loud Sound Reduction reads as a value this app does not know.")
	}
	return value[0] == 1, nil
}

// ExperimentalReadLoudSoundReduction reads Loud Sound Reduction, found by discovery. It writes
// nothing.
//
// Gating: model.Profile.LoudSoundReduction (AirPods Pro 2 and 3) is enforced here; callers
// need the experimental switch. UNVERIFIED over the dongle. Blocking; run it on the device
// owner goroutine.
func ExperimentalReadLoudSoundReduction(c *dongle.Client) (bool, error) {
	o, _, err := beginFlagged(c, loudSoundAllowed, loudSoundUnsupported)
	if err != nil {
		return false, err
	}
	defer o.close()
	exchange := readOnly(o.Exchange)
	characteristic, err := findLoudSound(exchange)
	if err != nil {
		return false, err
	}
	return readLoudSound(exchange, characteristic)
}

// ExperimentalSetLoudSoundReduction writes Loud Sound Reduction once if it differs. expected
// must be the value last read; a different stored value is refused. The setting is read twice
// before the write, which must find the same value, and the write uses the form the
// characteristic advertises. Up to 5 reads 75 ms apart then look for the new value. The result
// is the value last read, and a nil error means it equals requested.
//
// Errors: *Refused means nothing was written. *dongle.Unconfirmed means the write went out and
// its effect is unknown.
//
// Gating: model.Profile.LoudSoundReduction (AirPods Pro 2 and 3) is enforced here; callers
// need the experimental switch. UNVERIFIED over the dongle. Blocking; run it on the device
// owner goroutine.
func ExperimentalSetLoudSoundReduction(c *dongle.Client, expected, requested bool) (bool, error) {
	o, _, err := beginFlagged(c, loudSoundAllowed, loudSoundUnsupported)
	if err != nil {
		return false, err
	}
	defer o.close()
	characteristic, err := findLoudSound(readOnly(o.Exchange))
	if err != nil {
		return false, err
	}
	current, err := readLoudSound(o.Exchange, characteristic)
	if err != nil {
		return false, err
	}
	if current != expected {
		return current, refuse("Loud Sound Reduction changed on the AirPods. Read it again before changing it.")
	}
	if requested == current {
		return current, nil
	}
	if characteristic.WriteOpcode == 0 {
		return current, refuse("Loud Sound Reduction is read-only on these AirPods.")
	}
	if again, err := readLoudSound(o.Exchange, characteristic); err != nil {
		return current, err
	} else if again != current {
		return again, refuse("Loud Sound Reduction changed on the AirPods. Read it again before changing it.")
	}
	value := byte(0)
	if requested {
		value = 1
	}
	request := writeRequest(characteristic, []byte{value})
	response, err := o.Exchange(request)
	if err != nil {
		return current, unconfirmedWrite("No reply to the Loud Sound Reduction write. It may have applied; read it again.", err)
	}
	if err := writeAcknowledged("Loud Sound Reduction write.", request, response); err != nil {
		return current, unconfirmed("The AirPods did not acknowledge Loud Sound Reduction. Read it again.", err)
	}
	for attempt := 1; ; attempt++ {
		after, err := readLoudSound(o.Exchange, characteristic)
		if err != nil {
			return current, unconfirmed("The Loud Sound Reduction write could not be read back. Read it again.", err)
		}
		if after == requested {
			return after, nil
		}
		// The write may land after the first read; the old value is the only other one.
		if attempt == readbackTries {
			return after, unconfirmed("The AirPods did not apply Loud Sound Reduction. Read it again before changing it.", nil)
		}
		o.clock.Sleep(readbackWait)
	}
}

// EQ is the AirPods' EQ as their report gives it. On is byte 9 of the report and of the
// write, 2 for on and 1 for off; each band is 0 to 100.
type EQ struct {
	On             bool
	Low, Mid, High byte
}

// Valid reports whether every band is in range.
func (e EQ) Valid() bool { return e.Low <= 100 && e.Mid <= 100 && e.High <= 100 }

var eqHeader = []byte{4, 0, 4, 0, 0x63, 0, 5, 0, 1}

func parseEQ(packet []byte) (EQ, bool) {
	if len(packet) != 13 || !bytes.HasPrefix(packet, eqHeader) || (packet[9] != 1 && packet[9] != 2) {
		return EQ{}, false
	}
	e := EQ{On: packet[9] == 2, Low: packet[10], Mid: packet[11], High: packet[12]}
	return e, e.Valid()
}

// eqPacket is the kept report with only the four values replaced.
func eqPacket(report []byte, e EQ) []byte {
	packet := bytes.Clone(report)
	packet[9] = 1
	if e.On {
		packet[9] = 2
	}
	packet[10], packet[11], packet[12] = e.Low, e.Mid, e.High
	return packet
}

// readEQ reads the EQ report the dongle keeps in slot 7, which firmware 0.1.64 and newer fill
// from the AirPods' own reports.
func readEQ(link Link) (*dongle.Cached, EQ, error) {
	report, err := link.Cached(dongle.SlotEQ)
	var unsupported *dongle.Unsupported
	if errors.As(err, &unsupported) {
		return nil, EQ{}, &Refused{Reason: "The dongle firmware does not keep the AirPods' EQ. EQ needs dongle firmware 0.1.64 or newer.", cause: err}
	}
	if err != nil {
		return nil, EQ{}, err
	}
	if report == nil {
		return nil, EQ{}, refuse("These AirPods have not reported their EQ on this connection, so it cannot be read or changed.")
	}
	e, ok := parseEQ(report.Packet)
	if !ok {
		return nil, EQ{}, refuse("The AirPods reported an EQ in a format this app does not know.")
	}
	return report, e, nil
}

// ExperimentalReadEQ reads the AirPods' last EQ report as the dongle keeps it. It sends
// nothing to the AirPods. A *Refused means there is no usable report: the AirPods have not
// sent one on this connection, or the dongle firmware is older than 0.1.64.
//
// Gating: none beyond the report itself; callers need the experimental switch. UNVERIFIED on
// real AirPods. Blocking for a moment; run it on the device owner goroutine.
func ExperimentalReadEQ(c *dongle.Client) (EQ, error) {
	o, err := openOperation(c)
	if err != nil {
		return EQ{}, err
	}
	defer o.close()
	_, e, err := readEQ(o)
	return e, err
}

// eqFailure words a failed EQ send. The dongle passes EQ on only while it knows the AirPods'
// model and holds a valid EQ report that has not changed since the request arrived; otherwise
// it answers code 6 without sending anything.
func eqFailure(err error) error {
	var rejected *dongle.RequestRejected
	if errors.As(err, &rejected) && rejected.Code == 6 {
		return &Refused{Reason: "The dongle did not pass the EQ change on: it has no current EQ report from these AirPods, the report changed, or it does not know their model. Nothing was sent. Read the EQ again.", cause: err}
	}
	return sendFailure("EQ", err)
}

// ExperimentalSetEQ changes the EQ. It is ExperimentalStartSetEQ followed by Step until done,
// blocking for up to 8 s with exclusive use of the client. An app with a device owner goroutine
// that also polls should use ExperimentalStartSetEQ and one job per Step instead.
func ExperimentalSetEQ(c *dongle.Client, expected, requested EQ) (EQ, error) {
	change, err := ExperimentalStartSetEQ(c, expected, requested)
	if err != nil {
		return EQ{}, err
	}
	for {
		result, done, err := change.Step(c)
		if done {
			return result, err
		}
		c.Clock().Sleep(confirmPoll)
	}
}

// ExperimentalStartSetEQ begins an EQ change as a read-modify-write of the AirPods' EQ report.
// expected must be what ExperimentalReadEQ returned; the report is read again, must still hold
// it, and is read once more just before the send, which must find the same report revision and
// bytes (its age may differ). The packet is that report with the requested values in place, so
// a caller that changes one band keeps the others. It goes out once, here. A request equal to
// the report sends nothing and returns a change that has already ended.
//
// Step then waits for a new EQ report: one with exactly the written bytes confirms the change,
// a different one or none within 8 s ends it as *dongle.Unconfirmed. Step never sends.
//
// Errors: *Refused means nothing was sent, including when the dongle declines the packet
// because it has no current EQ report from these AirPods or does not know their model.
//
// Gating: a valid EQ report from the AirPods, which only models with EQ send; callers need the
// experimental switch. UNVERIFIED on real AirPods. Blocking for a moment; run it on the device
// owner goroutine.
func ExperimentalStartSetEQ(c *dongle.Client, expected, requested EQ) (*EQChange, error) {
	if !requested.Valid() {
		return nil, refuse("Keep each EQ band between 0 and 100.")
	}
	o, err := openOperation(c)
	if err != nil {
		return nil, err
	}
	defer o.close()
	before, current, err := readEQ(o)
	if err != nil {
		return nil, err
	}
	if current != expected {
		return nil, refuse("The EQ changed on the AirPods. Read it again before changing it.")
	}
	if requested == current {
		return &EQChange{ended: true, result: current}, nil
	}
	again, _, err := readEQ(o)
	if err != nil {
		return nil, err
	}
	if again.Revision != before.Revision || !bytes.Equal(again.Packet, before.Packet) {
		return nil, refuse("The EQ changed on the AirPods. Read it again before changing it.")
	}
	packet := eqPacket(before.Packet, requested)
	if err := o.SendAAP(packet); err != nil {
		return nil, eqFailure(err)
	}
	return &EQChange{revision: before.Revision, packet: packet, requested: requested, sent: true,
		start: o.start, deadline: o.deadline, confirmBy: o.clock.Now().Add(confirmLimit)}, nil
}

// EQChange follows one EQ change. Each Step is its own short device owner job with its own
// short lease. Its methods must not run concurrently.
type EQChange struct {
	revision  uint32
	packet    []byte
	requested EQ
	sent      bool
	start     dongle.Status
	deadline  time.Time
	confirmBy time.Time
	ended     bool
	result    EQ
	err       error
}

// Sent reports whether the change sent a packet; false means the EQ already matched.
func (ch *EQChange) Sent() bool { return ch.sent }

// Step looks once for the AirPods' report and returns without waiting. done means it has
// ended: a nil error says a new EQ report carries exactly the written bytes, and the result is
// that EQ. It sends nothing. Run it on the device owner goroutine, every 100 ms or more.
func (ch *EQChange) Step(c *dongle.Client) (EQ, bool, error) {
	if ch.ended {
		return ch.result, true, ch.err
	}
	o, err := reopenOperation(c, ch.start, ch.deadline)
	if err != nil {
		return ch.endUnlessBusy(c, err)
	}
	defer o.close()
	report, err := o.Cached(dongle.SlotEQ)
	if err != nil {
		return ch.endUnlessBusy(c, err)
	}
	if report != nil && report.Revision != ch.revision {
		if !bytes.Equal(report.Packet, ch.packet) {
			return ch.end("A newer EQ report from the AirPods differs from the EQ sent. Read it again before changing it.", nil)
		}
		ch.ended, ch.result = true, ch.requested
		return ch.result, true, nil
	}
	if !o.clock.Now().Before(ch.confirmBy) {
		return ch.end("The EQ change was sent, but the AirPods did not report it. Read the EQ again before changing it.", nil)
	}
	return EQ{}, false, nil
}

// Absent stands in for Step when the owner has no device to run it with. It ends the change
// as unconfirmed, with cause. Nothing touches the device.
func (ch *EQChange) Absent(cause error) (EQ, bool, error) {
	if ch.ended {
		return ch.result, true, ch.err
	}
	return ch.end("The EQ change was sent, but its outcome could not be followed: "+cause.Error(), cause)
}

func (ch *EQChange) endUnlessBusy(c *dongle.Client, err error) (EQ, bool, error) {
	var rejected *dongle.RequestRejected
	if errors.As(err, &rejected) && rejected.Code == 2 && c.Clock().Now().Before(ch.confirmBy) {
		return EQ{}, false, nil
	}
	return ch.end("The EQ change was sent, but its outcome is unknown: "+err.Error(), err)
}

// end records a failure after the send: the EQ may or may not have changed.
func (ch *EQChange) end(message string, cause error) (EQ, bool, error) {
	ch.ended = true
	ch.err = unconfirmed(message, cause)
	return EQ{}, true, ch.err
}

// sendFailure says honestly what a failed send means: a refusal by the dongle changed
// nothing, while anything else may have got through.
func sendFailure(name string, err error) error {
	var unsupported *dongle.Unsupported
	var refusal *Refused
	if errors.As(err, &unsupported) || errors.As(err, &refusal) {
		return err
	}
	return unconfirmedWrite("Could not confirm "+name+" was sent. It may or may not have arrived.", err)
}

// MaxNameBytes is the longest AirPods name: 64 bytes of UTF-8, fewer characters when they
// are not ASCII.
const MaxNameBytes = 64

func renamePacket(name string) []byte {
	return append([]byte{4, 0, 4, 0, 0x1a, 0, 1, byte(len(name)), 0}, name...)
}

// ExperimentalRename sends a new AirPods name: 1 to 64 UTF-8 bytes, not blank, with no control
// characters. There is no readback, so success is only ever StatusSent, and the system may
// show the old name until the AirPods reconnect. Sent once.
//
// Gating: no model flag; callers need the experimental switch. Dongle firmware up to at least
// 0.1.66 refuses this packet, which is reported as Refused. UNVERIFIED over the dongle.
// Blocking; run it on the device owner goroutine.
func ExperimentalRename(c *dongle.Client, name string) (SendResult, error) {
	if !utf8.ValidString(name) || strings.TrimSpace(name) == "" || len(name) > MaxNameBytes || strings.ContainsFunc(name, unicode.IsControl) {
		return SendResult{}, refuse("Use a name of 1 to %d UTF-8 bytes with no control characters.", MaxNameBytes)
	}
	o, err := openOperation(c)
	if err != nil {
		return SendResult{}, err
	}
	defer o.close()
	if err := o.SendAAP(renamePacket(name)); err != nil {
		return SendResult{}, renameFailure(err)
	}
	return SendResult{Status: StatusSent, Note: "Sent to the AirPods. The old name may show until they reconnect."}, nil
}

// renameFailure words a failed rename. The dongle sends only a fixed list of AirPods requests,
// which up to at least firmware 0.1.66 has no rename; it refuses one with code 6 before sending
// anything, so that code is a refusal here. Anything else keeps the honest uncertainty.
func renameFailure(err error) error {
	var rejected *dongle.RequestRejected
	if errors.As(err, &rejected) && rejected.Code == 6 {
		return &Refused{Reason: "The dongle refused to pass the name on to the AirPods: its firmware only sends a fixed list of AirPods requests. Nothing was sent.", cause: err}
	}
	return sendFailure("the name", err)
}
