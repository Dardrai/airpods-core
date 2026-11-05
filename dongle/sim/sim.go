// Package sim is an in-memory dongle implementing hid.Device: reports 2 and 3 with
// typed requests, commit, pagination, generation changes, settings with revisions
// and ages, control receipts, and injectable faults. It needs no hardware.
package sim

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Dardrai/airpods-core/hid"
)

// Hex is a byte string written as hex in JSON.
type Hex []byte

// UnmarshalJSON implements json.Unmarshaler.
func (h *Hex) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	decoded, err := hex.DecodeString(text)
	*h = decoded
	return err
}

// Setting is one cached setting. Revision 0 is treated as malformed by clients.
type Setting struct {
	Control  byte   `json:"control"`
	Age      uint32 `json:"age"`
	Revision uint32 `json:"revision"`
	Value    uint32 `json:"value"`
}

// Write modes for setting writes (Config.WriteMode).
const (
	WriteApply        = ""              // applied with a new revision
	WriteLost         = "lost"          // accepted, but never reaches the cache
	WriteSameRevision = "same-revision" // value applied, revision unchanged
	WriteOtherValue   = "other-value"   // new revision, but a different value
)

// Tamper modes corrupt the typed response (Config.Tamper).
const (
	TamperOversizedPage = "oversized-page" // page size above 42
	TamperTotalChanges  = "total-changes"  // second page declares another total
	TamperEmptyPage     = "empty-page"     // page size zero with data outstanding
	TamperExcessPage    = "excess-page"    // first page holds more than the total
	TamperHugeTotal     = "huge-total"     // total above 4096
)

// maxFragment is the most a report 3 fragment can carry.
const maxFragment = 42

