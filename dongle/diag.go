package dongle

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// Field is one key of an Object.
type Field struct {
	Key   string
	Value any
}

// Object is a JSON object that keeps its key order. Values are nil, bool, string,
// integers, float64, Object or []Object.
type Object []Field

// Get returns a key's value.
func (o Object) Get(key string) (any, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// String renders the object as one line of JSON with ", " and ": " separators.
func (o Object) String() string {
	var b strings.Builder
	writeJSON(&b, o)
	return b.String()
}

// MarshalJSON implements json.Marshaler.
func (o Object) MarshalJSON() ([]byte, error) { return []byte(o.String()), nil }

func writeJSON(b *strings.Builder, value any) {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case string:
		b.WriteString(strconv.Quote(v))
	case int:
		b.WriteString(strconv.Itoa(v))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(v), 10))
	case float64:
		text := strconv.FormatFloat(v, 'f', -1, 64)
		if !strings.Contains(text, ".") {
			text += ".0"
		}
		b.WriteString(text)
	case Object:
		b.WriteByte('{')
		for i, f := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Quote(f.Key))
			b.WriteString(": ")
			writeJSON(b, f.Value)
		}
		b.WriteByte('}')
	case []Object:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			writeJSON(b, item)
		}
		b.WriteByte(']')
	default:
		panic("dongle: unsupported JSON value")
	}
}

// zipU32 pairs names with consecutive little-endian u32 values starting at offset.
func zipU32(names []string, data []byte, offset int) Object {
	result := make(Object, len(names))
	for i, name := range names {
		result[i] = Field{name, binary.LittleEndian.Uint32(data[offset+4*i:])}
	}
	return result
}

var (
	traceHead  = []string{"time_us_wrapped", "event_serial", "pcm_used", "sample_debt", "game_phase", "rtp_timestamp", "reserved"}
	traceStats = []string{"usb_packets", "usb_frames", "usb_gap_max_us", "usb_bad_packets",
		"usb_short_reads", "startup_dropped_frames", "active_dropped_frames",
		"startup_zero_frames", "active_zero_frames", "timer_late_max_us",
		"encode_max_us", "send_wait_max_us", "send_gap_max_us", "packet_age_max_us",
		"controller_flushes", "controller_completed_packets", "discarded_debt_frames",
		"timeline_recoveries", "pcm_min", "pcm_max", "clock_rate_q16",
		"ble_scan_requested", "usb_alternate_setting", "encode_unit_max_us"}
	traceEvents = map[uint32]string{1: "drop", 2: "zero_fill", 3: "timer_late", 4: "send_wait",
		5: "timeline_recovery", 6: "controller_flush", 7: "usb_error", 8: "format", 9: "game"}
	inspectNames = []string{"usb_samples", "usb_nonzero_samples", "usb_peak_q15", "usb_last_nonzero_us",
		"pcm_samples", "pcm_nonzero_samples", "pcm_peak_q15", "pcm_last_nonzero_us",
		"submitted_au_count", "submitted_au_us", "submitted_au_bytes", "encoder_generation",
		"submitted_au_sequence", "usb_last_packet_us"}
	phoneNames = []string{"packets", "malformed", "sequence_gaps", "submit_failures",
		"decode_failures", "buffer_resets", "decoded_frames", "last_packet_ms",
		"queued_packets", "output_ready", "last_decode_us", "max_decode_us"}
	phoneNamesV2 = []string{"max_media_gap_ms", "max_pcm_gap_ms", "timestamp_discontinuities"}
	radioNames   = []string{"rssi", "quality", "tx_power", "max_tx_power", "afh_map"}
	adaptiveHead = []string{"current_bps", "ceiling_bps", "target_bps", "changes", "errors",
		"last_change_ms", "window_flush_events", "window_pressure_events", "window_elapsed_ms",
		"floor_bps", "clean_ms", "reserved", "total_pressure_events", "flush_events", "radio_epoch"}
	radioReasons = []string{"off", "startup", "clean", "pressure", "recover", "frozen", "fault"}
	perfNames    = []string{"uptime_ms", "pcm_used", "pcm_capacity", "pcm_peak", "underflows", "drops",
		"encoded_units", "encode_us", "encode_max_us", "send_failures", "recoveries",
		"heap_capacity", "heap_allocated", "heap_remaining", "heap_arena"}
)

