package hearing

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/Dardrai/airpods-core/dongle"
)

var checkRequestUUID = ParseUUID("a412000495c54d6f90980f0b41457e0a")

const (
	checkWait     = 120 * time.Second
	checkPoll     = 2 * time.Second
	checkTick     = 250 * time.Millisecond
	earReportLife = 30 * time.Second
	checkFirmware = "9A348"
)

// CheckPhase is where a hardware check stands.
type CheckPhase string

// Phases of a hardware check. Only the first two are still waiting.
const (
	CheckWaitingCaseClose CheckPhase = "waiting_case_close"
	CheckWaitingResult    CheckPhase = "waiting_result"
	CheckCompleted        CheckPhase = "completed"
	CheckTimedOut         CheckPhase = "timed_out"
	CheckStopped          CheckPhase = "stopped"
	CheckError            CheckPhase = "error"
)

// CheckProgress is a hardware check's state. A nil Record means no reading since the check
// began; a completed check reports what the AirPods saved, not a current health diagnosis.
type CheckProgress struct {
	Phase        CheckPhase
	LeftUpdated  bool
	RightUpdated bool
	Elapsed      time.Duration
	Remaining    time.Duration
	Record       *Diagnostics
	Message      string
}

// checkJob follows one requested check. Device clocks are not established, so a new
// measurement means an ear's exact stored time differs from the baseline, in either direction.
type checkJob struct {
	baseline [2]uint64
	phase    CheckPhase
	updated  [2]bool
	message  string
	err      error
}

func newCheckJob(baseline Diagnostics) *checkJob {
	return &checkJob{
		baseline: [2]uint64{baseline.Left.MeasuredAtSeconds, baseline.Right.MeasuredAtSeconds},
		phase:    CheckWaitingCaseClose,
	}
}

func (j *checkJob) active() bool {
	return j.phase == CheckWaitingCaseClose || j.phase == CheckWaitingResult
}

func (j *checkJob) tick(elapsed time.Duration) {
	if j.active() && elapsed >= checkWait {
		j.phase = CheckTimedOut
	}
}

func (j *checkJob) waiting() {
	if j.active() {
		j.phase = CheckWaitingResult
	}
}

func (j *checkJob) stop() {
	if j.active() {
		j.phase = CheckStopped
	}
}

func (j *checkJob) fail(err error) {
	if j.active() {
		j.phase, j.message, j.err = CheckError, err.Error(), err
	}
}

// observe recomputes both ears each time, so a replayed baseline cannot complete a check.
func (j *checkJob) observe(record Diagnostics) {
	if !j.active() {
		return
	}
	times := [2]uint64{record.Left.MeasuredAtSeconds, record.Right.MeasuredAtSeconds}
	for i := range times {
		j.updated[i] = times[i] != 0 && times[i] != j.baseline[i]
	}
	if j.updated[0] && j.updated[1] {
		j.phase = CheckCompleted
	} else if j.updated[0] || j.updated[1] {
		j.phase = CheckWaitingResult
	}
}

// earsInCase reads an ear status report: on when both AirPods are in the case, off when
// either is in an ear or out, unknown otherwise.
func earsInCase(packet []byte) Flag {
	if len(packet) < 8 || !bytes.Equal(packet[:6], []byte{4, 0, 4, 0, 6, 0}) || packet[6] > 4 || packet[7] > 4 {
		return FlagUnknown
	}
	left, right := packet[6], packet[7]
	switch {
	case left == 2 && right == 2:
		return FlagOn
	case left <= 1 || right <= 1:
		return FlagOff
	}
	return FlagUnknown
}

// checkFacts is what the in-case gate looks at.
type checkFacts struct {
	model, firmware string
	inCase          Flag
	earAge          time.Duration
	earsKnown       bool
	status          dongle.Status
	confirmed       bool
}