// Config is the simulated device's state and faults; the zero value of a fault disables it.
type Config struct {
	Serial    string `json:"serial"`
	OpenError string `json:"open_error"`

	Flags        byte      `json:"flags"`
	Generation   uint32    `json:"generation"`
	Peer         Hex       `json:"peer"`
	ANC          byte      `json:"anc"`
	Battery      [3]byte   `json:"battery"`
	Charging     byte      `json:"charging"`
	Codec        byte      `json:"codec"`
	Ages         [3]uint32 `json:"ages"`
	Capabilities uint16    `json:"capabilities"`
	Present      byte      `json:"present"`
	CaseSource   byte      `json:"case_source"`
	Settings     []Setting `json:"settings"`

	// Replies overrides the reply payload of a typed request kind.
	Replies map[byte]Hex `json:"replies"`
	// Fail makes a typed request kind end in phase 4 with this code.
	Fail map[byte]byte `json:"fail"`
	// PageSize is the response fragment size, 42 when zero.
	PageSize int `json:"page_size"`
	// Tamper corrupts typed responses; see the Tamper constants.
	Tamper string `json:"tamper"`
	// InitialPhase is report 3's phase before any request (1 or 2 means busy).
	InitialPhase byte `json:"initial_phase"`
	// InitialSequence is report 3's sequence before any request.
	InitialSequence uint32 `json:"initial_sequence"`
	// ForeignSequence makes report 3 answer with a sequence other than the request's.
	ForeignSequence bool `json:"foreign_sequence"`
	// StallStage keeps a staged request from ever being acknowledged.
	StallStage bool `json:"stall_stage"`
	// StallCommit keeps a commit in phase 2 forever.
	StallCommit bool `json:"stall_commit"`
	// CommitDelay is how many report 3 reads a commit stays in phase 2.
	CommitDelay int `json:"commit_delay"`

	// WriteMode selects what a setting write does; see the Write constants.
	WriteMode string `json:"write_mode"`
	// ApplyAfterReads delays a write until this many snapshot reads have happened.
	ApplyAfterReads int `json:"apply_after_reads"`
	// WriteReply is a non-empty reply to a setting write.
	WriteReply Hex `json:"write_reply"`
	// SnapshotFailAfterWrite fails snapshot reads with this code once a write happened.
	SnapshotFailAfterWrite byte `json:"snapshot_fail_after_write"`
	// SnapshotGarbageAfterWrite returns a malformed snapshot once a write happened.
	SnapshotGarbageAfterWrite bool `json:"snapshot_garbage_after_write"`

	// GenerationChangeAtGet bumps the generation on this 1-based Get.
	GenerationChangeAtGet int `json:"generation_change_at_get"`
	// GenerationChangeAfterWrite bumps the generation when a setting write commits.
	GenerationChangeAfterWrite bool `json:"generation_change_after_write"`
	// GetFailAt fails this 1-based Get with FailMessage.
	GetFailAt int `json:"get_fail_at"`
	// SetFailAt fails this 1-based Set with FailMessage.
	SetFailAt int `json:"set_fail_at"`
	// SetFailOnWrite fails the Set that stages a setting write with FailMessage.
	SetFailOnWrite bool `json:"set_fail_on_write"`
	// GetFailAfterWrite fails every Get after a setting write with FailMessage.
	GetFailAfterWrite bool `json:"get_fail_after_write"`
	// FailMessage is the text of injected transport errors.
	FailMessage string `json:"fail_message"`
	// BadIdentityReport makes this report fail its identity check; 0 disables.
	BadIdentityReport byte `json:"bad_identity_report"`

	// FailInspectStop fails only the request ending the inspection lease with this code.
	FailInspectStop byte `json:"fail_inspect_stop"`

	// ReceiptSequence is report 2's receipt sequence before any control command.
	ReceiptSequence uint32 `json:"receipt_sequence"`
	// ControlResults overrides the receipt result of an opcode.
	ControlResults map[byte]byte `json:"control_results"`
	// ControlDelay is how many report 2 reads a receipt stays invisible.
	ControlDelay int `json:"control_delay"`
	// ControlNoReceipt never publishes a receipt.
	ControlNoReceipt bool `json:"control_no_receipt"`
	// Exchange answers ATT requests, which the sim otherwise fails.
	Exchange func(pdu []byte) []byte `json:"-"`
	// OnAAP observes AAP packets sent to the AirPods.
	OnAAP func(pdu []byte) `json:"-"`
	// SettingsNeedReady refuses the settings snapshot as unsupported until controls are
	// ready, as a dongle in the middle of connecting could.
	SettingsNeedReady bool `json:"-"`
	// FailTimes refuses a typed request kind as unsupported this many times, then serves it.
	FailTimes map[byte]int `json:"-"`
	// Cached holds the AirPods reports the dongle has kept, by slot.
	Cached map[byte]CachedReport `json:"-"`
	// DisconnectOpcode, when nonzero, is an opcode that ends the AirPods connection;
	// reconnect then brings it back. Capabilities are the config's to advertise.
	DisconnectOpcode byte `json:"disconnect_opcode"`
}

// CachedReport is one kept report: how old it is, its reception revision and its packet.
type CachedReport struct {
	Age      uint32
	Revision uint32
	Packet   []byte
}

// Default is a connected dongle with settings ready, a few settings and no faults.
func Default() Config {
	return Config{
		Serial:       "SIM0001",
		Flags:        0x03,
		Generation:   7,
		Peer:         Hex{1, 2, 3, 4, 5, 6},
		ANC:          2,
		Battery:      [3]byte{80, 70, 255},
		Codec:        2,
		Ages:         [3]uint32{1000, 1000, 0xffffffff},
		Capabilities: 0x7f,
		Settings: []Setting{
			{Control: 0x0d, Age: 1200, Revision: 5, Value: 2},
			{Control: 0x0a, Age: 300, Revision: 2, Value: 2},
			{Control: 0x1f, Age: 40, Revision: 9, Value: 0x5032},
			{Control: 0x16, Age: 7, Revision: 1, Value: 0x0501},
		},
	}
}