// DecodeTrace decodes the 512-byte audio trace (kind 11).
func DecodeTrace(data []byte) (Object, error) {
	if len(data) != 512 || data[0] != 1 || data[1] != 0x10 || data[2] > 24 {
		return nil, errors.New("Unsupported audio trace layout")
	}
	le := binary.LittleEndian
	result := zipU32(traceHead, data, 4)
	result = append(result, Field{"stats", zipU32(traceStats, data, 32)})
	events := make([]Object, 0, data[2])
	for i := 0; i < int(data[2]); i++ {
		at := 128 + 16*i
		code := le.Uint32(data[at+4:])
		var kind any = code
		if name, ok := traceEvents[code]; ok {
			kind = name
		}
		events = append(events, Object{
			{"time_us_wrapped", le.Uint32(data[at:])},
			{"kind", kind},
			{"a", le.Uint32(data[at+8:])},
			{"b", le.Uint32(data[at+12:])},
		})
	}
	return append(result, Field{"events", events}), nil
}

// DecodeInspect decodes the 400-byte audio inspection (kind 13).
func DecodeInspect(data []byte) (Object, error) {
	if len(data) != 400 || data[0] != 1 || data[1] != 3 || data[2] > 3 || data[3] < 1 || data[3] > 8 {
		return nil, errors.New("Unsupported audio inspection layout")
	}
	le := binary.LittleEndian
	result := zipU32(inspectNames, data, 8)
	auBytes := le.Uint32(data[8+4*10:])
	truncated := data[2]&1 != 0
	if auBytes > 2047 || truncated != (auBytes > 320) {
		return nil, errors.New("Invalid inspected AAC-ELD length")
	}
	shown := min(auBytes, 320)
	return append(result,
		Field{"snapshot_us", le.Uint32(data[4:])},
		Field{"rtp_timestamp", le.Uint32(data[64:])},
		Field{"bad_eld_packets", le.Uint32(data[76:])},
		Field{"truncated", truncated},
		Field{"active", data[2]&2 != 0},
		Field{"asc_hex", hex.EncodeToString(data[68 : 68+int(data[3])])},
		Field{"au_hex", hex.EncodeToString(data[80 : 80+shown])},
	), nil
}

// DecodePhone decodes the phone diagnostics (kind 15), layouts 1 (52 B) and 2 (64 B).
func DecodePhone(data []byte) (Object, error) {
	layoutOK := len(data) >= 4 && data[1] <= 3 &&
		((data[0] == 1 && data[3] == 12 && len(data) == 52) || (data[0] == 2 && data[3] == 15 && len(data) == 64))
	if !layoutOK {
		return nil, errors.New("Unsupported phone diagnostic layout")
	}
	names := phoneNames
	if data[0] == 2 {
		names = append(append([]string{}, phoneNames...), phoneNamesV2...)
	}
	result := zipU32(names, data, 4)
	if binary.LittleEndian.Uint32(data[4+4*9:]) > 1 {
		return nil, errors.New("Invalid phone output state")
	}
	return append(result, Field{"codec", int(data[1])}, Field{"flags", int(data[2])}), nil
}

// DecodeRadio decodes the 128-byte radio and adaptive bitrate snapshot (kind 19).
func DecodeRadio(data []byte) (Object, error) {
	if len(data) != 128 || data[0] != 1 || data[1] != 128 || data[64] != 1 ||
		data[2]&^7 != 0 || data[3]&^31 != 0 || data[14]&^31 != 0 || data[15]&^31 != 0 ||
		data[65]&^15 != 0 || data[66] > 6 {
		return nil, errors.New("Unsupported radio/adaptation layout")
	}
	le := binary.LittleEndian
	valid := data[3]
	measure := func(index int, value any) any {
		if valid&(1<<index) == 0 {
			return nil
		}
		return value
	}
	ages := make(Object, len(radioNames))
	for i, name := range radioNames {
		var age any = le.Uint32(data[32+4*i:])
		if age == uint32(0xffffffff) {
			age = nil
		}
		ages[i] = Field{name, age}
	}
	adaptive := make(Object, 0, len(adaptiveHead)+4)
	for _, field := range zipU32(adaptiveHead, data, 68) {
		if field.Key != "reserved" {
			adaptive = append(adaptive, field)
		}
	}
	adaptive = append(adaptive,
		Field{"active", data[65]&1 != 0},
		Field{"frozen", data[65]&2 != 0},
		Field{"fault", data[65]&4 != 0},
		Field{"variable", data[65]&8 != 0},
		Field{"reason", radioReasons[data[66]]},
	)
	return Object{
		{"snapshot_ms", le.Uint32(data[4:])},
		{"epoch", le.Uint32(data[8:])},
		{"handle", int(le.Uint16(data[12:]))},
		{"connected", data[2]&1 != 0},
		{"sampling", data[2]&2 != 0},
		{"pending", data[2]&4 != 0},
		{"valid_mask", int(valid)},
		{"failed_mask", int(data[14])},
		{"unavailable_mask", int(data[15])},
		{"rssi_relative_db", measure(0, int(int8(data[16])))},
		{"quality", measure(1, int(data[17]))},
		{"tx_power_dbm", measure(2, int(int8(data[18])))},
		{"max_tx_power_dbm", measure(3, int(int8(data[19])))},
		{"afh_mode", measure(4, int(data[20]))},
		{"afh_map", measure(4, hex.EncodeToString(data[21:31]))},
		{"age_ms", ages},
		{"flush_events", le.Uint32(data[52:])},
		{"completed_packets", le.Uint32(data[56:])},
		{"adaptive", adaptive},
	}, nil
}

