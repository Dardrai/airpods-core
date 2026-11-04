package dongle

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"

	"github.com/Dardrai/airpods-core/hid"
)

// Kind selects how a setting's value is encoded.
type Kind int

// Setting kinds.
const (
	KindNamed Kind = iota
	KindPercent
	KindModes
	KindActions
)

// NamedValue is one word of a named setting. Order is the display order.
type NamedValue struct {
	Word  string
	Value uint32
}

// Setting is one row of the settings table.
type Setting struct {
	Name    string
	Control byte
	Kind    Kind
	Values  []NamedValue // KindNamed only
}

const toneVolumeControl = 0x1f

// holdActionLeftShift and holdActionRightShift place each ear's hold action in the
// setting's value. They are the only statement of that order: swap them to flip it.
const (
	holdActionLeftShift  = 0
	holdActionRightShift = 8
)

var (
	modeValues = []NamedValue{{"off", 1}, {"anc", 2}, {"transparency", 3}, {"adaptive", 4}}
	toggle     = []NamedValue{{"on", 1}, {"off", 2}}
	holdValues = []NamedValue{{"noise-control", 1}, {"siri", 5}}
)

// Settings is the CLI's table, in display order.
var Settings = []Setting{
	{Name: "mode", Control: 0x0d, Values: modeValues},
	{Name: "microphone", Control: 0x01, Values: []NamedValue{{"auto", 0}, {"right", 1}, {"left", 2}}},
	{Name: "ear-detection", Control: 0x0a, Values: []NamedValue{{"on", 2}, {"off", 1}}},
	{Name: "one-bud-anc", Control: 0x1b, Values: toggle},
	{Name: "conversation-awareness", Control: 0x28, Values: toggle},
	{Name: "personalized-volume", Control: 0x26, Values: toggle},
	{Name: "allow-off", Control: 0x34, Values: toggle},
	{Name: "pause-when-asleep", Control: 0x35, Values: toggle},
	{Name: "case-sounds", Control: 0x31, Values: toggle},
	{Name: "optimized-charging", Control: 0x3b, Values: toggle},
	{Name: "volume-swipe", Control: 0x25, Values: toggle},
	{Name: "press-speed", Control: 0x17, Values: []NamedValue{{"default", 0}, {"slower", 1}, {"slowest", 2}}},
	{Name: "hold-duration", Control: 0x18, Values: []NamedValue{{"default", 0}, {"slower", 1}, {"slowest", 2}}},
	{Name: "swipe-duration", Control: 0x23, Values: []NamedValue{{"default", 0}, {"longer", 1}, {"longest", 2}}},
	{Name: "call-controls", Control: 0x24, Values: []NamedValue{{"default", 0x0300}, {"swapped", 0x0200}}},
	{Name: "crown-direction", Control: 0x1c, Values: []NamedValue{{"default", 2}, {"reversed", 1}}},
	{Name: "tone-volume", Control: toneVolumeControl, Kind: KindPercent},
	{Name: "case-volume", Control: 0x40, Kind: KindPercent},
	{Name: "adaptive-strength", Control: 0x2e, Kind: KindPercent},
	{Name: "hold-modes", Control: 0x1a, Kind: KindModes},
	{Name: "hold-actions", Control: 0x16, Kind: KindActions},
}

// ExtraSettings are settings kept apart from Settings, in the same form. The CLI and the tray
// both list and accept them; LookupAnySetting finds them.
var ExtraSettings = []Setting{
	{Name: "connect-automatically", Control: 0x20, Values: toggle},
	{Name: "allow-auto-connect", Control: 0x36, Values: toggle},
}

// LookupSetting finds a row of Settings by name.
func LookupSetting(name string) (*Setting, bool) { return lookupIn(Settings, name) }

// LookupAnySetting finds a row of Settings or ExtraSettings by name.
func LookupAnySetting(name string) (*Setting, bool) {
	if s, ok := lookupIn(Settings, name); ok {
		return s, ok
	}
	return lookupIn(ExtraSettings, name)
}

func lookupIn(table []Setting, name string) (*Setting, bool) {
	for i := range table {
		if table[i].Name == name {
			return &table[i], true
		}
	}
	return nil, false
}

func joinWords(values []NamedValue) string {
	words := make([]string, len(values))
	for i, v := range values {
		words[i] = v.Word
	}
	return strings.Join(words, ", ")
}

// ValueHelp describes the accepted values.
func (s *Setting) ValueHelp() string {
	switch s.Kind {
	case KindNamed:
		return joinWords(s.Values)
	case KindPercent:
		return "integer 0..100"
	case KindModes:
		return "at least two distinct modes, comma-separated: " + joinWords(modeValues)
	}
	return "left,right actions, comma-separated: noise-control or siri for each ear"
}