// ParseConfig applies JSON overrides on top of Default.
func ParseConfig(data []byte) (Config, error) {
	cfg := Default()
	if len(data) == 0 {
		return cfg, nil
	}
	return cfg, json.Unmarshal(data, &cfg)
}

// Device is the simulated dongle.
type Device struct {
	mu  sync.Mutex
	cfg Config

	gets, sets int
	closed     bool
	trace      []string
	commits    [][]byte
	controls   [][hid.ReportSize]byte
	inspecting bool

	// OnControl, when set, decides a control receipt result for an opcode.
	OnControl func(opcode, arg byte) byte

	reply        [hid.ReportSize]byte
	staged       []byte
	stagedOp     byte
	nearbyOn     bool
	leaseHeld    bool
	exchanges    [][]byte
	aapSent      [][]byte
	leaseEvents  []string
	response     []byte
	phase2Reads  int
	pendingReply func()

	written      bool
	writes       int
	pendingWrite *Setting
	applyIn      int

	receiptSeq     uint32
	receiptResult  byte
	receiptOpcode  byte
	receiptPending func()
	receiptIn      int
}

// New builds a device from a config.
func New(cfg Config) *Device {
	d := &Device{cfg: cfg, receiptSeq: cfg.ReceiptSequence}
	d.cfg.Settings = append([]Setting(nil), cfg.Settings...)
	header := hid.Header(3)
	copy(d.reply[:], header[:])
	binary.LittleEndian.PutUint32(d.reply[8:], cfg.Generation)
	binary.LittleEndian.PutUint32(d.reply[12:], cfg.InitialSequence)
	d.reply[21] = cfg.InitialPhase
	return d
}

// Open applies the exactly-one-match rules to this single simulated dongle.
func (d *Device) Open(serial string) (hid.Device, error) {
	if d.cfg.OpenError != "" {
		return nil, errors.New(d.cfg.OpenError)
	}
	if serial != "" && serial != d.cfg.Serial {
		return nil, errors.New("Expected exactly one matching Pico HID interface (CAFE:4015/4005)")
	}
	return d, nil
}

// Serial implements hid.Device.
func (d *Device) Serial() string { return d.cfg.Serial }

// Close implements hid.Device.
func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	return nil
}

// Closed reports whether Close was called.
func (d *Device) Closed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

// Commits returns every committed typed request payload, in order.
func (d *Device) Commits() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]byte(nil), d.commits...)
}

// Controls returns every control report received, in order.
func (d *Device) Controls() [][hid.ReportSize]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][hid.ReportSize]byte(nil), d.controls...)
}

// Writes counts committed setting writes.
func (d *Device) Writes() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writes
}

// Inspecting reports whether the inspection lease is held.
func (d *Device) Inspecting() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inspecting
}

// SetFlags changes the status flags byte while running.
func (d *Device) SetFlags(flags byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg.Flags = flags
}

// BumpGeneration starts a new connection generation, as a reconnect would.
func (d *Device) BumpGeneration() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg.Generation++
}

// TraceSummary returns the length and a SHA-256 of every Get and Set seen, in order.
func (d *Device) TraceSummary() (int, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := sha256.New()
	for _, line := range d.trace {
		h.Write([]byte(line))
		h.Write([]byte{'\n'})
	}
	return len(d.trace), hex.EncodeToString(h.Sum(nil))
}

func (d *Device) failure() error {
	if d.cfg.FailMessage != "" {
		return errors.New(d.cfg.FailMessage)
	}
	return errors.New("simulated transport failure")
}