func checkGate(f checkFacts) error {
	switch {
	case (f.model != "A3063" && f.model != "A3064" && f.model != "A3065") || f.firmware != checkFirmware:
		return refuse("The in-case check is only verified on AirPods Pro 3 (A3063, A3064 or A3065) with firmware %s.", checkFirmware)
	case !f.confirmed || f.inCase != FlagOn || !f.earsKnown || f.earAge > earReportLife:
		return refuse("Both AirPods must be in the open case, not in your ears, and the case report must be recent.")
	case f.status.MicrophoneActive || f.status.USBPlayback || f.status.Streaming || f.status.PhoneConnected:
		return refuse("The in-case check needs the audio idle. Pause playback and recording and disconnect phone audio first.")
	}
	return nil
}

// observeCheckFacts reads the metadata, ear status and dongle status the gate needs.
func observeCheckFacts(o *operation, confirmed bool) (checkFacts, error) {
	facts := checkFacts{confirmed: confirmed}
	meta, err := identity(o)
	if err != nil {
		return facts, err
	}
	facts.model, facts.firmware = meta.Model, meta.Firmware
	report, err := o.Cached(dongle.SlotEarStatus)
	if err != nil {
		return facts, err
	}
	if report != nil {
		facts.earsKnown = true
		facts.inCase = earsInCase(report.Packet)
		facts.earAge = time.Duration(report.AgeMs) * time.Millisecond
	}
	facts.status, err = o.client.Status()
	return facts, err
}

func safeToCheck(o *operation, confirmed bool) error {
	if err := o.check(); err != nil {
		return err
	}
	facts, err := observeCheckFacts(o, confirmed)
	if err != nil {
		return err
	}
	return checkGate(facts)
}

// HardwareCheckReady reports whether RunHardwareCheck would start now. It sends nothing to
// the AirPods beyond taking the lease to read kept reports. nil means ready.
//
// Gating: needs model A3063, A3064 or A3065 on firmware exactly 9A348, enforced here, and the
// kept ear status in slot 5. Callers keep this behind the experimental switch.
// Blocking; run it on the device owner goroutine.
func HardwareCheckReady(c *dongle.Client, budsInCaseConfirmed bool) error {
	o, err := openOperation(c)
	if err != nil {
		return err
	}
	defer o.close()
	return safeToCheck(o, budsInCaseConfirmed)
}

// triggerCheck sends the request once. A lost reply leaves it uncertain whether the check
// began, so it is never sent twice.
func triggerCheck(exchange Exchanger) error {
	found, err := discover(exchange, []UUID{checkRequestUUID}, "Hardware check", propWrite)
	if err != nil {
		return err
	}
	if found[0] == nil {
		return refuse("These AirPods do not offer the hardware check request.")
	}
	if found[0].WriteOpcode != attWrite {
		return refuse("The hardware check request does not support a response.")
	}
	request := writeRequest(*found[0], []byte{'1'})
	response, err := exchange(request)
	if err != nil {
		return unconfirmedWrite("No reply to the hardware check request, so it may or may not have started.", err)
	}
	if err := writeAcknowledged("Hardware check request.", request, response); err != nil {
		return unconfirmed("The AirPods did not acknowledge the hardware check request.", err)
	}
	return nil
}

// HardwareCheckOptions controls RunHardwareCheck.
type HardwareCheckOptions struct {
	// BudsInCaseConfirmed is the user's confirmation that both AirPods sit in the open case.
	BudsInCaseConfirmed bool
	// Stop is polled while waiting; true stops waiting. No cancel command is known, so this
	// only ends what the app is watching: the AirPods may still finish the check.
	Stop func() bool
	// Progress is called on the same goroutine whenever the state changes, and about once a
	// second with the time left.
	Progress func(CheckProgress)
	// TolerateLinkLoss keeps waiting when a status read fails, for a caller whose device can
	// be reopened between steps. The time limit still ends the wait.
	TolerateLinkLoss bool
}

