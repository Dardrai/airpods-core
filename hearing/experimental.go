package hearing

import (
	"bytes"
	"errors"
	"strings"
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

// SendResult is the outcome of a packet the AirPods never acknowledge. Status is always
// StatusSent: the dongle took it. It does not mean the AirPods applied it, so it must never
// be shown as confirmed.
type SendResult struct {
	Status string
	Note   string
}

// loudSoundHandle is a fixed attribute handle with no discovery behind it. Other firmware
// could place something else there, which is why a one byte 0 or 1 is required before a write.
const loudSoundHandle uint16 = 0x001B

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

// ExperimentalWriteTransparency writes Custom Transparency once and requires the whole 104
// byte record to read back. expected must be what was last read; the record is read again
// first and must still match. A request equal to the stored values writes nothing. Enabling is
// refused unless Hearing Aid and Media Assist are both reported off.
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
	if profile, ok := model.Find(m.model); requested.Enabled && ok && profile.HearingAid {
		state := m.cache.state()
		if state.HearingEnabled != FlagOff || state.Media != FlagOff {
			return TransparencySettings{}, refuse("Hearing assistance must be reported off before enabling Custom Transparency. If it is off, reconnect the AirPods.")
		}
	}
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
	if found.WriteOpcode == 0 {
		return TransparencySettings{}, refuse("Custom Transparency is read-only on these AirPods.")
	}
	written := patchTransparency(baseline, requested)
	pdu := append([]byte{found.WriteOpcode, byte(found.Handle), byte(found.Handle >> 8)}, written...)
	response, err := o.Exchange(pdu)
	if err != nil {
		return TransparencySettings{}, unconfirmedWrite("No reply to the Custom Transparency write. It may have applied; read it again.", err)
	}
	if found.WriteOpcode == attWrite && !bytes.Equal(response, []byte{attWriteR}) {
		return TransparencySettings{}, unconfirmed("The AirPods did not acknowledge Custom Transparency. Read it again.", errorText("Custom Transparency write.", response))
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

func loudSoundValue(response []byte) (bool, error) {
	if len(response) != 2 || response[0] != attReadR || response[1] > 1 {
		return false, errorText("Could not read Loud Sound Reduction.", response)
	}
	return response[1] == 1, nil
}

func loudSoundAllowed(p *model.Profile) bool { return p.LoudSoundReduction }

const loudSoundUnsupported = "Loud Sound Reduction is not supported by the model these AirPods report."

// ExperimentalReadLoudSoundReduction reads Loud Sound Reduction. It writes nothing. An error
// may mean the dongle does not pass this attribute through, not that the setting is missing.
//
// Gating: model.Profile.LoudSoundReduction is enforced here; callers need the experimental
// switch. UNVERIFIED over the dongle, and the attribute handle is fixed rather than
// discovered. Blocking; run it on the device owner goroutine.
func ExperimentalReadLoudSoundReduction(c *dongle.Client) (bool, error) {
	o, _, err := beginFlagged(c, loudSoundAllowed, loudSoundUnsupported)
	if err != nil {
		return false, err
	}
	defer o.close()
	response, err := o.Exchange(readRequest(loudSoundHandle))
	if err != nil {
		return false, err
	}
	return loudSoundValue(response)
}

// ExperimentalSetLoudSoundReduction writes Loud Sound Reduction once if it differs and reads
// it back. expected must be the value last read; a different stored value is refused. The
// result is the value read back, and a nil error means it equals requested.
//
// Errors: *Refused means nothing was written. *dongle.Unconfirmed means the write went out and
// its effect is unknown.
//
// Gating: model.Profile.LoudSoundReduction is enforced here; callers need the experimental
// switch. UNVERIFIED over the dongle, and the attribute handle is fixed rather than
// discovered. Blocking; run it on the device owner goroutine.
func ExperimentalSetLoudSoundReduction(c *dongle.Client, expected, requested bool) (bool, error) {
	o, _, err := beginFlagged(c, loudSoundAllowed, loudSoundUnsupported)
	if err != nil {
		return false, err
	}
	defer o.close()
	read := readRequest(loudSoundHandle)
	response, err := o.Exchange(read)
	if err != nil {
		return false, err
	}
	current, err := loudSoundValue(response)
	if err != nil {
		return false, err
	}
	if current != expected {
		return current, refuse("Loud Sound Reduction changed on the AirPods. Read it again before changing it.")
	}
	if requested == current {
		return current, nil
	}
	value := byte(0)
	if requested {
		value = 1
	}
	response, err = o.Exchange([]byte{attWrite, byte(loudSoundHandle), byte(loudSoundHandle >> 8), value})
	if err != nil {
		return current, unconfirmedWrite("No reply to the Loud Sound Reduction write. It may have applied; read it again.", err)
	}
	if !bytes.Equal(response, []byte{attWriteR}) {
		return current, unconfirmed("The AirPods did not acknowledge Loud Sound Reduction. Read it again.", errorText("Loud Sound Reduction write.", response))
	}
	response, err = o.Exchange(read)
	if err != nil {
		return current, unconfirmed("The Loud Sound Reduction write could not be read back. Read it again.", err)
	}
	after, err := loudSoundValue(response)
	if err != nil {
		return current, unconfirmed("The Loud Sound Reduction write could not be read back. Read it again.", err)
	}
	if after != requested {
		return after, unconfirmed("The AirPods did not apply Loud Sound Reduction.", nil)
	}
	return after, nil
}

// EQ is a custom equalizer. Mode is 1 recommended or 2 custom; each band is 0 to 100.
type EQ struct {
	Mode, Low, Mid, High byte
}

// Valid reports whether the mode and bands are in range.
func (e EQ) Valid() bool {
	return (e.Mode == 1 || e.Mode == 2) && e.Low <= 100 && e.Mid <= 100 && e.High <= 100
}

func eqPacket(e EQ) []byte {
	return []byte{4, 0, 4, 0, 0x63, 0, 5, 0, 1, e.Mode, e.Low, e.Mid, e.High}
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

// unlistedFailure words a failed send of a packet outside the dongle's fixed list of AAP
// packets. Firmware up to at least 0.1.62 refuses such a packet with code 6 before sending
// anything, so that code is a refusal here; anything else keeps the honest uncertainty.
func unlistedFailure(name string, err error) error {
	var rejected *dongle.RequestRejected
	if errors.As(err, &rejected) && rejected.Code == 6 {
		return &Refused{Reason: "The dongle refused to pass " + name + " on to the AirPods: its firmware only sends a fixed list of AirPods requests. Nothing was sent.", cause: err}
	}
	return sendFailure(name, err)
}

// ExperimentalSendEQ sends a custom EQ. There is no readback, so success is only ever
// StatusSent: the result must read "sent", never "confirmed". Sent once.
//
// Gating: model.Profile.CustomEQ and firmware major 9 or newer are enforced here; callers need
// the experimental switch. Current dongle firmware refuses this packet, which is reported as
// Refused. UNVERIFIED over the dongle: the AirPods may ignore it with no error.
// Blocking; run it on the device owner goroutine.
func ExperimentalSendEQ(c *dongle.Client, eq EQ) (SendResult, error) {
	if !eq.Valid() {
		return SendResult{}, refuse("Choose the recommended or custom EQ mode, and keep each band between 0 and 100.")
	}
	o, meta, err := beginFlagged(c, func(p *model.Profile) bool { return p.CustomEQ }, "Custom EQ is not supported by the model these AirPods report.")
	if err != nil {
		return SendResult{}, err
	}
	defer o.close()
	if dongle.FirmwareMajor(meta.Firmware) < 9 {
		return SendResult{}, refuse("Custom EQ needs firmware 9 or newer on these AirPods.")
	}
	if err := o.SendAAP(eqPacket(eq)); err != nil {
		return SendResult{}, unlistedFailure("EQ", err)
	}
	return SendResult{Status: StatusSent, Note: "Sent to the AirPods. They do not confirm EQ, so its effect is unknown."}, nil
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
// Gating: no model flag; callers need the experimental switch. Current dongle firmware refuses
// this packet, which is reported as Refused. UNVERIFIED over the dongle.
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
		return SendResult{}, unlistedFailure("the name", err)
	}
	return SendResult{Status: StatusSent, Note: "Sent to the AirPods. The old name may show until they reconnect."}, nil
}