// Get implements hid.Device.
func (d *Device) Get(report byte) ([hid.ReportSize]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var data [hid.ReportSize]byte
	if d.closed {
		return data, errors.New("simulated device is closed")
	}
	d.gets++
	d.trace = append(d.trace, fmt.Sprintf("G%d", report))
	if d.cfg.GenerationChangeAtGet == d.gets {
		d.cfg.Generation++
	}
	if d.cfg.GetFailAt == d.gets || (d.cfg.GetFailAfterWrite && d.written) {
		return data, d.failure()
	}
	switch report {
	case 2:
		data = d.status()
	case 3:
		if d.phase2Reads > 0 {
			d.phase2Reads--
			if d.phase2Reads == 0 && d.pendingReply != nil {
				d.pendingReply()
				d.pendingReply = nil
			}
		}
		data = d.reply
	default:
		header := hid.Header(report)
		copy(data[:], header[:])
	}
	if d.cfg.BadIdentityReport == report {
		data[1] = 'X'
	}
	if err := hid.CheckReport(report, data); err != nil {
		return data, err
	}
	return data, nil
}

func (d *Device) status() [hid.ReportSize]byte {
	if d.receiptPending != nil {
		if d.receiptIn <= 0 {
			d.receiptPending()
			d.receiptPending = nil
		} else {
			d.receiptIn--
		}
	}
	var data [hid.ReportSize]byte
	header := hid.Header(2)
	copy(data[:], header[:])
	le := binary.LittleEndian
	data[7] = d.cfg.Flags
	le.PutUint32(data[8:], d.cfg.Generation)
	copy(data[12:18], d.cfg.Peer)
	data[18] = d.cfg.ANC
	copy(data[19:22], d.cfg.Battery[:])
	data[22] = d.cfg.Charging
	data[23] = d.cfg.Codec
	le.PutUint32(data[24:], d.cfg.Ages[0])
	le.PutUint32(data[28:], d.cfg.Ages[1])
	le.PutUint32(data[32:], d.cfg.Ages[2])
	le.PutUint32(data[36:], d.receiptSeq)
	data[40] = d.receiptResult
	data[41] = d.receiptOpcode
	le.PutUint16(data[42:], d.cfg.Capabilities)
	data[44] = d.cfg.Present
	data[45] = d.cfg.CaseSource
	return data
}

// Set implements hid.Device.
func (d *Device) Set(data [hid.ReportSize]byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("simulated device is closed")
	}
	d.sets++
	d.trace = append(d.trace, "S"+hex.EncodeToString(data[:]))
	if d.cfg.SetFailAt == d.sets {
		return d.failure()
	}
	if err := hid.CheckReport(data[0], data); err != nil {
		return err
	}
	switch data[0] {
	case 2:
		d.control(data)
	case 3:
		if d.cfg.SetFailOnWrite && data[7] == 7 && data[20] > 0 && data[22] == 1 {
			return d.failure()
		}
		d.typed(data)
	}
	return nil
}

func (d *Device) control(data [hid.ReportSize]byte) {
	d.controls = append(d.controls, data)
	le := binary.LittleEndian
	opcode, arg := data[7], data[22]
	sequence := le.Uint32(data[18:])
	result := ResultFor(opcode)
	if d.cfg.DisconnectOpcode != 0 && opcode == d.cfg.DisconnectOpcode {
		result = 1
	}
	switch {
	case d.OnControl != nil:
		result = d.OnControl(opcode, arg)
	case d.cfg.ControlResults[opcode] != 0:
		result = d.cfg.ControlResults[opcode]
	case sequence == 0:
		result = 6
	case le.Uint32(data[8:]) != d.cfg.Generation || string(data[12:18]) != string(d.cfg.Peer):
		result = 3
	}
	if d.cfg.ControlNoReceipt {
		return
	}
	d.receiptIn = d.cfg.ControlDelay
	d.receiptPending = func() {
		d.receiptSeq, d.receiptResult, d.receiptOpcode = sequence, result, opcode
		if result != 1 {
			return
		}
		if d.cfg.DisconnectOpcode != 0 {
			switch opcode {
			case d.cfg.DisconnectOpcode:
				d.cfg.Flags &^= 0x07
			case 1:
				d.cfg.Flags |= 0x03
			}
		}
		switch opcode {
		case 2:
			d.cfg.Generation++ // Pairing starts a new connection generation.
		case 3:
			d.cfg.ANC = arg
		case 4:
			d.cfg.Flags |= 32
		}
	}
}