// Transaction runs one diagnostic request, restricted to reads and profile controls.
func (c *Client) Transaction(payload []byte) ([]byte, error) {
	isRead := false
	if len(payload) == 1 {
		switch payload[0] {
		case 5, 9, 11, 13, 15, 19:
			isRead = true
		}
	}
	isProfile := false
	if len(payload) == 2 && payload[1] <= 1 {
		switch payload[0] {
		case 4, 12, 14, 18:
			isProfile = true
		}
	}
	if !isRead && !isProfile {
		return nil, errors.New("Only diagnostic reads and explicit audio profiling controls are allowed")
	}
	response, err := c.TypedRequest(payload, nil)
	if err != nil {
		return nil, err
	}
	if isRead && len(response) == 0 {
		return nil, errors.New("Invalid diagnostic length")
	}
	return response, nil
}

// Feature runs one diagnostic read.
func (c *Client) Feature(command byte) ([]byte, error) {
	switch command {
	case 5, 9, 11, 13, 15, 19:
		return c.Transaction([]byte{command})
	}
	return nil, errors.New("Collector permits audio/performance/trace reads only")
}

// SampleOptions selects what one diagnostics sample contains.
type SampleOptions struct {
	Trace, Inspect, Phone, Radio bool
}

func (c *Client) unixSeconds() float64 {
	return float64(c.clock.Now().UnixNano()) / 1e9
}

// Sample reads one diagnostics record.
func (c *Client) Sample(opts SampleOptions) (Object, error) {
	if opts.Radio {
		now := c.unixSeconds()
		data, err := c.Feature(19)
		if err != nil {
			return nil, err
		}
		radio, err := DecodeRadio(data)
		if err != nil {
			return nil, err
		}
		return Object{{"time", now}, {"radio", radio}}, nil
	}
	audio, err := c.Feature(9)
	if err != nil {
		return nil, err
	}
	perf, err := c.Feature(5)
	if err != nil {
		return nil, err
	}
	if len(audio) != 20 || len(perf) != 64 || audio[0] != 1 || perf[0] != 1 {
		return nil, errors.New("Unsupported audio/performance layout")
	}
	result := Object{
		{"time", c.unixSeconds()},
		{"game_requested", audio[12] != 0},
		{"game_phase", int(audio[13])},
		{"audio_flags", int(audio[15])},
		{"delay_tenths_ms", int(binary.LittleEndian.Uint16(audio[16:]))},
		{"performance", zipU32(perfNames, perf, 4)},
	}
	var unsupported *Unsupported
	if opts.Trace {
		data, err := c.Feature(11)
		switch {
		case errors.As(err, &unsupported):
			result = append(result, Field{"trace_supported", false})
		case err != nil:
			return nil, err
		default:
			trace, err := DecodeTrace(data)
			if err != nil {
				return nil, err
			}
			result = append(result, Field{"trace", trace})
		}
	}
	if opts.Inspect {
		data, err := c.Feature(13)
		if err != nil {
			return nil, err
		}
		inspect, err := DecodeInspect(data)
		if err != nil {
			return nil, err
		}
		result = append(result, Field{"inspect", inspect})
	}
	if opts.Phone {
		data, err := c.Feature(15)
		switch {
		case errors.As(err, &unsupported):
			result = append(result, Field{"phone_supported", false})
		case err != nil:
			return nil, err
		default:
			phone, err := DecodePhone(data)
			if err != nil {
				return nil, err
			}
			result = append(result, Field{"phone", phone})
		}
	}
	return result, nil
}