// RunHardwareCheck asks the AirPods for one in-case self-check and waits up to 120 s for both
// ears' saved results to change. The AirPods disconnect when the case closes, and that is
// expected: the wait goes on, and readings resume when the same pair reconnects. It ends on
// new results, on timeout, on Stop, on a different pair connecting, or on a read failing while
// the connection holds. The returned progress is the final state.
//
// A precondition failure is a *Refused before the request is sent. A request with no
// acknowledgement is *dongle.Unconfirmed. Neither is ever retried, and the phase after the
// request is reported rather than returned as an error unless the phase is CheckError.
//
// Gating: as for StartHardwareCheck. UNVERIFIED on real AirPods. Blocking for up to about 2
// minutes with exclusive use of the client, so it must not run as one device owner job: the
// owner could not poll status or serve its queue for that long. The window uses
// StartHardwareCheck and one job per Step instead.
func RunHardwareCheck(c *dongle.Client, options HardwareCheckOptions) (CheckProgress, error) {
	check, progress, err := StartHardwareCheck(c, options)
	if err != nil {
		return progress, err
	}
	for {
		progress, done, err := check.Step(c)
		if done {
			return progress, err
		}
		check.clock.Sleep(checkTick)
	}
}

// HardwareCheck follows one requested check. Its methods must not run concurrently: one
// call at a time, each as its own short device owner job, with the owner free in between.
type HardwareCheck struct {
	job        *checkJob
	clock      dongle.Clock
	peer       [6]byte
	model      string
	record     *Diagnostics
	options    HardwareCheckOptions
	started    time.Time
	lastRead   time.Duration
	lastReport time.Duration
	phase      CheckPhase
	updated    [2]bool
}

// StartHardwareCheck checks the preconditions and sends the request once, under one lease
// that is released before it returns, so the case closing cannot strand it. Later readings
// each take their own short lease in Step.
//
// A precondition failure is a *Refused before the request is sent. A request with no
// acknowledgement is *dongle.Unconfirmed, with the progress in CheckError. Neither is retried.
//
// Gating: needs model A3063, A3064 or A3065 on firmware exactly 9A348, both AirPods in the case
// per slot 5 with a report at most 30 s old, and no playback, recording or phone audio; all
// enforced here. Callers keep this behind the experimental switch. UNVERIFIED on real AirPods.
// Blocking for a moment; run it on the device owner goroutine.
func StartHardwareCheck(c *dongle.Client, options HardwareCheckOptions) (*HardwareCheck, CheckProgress, error) {
	confirmed := options.BudsInCaseConfirmed
	o, err := openOperation(c)
	if err != nil {
		return nil, CheckProgress{}, err
	}
	defer o.close()
	if err := safeToCheck(o, confirmed); err != nil {
		return nil, CheckProgress{}, err
	}
	meta, err := identity(o)
	if err != nil {
		return nil, CheckProgress{}, err
	}
	baseline, err := ReadDiagnostics(o.Exchange, meta.Model, o.clock.Now())
	if err != nil {
		return nil, CheckProgress{}, err
	}
	if baseline == nil {
		return nil, CheckProgress{}, refuse("Read the saved hardware results before starting a check.")
	}
	if err := safeToCheck(o, confirmed); err != nil {
		return nil, CheckProgress{}, err
	}
	h := &HardwareCheck{
		job: newCheckJob(*baseline), clock: o.clock, peer: o.start.Peer, model: meta.Model,
		record: baseline, options: options,
	}
	if options.Progress != nil {
		options.Progress(h.report(0))
	}
	err = triggerCheck(func(pdu []byte) ([]byte, error) {
		if err := safeToCheck(o, confirmed); err != nil {
			return nil, dongle.NotSent(err)
		}
		return o.Exchange(pdu)
	})
	if err != nil {
		h.job.fail(err)
		return nil, h.report(0), err
	}
	h.started = o.clock.Now()
	return h, h.report(0), nil
}