// ResultFor is the default receipt: known opcodes succeed, others are unsupported.
func ResultFor(opcode byte) byte {
	if opcode >= 1 && opcode <= 4 {
		return 1
	}
	return 4
}

func (d *Device) typed(data [hid.ReportSize]byte) {
	le := binary.LittleEndian
	op := data[7]
	if le.Uint32(data[8:]) != d.cfg.Generation {
		d.fail(data, 3)
		return
	}
	switch op {
	case 1, 2, 3, 7:
		size := int(data[20])
		offset := int(le.Uint16(data[18:]))
		if size > maxFragment {
			d.fail(data, 1)
			return
		}
		if offset == 0 {
			d.staged = nil
		}
		if offset != len(d.staged) {
			d.fail(data, 1)
			return
		}
		d.staged = append(d.staged, data[22:22+size]...)
		d.stagedOp = op
		if d.cfg.StallStage {
			return
		}
		d.reply = data
		le.PutUint16(d.reply[18:], uint16(offset+size))
		d.reply[20] = 0
		d.reply[21] = 1
		clear(d.reply[22:])
	case 6:
		d.commit(data)
	case 4:
		offset := int(le.Uint16(data[18:]))
		d.page(data, offset)
	case 5:
		// A commit still in phase 2 is dropped with the request, not finished later.
		d.phase2Reads, d.pendingReply = 0, nil
		d.fail(data, 8)
	}
	if d.cfg.ForeignSequence {
		le.PutUint32(d.reply[12:], le.Uint32(data[12:])+100)
	}
}

func (d *Device) fail(request [hid.ReportSize]byte, code byte) {
	d.reply = request
	binary.LittleEndian.PutUint16(d.reply[16:], 1)
	clear(d.reply[18:])
	d.reply[20] = 1
	d.reply[21] = 4
	d.reply[22] = code
}

func (d *Device) commit(request [hid.ReportSize]byte) {
	d.commits = append(d.commits, append([]byte(nil), d.staged...))
	finish := func() {
		kind := byte(0)
		if len(d.staged) > 0 && d.stagedOp == 7 {
			kind = d.staged[0]
		}
		if d.stagedOp == 7 && kind == 2 && d.cfg.SettingsNeedReady && d.cfg.Flags&2 == 0 {
			d.fail(request, 7)
			return
		}
		if d.stagedOp == 7 && d.cfg.FailTimes[kind] > 0 {
			d.cfg.FailTimes[kind]--
			d.fail(request, 7)
			return
		}
		if code := d.cfg.Fail[kind]; code != 0 {
			d.fail(request, code)
			return
		}
		response, code := d.respond()
		if code != 0 {
			d.fail(request, code)
			return
		}
		d.response = response
		d.page(request, 0)
	}
	if d.cfg.StallCommit || d.cfg.CommitDelay > 0 {
		d.reply = request
		clear(d.reply[16:])
		d.reply[21] = 2
		if !d.cfg.StallCommit {
			d.phase2Reads = d.cfg.CommitDelay
			d.pendingReply = finish
		}
		return
	}
	finish()
}

func (d *Device) page(request [hid.ReportSize]byte, offset int) {
	le := binary.LittleEndian
	size := d.cfg.PageSize
	if size <= 0 {
		size = maxFragment
	}
	total := len(d.response)
	if offset > total {
		d.fail(request, 1)
		return
	}
	count := min(size, total-offset)
	d.reply = request
	clear(d.reply[16:])
	le.PutUint16(d.reply[16:], uint16(total))
	le.PutUint16(d.reply[18:], uint16(offset))
	d.reply[20] = byte(count)
	d.reply[21] = 3
	copy(d.reply[22:], d.response[offset:offset+count])
	if total == 0 {
		return
	}
	switch d.cfg.Tamper {
	case TamperOversizedPage:
		d.reply[20] = 43
	case TamperTotalChanges:
		if offset > 0 {
			le.PutUint16(d.reply[16:], uint16(total+1))
		}
	case TamperEmptyPage:
		d.reply[20] = 0
	case TamperExcessPage:
		le.PutUint16(d.reply[16:], 1)
	case TamperHugeTotal:
		le.PutUint16(d.reply[16:], 4097)
	}
}