func wordValue(values []NamedValue, word string) (uint32, bool) {
	for _, v := range values {
		if v.Word == word {
			return v.Value, true
		}
	}
	return 0, false
}

// parsePercent accepts ASCII decimal digits only; leading zeros are fine and any
// number of digits cannot overflow into acceptance.
func parsePercent(value string) (uint32, bool) {
	if value == "" {
		return 0, false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	trimmed := strings.TrimLeft(value, "0")
	if len(trimmed) > 3 {
		return 0, false
	}
	if trimmed == "" {
		return 0, true
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n > 100 {
		return 0, false
	}
	return uint32(n), true
}

// Encode turns a command-line word into the device value. The error text is ValueHelp.
func (s *Setting) Encode(value string) (uint32, error) {
	invalid := errors.New(s.ValueHelp())
	switch s.Kind {
	case KindNamed:
		if raw, ok := wordValue(s.Values, value); ok {
			return raw, nil
		}
		return 0, invalid
	case KindPercent:
		n, ok := parsePercent(value)
		if !ok {
			return 0, invalid
		}
		if s.Control == toneVolumeControl {
			n |= 0x5000
		}
		return n, nil
	case KindModes:
		parts := strings.Split(value, ",")
		if len(parts) < 2 {
			return 0, invalid
		}
		var raw uint32
		for _, part := range parts {
			mode, ok := wordValue(modeValues, part)
			if !ok || raw&(1<<(mode-1)) != 0 {
				return 0, invalid
			}
			raw |= 1 << (mode - 1)
		}
		return raw, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return 0, invalid
	}
	left, okLeft := wordValue(holdValues, parts[0])
	right, okRight := wordValue(holdValues, parts[1])
	if !okLeft || !okRight {
		return 0, invalid
	}
	return left<<holdActionLeftShift | right<<holdActionRightShift, nil
}

// Decode renders a device value, or "unknown device value (0x..)".
func (s *Setting) Decode(raw uint32) string {
	switch s.Kind {
	case KindNamed:
		for _, v := range s.Values {
			if v.Value == raw {
				return v.Word
			}
		}
	case KindPercent:
		value, marker := raw, uint32(0)
		if s.Control == toneVolumeControl {
			value, marker = raw&0xff, 0x5000
		}
		if raw == value|marker && value <= 100 {
			return strconv.FormatUint(uint64(value), 10)
		}
	case KindModes:
		if raw <= 15 && bits.OnesCount32(raw) >= 2 {
			var words []string
			for _, m := range modeValues {
				if raw&(1<<(m.Value-1)) != 0 {
					words = append(words, m.Word)
				}
			}
			return strings.Join(words, ",")
		}
	case KindActions:
		if raw <= 0xffff {
			word := func(v uint32) (string, bool) {
				for _, h := range holdValues {
					if h.Value == v {
						return h.Word, true
					}
				}
				return "", false
			}
			left, okLeft := word(raw >> holdActionLeftShift & 255)
			right, okRight := word(raw >> holdActionRightShift & 255)
			if okLeft && okRight {
				return left + "," + right
			}
		}
	}
	return fmt.Sprintf("unknown device value (0x%x)", raw)
}

// Record is one setting as the dongle last received it.
type Record struct {
	AgeMs    uint32
	Revision uint32
	Value    uint32
}

// DecodeSnapshot parses the kind 2 settings snapshot.
func DecodeSnapshot(data []byte) (map[byte]Record, error) {
	if len(data) < 2 || data[0] != 1 || len(data) != 2+13*int(data[1]) {
		return nil, errors.New("Malformed settings snapshot")
	}
	result := make(map[byte]Record, int(data[1]))
	le := binary.LittleEndian
	for offset := 2; offset < len(data); offset += 13 {
		control := data[offset]
		record := Record{le.Uint32(data[offset+1:]), le.Uint32(data[offset+5:]), le.Uint32(data[offset+9:])}
		if _, duplicate := result[control]; duplicate || record.Revision == 0 {
			return nil, errors.New("Malformed settings record")
		}
		result[control] = record
	}
	return result, nil
}

const (
	settingConfirmWait = 5 * time.Second
	settingConfirmPoll = 100 * time.Millisecond
)

// SettingsClient reads and writes settings within one connection generation.
type SettingsClient struct {
	client     *Client
	status     Status
	generation uint32
}

// NewSettingsClient reads the status that pins the client to the current generation.
func NewSettingsClient(dev hid.Device) (*SettingsClient, error) {
	return NewClient(dev).SettingsClient()
}

// SettingsClient reads the status that pins a settings client to the current generation.
func (c *Client) SettingsClient() (*SettingsClient, error) {
	status, err := c.Status()
	if err != nil {
		return nil, err
	}
	return &SettingsClient{client: c, status: status, generation: status.Generation}, nil
}

// Status returns the status read when the client was created.
func (s *SettingsClient) Status() Status { return s.status }

// Read returns the current settings snapshot keyed by control ID.
func (s *SettingsClient) Read() (map[byte]Record, error) {
	if s.status.Capabilities&CapSettings == 0 {
		return nil, errors.New("Firmware does not advertise typed settings support")
	}
	generation := s.generation
	reply, err := s.client.TypedRequest([]byte{2}, &generation)
	if err != nil {
		return nil, err
	}
	return DecodeSnapshot(reply)
}

// Set writes value to a setting of either table once and waits for a fresh matching
// report. Errors are Unsupported or RequestRejected as raised, Unconfirmed once the
// write may have applied, and plain errors from before the write.
func (s *SettingsClient) Set(name string, value uint32) (string, error) {
	setting, ok := LookupAnySetting(name)
	if !ok {
		return "", fmt.Errorf("unknown setting: %s", name)
	}
	if !s.status.ControlsReady {
		return "", errors.New("AirPods settings connection is not ready")
	}
	if s.status.MicrophoneActive {
		return "", errors.New("Stop microphone use before changing AirPods settings")
	}
	values, err := s.Read()
	if err != nil {
		return "", err
	}
	before, reported := values[setting.Control]
	if reported && before.Value == value {
		return fmt.Sprintf("%s: %s (already reported, %d ms old; no write sent)", name, setting.Decode(value), before.AgeMs), nil
	}
	if err := s.write(name, setting.Control, value); err != nil {
		return "", err
	}
	mayHaveApplied := func(err error) error {
		return &Unconfirmed{
			Message: fmt.Sprintf("%s: change not confirmed: %v. It may have applied; read it again before retrying.", name, err),
			Err:     err,
		}
	}
	clock := s.client.clock
	until := clock.Now().Add(settingConfirmWait)
	for clock.Now().Before(until) {
		values, err := s.Read()
		if err != nil {
			return "", mayHaveApplied(err)
		}
		current, present := values[setting.Control]
		if present && (!reported || current.Revision != before.Revision) && current.Value == value {
			return fmt.Sprintf("%s: %s (confirmed by AirPods)", name, setting.Decode(value)), nil
		}
		clock.Sleep(settingConfirmPoll)
	}
	return "", &Unconfirmed{Message: fmt.Sprintf("%s: sent, but no fresh matching device report. It may have applied; use get %s before retrying.", name, name)}
}

// Send writes value once without waiting for a report, for settings the AirPods can
// apply silently. Success means the dongle accepted it, nothing more.
func (s *SettingsClient) Send(name string, value uint32) (string, error) {
	setting, ok := LookupAnySetting(name)
	if !ok {
		return "", fmt.Errorf("unknown setting: %s", name)
	}
	if !s.status.ControlsReady {
		return "", errors.New("AirPods settings connection is not ready")
	}
	if s.status.MicrophoneActive {
		return "", errors.New("Stop microphone use before changing AirPods settings")
	}
	if s.status.Capabilities&CapSettings == 0 {
		return "", errors.New("Firmware does not advertise typed settings support")
	}
	if err := s.write(name, setting.Control, value); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: %s (sent)", name, setting.Decode(value)), nil
}

// write classifies failures of the write itself: rejected before applying, or uncertain.
func (s *SettingsClient) write(name string, control byte, value uint32) error {
	request := []byte{1, control, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(request[2:], value)
	generation := s.generation
	reply, err := s.client.TypedRequest(request, &generation)
	if err == nil && len(reply) != 0 {
		err = errors.New("Unexpected setting-write response")
	}
	if err == nil {
		return nil
	}
	var unsupported *Unsupported
	if errors.As(err, &unsupported) {
		return err
	}
	var rejected *RequestRejected
	if errors.As(err, &rejected) {
		if rejected.Code == 1 || rejected.Code == 2 || rejected.Code == 4 {
			return err
		}
		return &Unconfirmed{
			Message: fmt.Sprintf("%s: change not confirmed: %v. Read it again before retrying.", name, err),
			Err:     err,
		}
	}
	return &Unconfirmed{
		Message: fmt.Sprintf("%s: change not confirmed: %v. It may have applied; read it again before retrying.", name, err),
		Err:     err,
	}
}