func (h *HardwareCheck) report(elapsed time.Duration) CheckProgress {
	j := h.job
	return CheckProgress{
		Phase: j.phase, LeftUpdated: j.updated[0], RightUpdated: j.updated[1], Message: j.message,
		Elapsed: elapsed, Remaining: max(checkWait-elapsed, 0), Record: h.record,
	}
}

// Step looks once at the check and returns at once; it never waits. done means the check has
// ended, with the final progress and, only for CheckError, an error. It sends nothing but
// status reads and, every 2 s while the link is up, one short leased read of the results.
//
// A client whose context has ended stops the check as Stop does: the AirPods may still finish.
//
// Blocking for one read at most; run it on the device owner goroutine.
func (h *HardwareCheck) Step(c *dongle.Client) (CheckProgress, bool, error) {
	elapsed := h.clock.Now().Sub(h.started)
	if c.Context().Err() != nil {
		h.job.stop()
	}
	if h.stepBegin(elapsed) {
		status, err := c.Status()
		switch {
		case err != nil && c.Context().Err() != nil:
			h.job.stop()
		case err != nil && h.options.TolerateLinkLoss:
			h.job.waiting()
		case err != nil:
			h.job.fail(err)
		case !status.Connected:
			h.job.waiting()
		case status.Peer != h.peer:
			h.job.fail(errors.New("Different AirPods connected, so this check is no longer being followed."))
		case status.ControlsReady && !status.MicrophoneActive && elapsed-h.lastRead >= checkPoll:
			h.lastRead = elapsed
			h.readResults(c, status, elapsed)
		}
	}
	return h.stepEnd(elapsed)
}

// Absent stands in for Step when the owner has no device to run it with, which the dongle
// re-enumerating causes. The time limit and Stop still apply; nothing touches the device.
func (h *HardwareCheck) Absent() (CheckProgress, bool, error) {
	elapsed := h.clock.Now().Sub(h.started)
	if h.stepBegin(elapsed) {
		h.job.waiting()
	}
	return h.stepEnd(elapsed)
}

func (h *HardwareCheck) stepBegin(elapsed time.Duration) bool {
	if h.options.Stop != nil && h.options.Stop() {
		h.job.stop()
	}
	h.job.tick(elapsed)
	return h.job.active()
}

func (h *HardwareCheck) readResults(c *dongle.Client, status dongle.Status, elapsed time.Duration) {
	latest, err := readCheckRecord(c, h.model, h.clock.Now())
	if latest != nil {
		h.job.observe(*latest)
		h.record = latest
	}
	if err == nil {
		return
	}
	// Another client holding the channel says nothing about the check. Look again next time,
	// within the time limit, as a mode change does.
	var rejected *dongle.RequestRejected
	if errors.As(err, &rejected) && rejected.Code == 2 {
		h.job.waiting()
		return
	}
	// An error across a reconnect is expected. One on a steady link is not.
	var refusal *Refused
	now, statusErr := c.Status()
	steady := statusErr == nil && now.Generation == status.Generation && now.Connected
	if steady && !errors.As(err, &refusal) {
		h.job.fail(fmt.Errorf("Could not read the hardware check result: %w", err))
	}
}

func (h *HardwareCheck) stepEnd(elapsed time.Duration) (CheckProgress, bool, error) {
	j := h.job
	progress := h.report(elapsed)
	if h.options.Progress != nil && (j.phase != h.phase || j.updated != h.updated || elapsed-h.lastReport >= time.Second) {
		h.options.Progress(progress)
		h.phase, h.updated, h.lastReport = j.phase, j.updated, elapsed
	}
	if j.active() {
		return progress, false, nil
	}
	if j.phase == CheckError {
		return progress, true, j.err
	}
	return progress, true, nil
}

func readCheckRecord(c *dongle.Client, model string, now time.Time) (*Diagnostics, error) {
	o, err := openOperation(c)
	if err != nil {
		return nil, err
	}
	defer o.close()
	return ReadDiagnostics(o.Exchange, model, now)
}