// respond computes a committed request's reply, or a phase 4 code.
func (d *Device) respond() ([]byte, byte) {
	request := d.staged
	if len(request) == 0 {
		return nil, 1
	}
	switch d.stagedOp {
	case 3:
		return d.cachedReply(request)
	case 1, 2:
		if !d.leaseHeld {
			return nil, 3
		}
		if d.stagedOp == 2 {
			d.aapSent = append(d.aapSent, append([]byte(nil), request...))
			if d.cfg.OnAAP != nil {
				d.cfg.OnAAP(request)
			}
			return nil, 0
		}
		d.exchanges = append(d.exchanges, append([]byte(nil), request...))
		if d.cfg.Exchange == nil {
			return nil, 7
		}
		return d.cfg.Exchange(request), 0
	}
	kind := request[0]
	if kind == 2 {
		return d.snapshot()
	}
	if kind == 1 {
		return d.writeSetting(request)
	}
	if kind == 14 && len(request) == 2 {
		if request[1] == 0 && d.cfg.FailInspectStop != 0 {
			return nil, d.cfg.FailInspectStop
		}
		d.inspecting = request[1] == 1
	}
	switch kind {
	case 6:
		d.nearbyOn = true
	case 8:
		d.nearbyOn = false
	}
	if reply, ok := d.cfg.Replies[kind]; ok {
		out := append([]byte(nil), reply...)
		if kind == 7 && !d.nearbyOn && len(out) > 1 {
			out[1] &= 0x80 // A kept finder reply reads as idle until the finder starts.
		}
		return out, 0
	}
	switch kind {
	case 3, 4, 9:
		return defaultAudio(), 0
	case 5:
		return defaultPerformance(), 0
	case 6, 7:
		return defaultNearby(kind == 6), 0
	case 8, 12, 14, 18:
		return nil, 0
	case 10:
		reply, code := lease(request)
		if code == 0 {
			action := request[1]
			d.leaseHeld = action != 2
			d.leaseEvents = append(d.leaseEvents, [...]string{"acquire", "renew", "release"}[min(action, 2)])
		}
		return reply, code
	}
	return nil, 7
}

// cachedReply is the envelope of a kept report, empty when the slot holds nothing.
func (d *Device) cachedReply(request []byte) ([]byte, byte) {
	if len(request) != 1 || request[0] < 1 || request[0] > 5 {
		return nil, 1
	}
	report, ok := d.cfg.Cached[request[0]]
	if !ok {
		return nil, 0
	}
	out := binary.LittleEndian.AppendUint32(nil, report.Age)
	out = binary.LittleEndian.AppendUint32(out, report.Revision)
	return append(out, report.Packet...), 0
}

func (d *Device) snapshot() ([]byte, byte) {
	if d.written && d.cfg.SnapshotFailAfterWrite != 0 {
		return nil, d.cfg.SnapshotFailAfterWrite
	}
	if d.written && d.cfg.SnapshotGarbageAfterWrite {
		return []byte{9, 9}, 0
	}
	if reply, ok := d.cfg.Replies[2]; ok {
		return append([]byte(nil), reply...), 0
	}
	if d.pendingWrite != nil {
		if d.applyIn <= 0 {
			d.applyWrite(*d.pendingWrite)
			d.pendingWrite = nil
		} else {
			d.applyIn--
		}
	}
	out := []byte{1, byte(len(d.cfg.Settings))}
	for _, s := range d.cfg.Settings {
		out = append(out, s.Control)
		out = binary.LittleEndian.AppendUint32(out, s.Age)
		out = binary.LittleEndian.AppendUint32(out, s.Revision)
		out = binary.LittleEndian.AppendUint32(out, s.Value)
	}
	return out, 0
}

func (d *Device) applyWrite(write Setting) {
	if write.Control == 0x0d && write.Value >= 1 && write.Value <= 4 {
		d.cfg.ANC = byte(write.Value) // The status report follows the mode setting.
	}
	for i := range d.cfg.Settings {
		s := &d.cfg.Settings[i]
		if s.Control != write.Control {
			continue
		}
		switch d.cfg.WriteMode {
		case WriteSameRevision:
			s.Value = write.Value
		case WriteOtherValue:
			s.Revision++
			s.Value = write.Value + 1
		default:
			s.Revision++
			s.Value = write.Value
		}
		s.Age = 0
		return
	}
	value := write.Value
	if d.cfg.WriteMode == WriteOtherValue {
		value++
	}
	d.cfg.Settings = append(d.cfg.Settings, Setting{Control: write.Control, Revision: 1, Value: value})
}

func (d *Device) writeSetting(request []byte) ([]byte, byte) {
	if len(request) != 6 {
		return nil, 1
	}
	d.written = true
	d.writes++
	if d.cfg.GenerationChangeAfterWrite {
		d.cfg.Generation++
	}
	if d.cfg.WriteMode != WriteLost {
		d.pendingWrite = &Setting{Control: request[1], Value: binary.LittleEndian.Uint32(request[2:])}
		d.applyIn = d.cfg.ApplyAfterReads
	}
	return append([]byte(nil), d.cfg.WriteReply...), 0
}

func defaultAudio() []byte {
	reply := make([]byte, 20)
	reply[0] = 1
	reply[1], reply[2] = 2, 2
	binary.LittleEndian.PutUint32(reply[4:], 192000)
	binary.LittleEndian.PutUint32(reply[8:], 192000)
	reply[14] = 1
	binary.LittleEndian.PutUint16(reply[16:], 0xffff)
	binary.LittleEndian.PutUint16(reply[18:], 100)
	return reply
}

func defaultPerformance() []byte {
	reply := make([]byte, 64)
	reply[0] = 1
	for i := 4; i < 64; i += 4 {
		binary.LittleEndian.PutUint32(reply[i:], uint32(i))
	}
	binary.LittleEndian.PutUint32(reply[12:], 4000)
	binary.LittleEndian.PutUint32(reply[48:], 9000)
	return reply
}

func defaultNearby(active bool) []byte {
	reply := make([]byte, 24)
	reply[0] = 1
	reply[1] = 0x80
	if active {
		reply[1] = 0x81
	}
	reply[2] = 127
	binary.LittleEndian.PutUint32(reply[4:], 0xffffffff)
	binary.LittleEndian.PutUint32(reply[20:], 0x80000000)
	return reply
}

func lease(request []byte) ([]byte, byte) {
	if len(request) != 6 {
		return nil, 1
	}
	if request[1] == 0 {
		reply := []byte{1, 0, 0, 0, 0}
		binary.LittleEndian.PutUint32(reply[1:], 0x76543210)
		return reply, 0
	}
	if binary.LittleEndian.Uint32(request[2:]) != 0x76543210 {
		return nil, 3
	}
	return nil, 0
}

// VirtualClock advances only when slept on, so timeout paths run instantly and
// deterministically. It implements the dongle package's Clock interface.
type VirtualClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewVirtualClock starts at a fixed instant.
func NewVirtualClock() *VirtualClock {
	return &VirtualClock{now: time.Unix(1700000000, 250000000)}
}

// Now implements Clock.
func (c *VirtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Sleep implements Clock.
func (c *VirtualClock) Sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
