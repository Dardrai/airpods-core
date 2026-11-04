package dongle

import (
	"encoding/binary"
	"errors"
	"math"
)

// Typed request kinds (first payload byte) used by the tray helpers.
const (
	kindSetAudioProfile = 3
	kindSetGameMode     = 4
	kindPerformance     = 5
	kindNearbyStart     = 6
	kindNearbySnapshot  = 7
	kindNearbyStop      = 8
	kindAudioState      = 9
	kindLease           = 10
)

var gameModeStates = [...]string{"off", "pending", "unconfirmed", "confirmed", "failed", "unsupported", "restoring"}

// AudioState is the kind 9 reply. Requested and actual codec and bitrate are separate.
type AudioState struct {
	RequestedCodec        byte
	Codec                 byte
	Variable              bool
	RequestedBitrate      uint32
	Bitrate               uint32
	GameModeRequested     bool
	GameModeState         string
	Input                 byte
	Pending               bool
	Streaming             bool
	MicrophoneActive      bool
	GameModeSupported     bool
	ReportedDelayTenthsMs *uint16 // nil when the dongle reports no delay
	PrimingMs             uint16
}

// DecodeAudioState validates and decodes a kind 9 reply.
func DecodeAudioState(b []byte) (AudioState, error) {
	le := binary.LittleEndian
	if len(b) != 20 || b[0] != 1 || b[1] > 3 || b[2] > 3 || b[3] > 1 || b[12] > 1 || b[13] > 6 ||
		b[14] > 2 || b[15]&^15 != 0 || le.Uint16(b[18:]) != 100 {
		return AudioState{}, errors.New("Unsupported dongle audio profile.")
	}
	state := AudioState{
		RequestedCodec:    b[1],
		Codec:             b[2],
		Variable:          b[3] == 1,
		RequestedBitrate:  le.Uint32(b[4:]),
		Bitrate:           le.Uint32(b[8:]),
		GameModeRequested: b[12] == 1,
		GameModeState:     gameModeStates[b[13]],
		Input:             b[14],
		Pending:           b[15]&1 != 0,
		Streaming:         b[15]&2 != 0,
		MicrophoneActive:  b[15]&4 != 0,
		GameModeSupported: b[15]&8 != 0,
		PrimingMs:         100,
	}
	if delay := le.Uint16(b[16:]); delay != math.MaxUint16 {
		state.ReportedDelayTenthsMs = &delay
	}
	return state, nil
}

// AudioState reads the current audio profile (kind 9).
func (c *Client) AudioState() (AudioState, error) {
	reply, err := c.TypedRequest([]byte{kindAudioState}, nil)
	if err != nil {
		return AudioState{}, err
	}
	return DecodeAudioState(reply)
}

// AudioProfileRequest builds and validates a kind 3 request. Codec is 0 automatic,
// 1 SBC, 2 AAC, 3 AAC-ELD; variable is AAC-ELD only; bitrate 0 means the default.
func AudioProfileRequest(codec byte, variable bool, bitrate uint32) ([]byte, error) {
	valid := codec <= 3
	switch {
	case codec <= 1 && (variable || bitrate != 0):
		valid = false
	case variable && codec != 3:
		valid = false
	case codec == 2:
		switch bitrate {
		case 0, 96000, 128000, 174000, 192000, 220000:
		default:
			valid = false
		}
	case codec == 3:
		switch bitrate {
		case 0, 128000, 174000, 192000, 220000, 256000:
		default:
			valid = false
		}
	}
	if !valid {
		return nil, errors.New("Unsupported dongle audio profile.")
	}
	request := []byte{kindSetAudioProfile, codec, 0, 0, 0, 0, 0}
	if variable {
		request[2] = 1
	}
	binary.LittleEndian.PutUint32(request[3:], bitrate)
	return request, nil
}

// SetAudioProfile requests a codec profile and returns the reported audio state.
// A profile change can restart the link before its reply: on error the request may
// have applied, so read AudioState instead of replaying it.
func (c *Client) SetAudioProfile(codec byte, variable bool, bitrate uint32) (AudioState, error) {
	request, err := AudioProfileRequest(codec, variable, bitrate)
	if err != nil {
		return AudioState{}, err
	}
	reply, err := c.TypedRequest(request, nil)
	if err != nil {
		return AudioState{}, err
	}
	return DecodeAudioState(reply)
}

// SetGameMode toggles game mode (kind 4) and returns the reported audio state.
func (c *Client) SetGameMode(enabled bool) (AudioState, error) {
	request := []byte{kindSetGameMode, 0}
	if enabled {
		request[1] = 1
	}
	reply, err := c.TypedRequest(request, nil)
	if err != nil {
		return AudioState{}, err
	}
	return DecodeAudioState(reply)
}

// PerformanceState is the kind 5 reply; a nil counter is not reported.
type PerformanceState struct {
	Input, Codec, Flags byte
	UptimeMs            *uint32
	PCMUsed             *uint32
	PCMCapacity         *uint32
	PCMHighWater        *uint32
	UnderflowFrames     *uint32
	DroppedFrames       *uint32
	EncodedUnits        *uint32
	EncodeUs            *uint32
	EncodeMaxUs         *uint32
	SendFailures        *uint32
	Recoveries          *uint32
	HeapCapacity        *uint32
	HeapAllocated       *uint32
	HeapRemaining       *uint32
	HeapArena           *uint32
}

// DecodePerformance validates and decodes a kind 5 reply.
func DecodePerformance(b []byte) (PerformanceState, error) {
	if len(b) != 64 || b[0] != 1 || b[1] > 2 || b[2] > 3 || b[3]&^15 != 0 {
		return PerformanceState{}, errors.New("Unsupported dongle performance snapshot.")
	}
	field := func(offset int) *uint32 {
		value := binary.LittleEndian.Uint32(b[offset:])
		if value == math.MaxUint32 {
			return nil
		}
		return &value
	}
	s := PerformanceState{
		Input: b[1], Codec: b[2], Flags: b[3],
		UptimeMs: field(4), PCMUsed: field(8), PCMCapacity: field(12), PCMHighWater: field(16),
		UnderflowFrames: field(20), DroppedFrames: field(24), EncodedUnits: field(28), EncodeUs: field(32),
		EncodeMaxUs: field(36), SendFailures: field(40), Recoveries: field(44), HeapCapacity: field(48),
		HeapAllocated: field(52), HeapRemaining: field(56), HeapArena: field(60),
	}
	over := func(value, limit *uint32) bool { return value != nil && limit != nil && *value > *limit }
	if over(s.PCMUsed, s.PCMCapacity) || over(s.PCMHighWater, s.PCMCapacity) ||
		over(s.HeapAllocated, s.HeapCapacity) || over(s.HeapRemaining, s.HeapCapacity) {
		return PerformanceState{}, errors.New("Inconsistent dongle performance counters.")
	}
	return s, nil
}

// Performance reads the performance counters (kind 5).
func (c *Client) Performance() (PerformanceState, error) {
	reply, err := c.TypedRequest([]byte{kindPerformance}, nil)
	if err != nil {
		return PerformanceState{}, err
	}
	return DecodePerformance(reply)
}

// NearbyState is a nearby-finder snapshot (kinds 6 and 7).
type NearbyState struct {
	Active            bool
	Paused            bool
	AvailableIdentity bool
	RSSI              *int16 // nil when no verified reading exists
	Trend             string // unknown, stronger, steady or weaker
	LastHeardAgeMs    *uint32
}

// DecodeNearby validates and decodes a nearby-finder snapshot.
func DecodeNearby(b []byte) (NearbyState, error) {
	if len(b) != 24 || b[0] != 1 || b[1]&0x7f > 4 || b[3] > 3 {
		return NearbyState{}, errors.New("Unsupported dongle nearby-finder snapshot.")
	}
	state := b[1] & 0x7f
	identity := b[1]&0x80 != 0
	latest := int8(b[2])
	q8 := int32(binary.LittleEndian.Uint32(b[20:]))
	var rssi *int16
	if q8 != math.MinInt32 {
		if q8 < -126*256 || q8 > 20*256 {
			return NearbyState{}, errors.New("Invalid dongle signal strength.")
		}
		value := int16(math.Round(float64(q8) / 256))
		rssi = &value
	}
	if (latest != 127 && (latest < -126 || latest > 20)) || (state != 0 && !identity) || (!identity && rssi != nil) {
		return NearbyState{}, errors.New("Unverified dongle signal reading.")
	}
	result := NearbyState{
		Active:            state != 0,
		Paused:            state == 4,
		AvailableIdentity: identity,
		RSSI:              rssi,
		Trend:             [...]string{"unknown", "stronger", "steady", "weaker"}[b[3]],
	}
	if age := binary.LittleEndian.Uint32(b[4:]); age != math.MaxUint32 {
		result.LastHeardAgeMs = &age
	}
	return result, nil
}

func (c *Client) nearby(kind byte) (NearbyState, error) {
	reply, err := c.TypedRequest([]byte{kind}, nil)
	if err != nil {
		return NearbyState{}, err
	}
	return DecodeNearby(reply)
}

// NearbyStart starts the nearby finder (kind 6).
func (c *Client) NearbyStart() (NearbyState, error) { return c.nearby(kindNearbyStart) }

// NearbySnapshot reads the nearby finder's state (kind 7).
func (c *Client) NearbySnapshot() (NearbyState, error) { return c.nearby(kindNearbySnapshot) }

// NearbyStop stops the nearby finder (kind 8).
func (c *Client) NearbyStop() error {
	reply, err := c.TypedRequest([]byte{kindNearbyStop}, nil)
	if err != nil {
		return err
	}
	if len(reply) != 0 {
		return errors.New("Unsupported finder response.")
	}
	return nil
}

func leaseRequest(action byte, token uint32) []byte {
	request := []byte{kindLease, action, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(request[2:], token)
	return request
}

// DecodeLeaseToken validates the reply to a lease acquisition.
func DecodeLeaseToken(reply []byte) (uint32, error) {
	if len(reply) != 5 || reply[0] != 1 {
		return 0, errors.New("Unsupported dongle settings lease.")
	}
	token := binary.LittleEndian.Uint32(reply[1:])
	if token == 0 {
		return 0, errors.New("Dongle settings lease is unavailable.")
	}
	return token, nil
}

// AcquireLease takes the settings lease (kind 10, action 0).
func (c *Client) AcquireLease() (uint32, error) {
	reply, err := c.TypedRequest(leaseRequest(0, 0), nil)
	if err != nil {
		return 0, err
	}
	return DecodeLeaseToken(reply)
}

// RenewLease extends the lease (kind 10, action 1). A rejected renewal has lost the
// lease; do not acquire a replacement halfway through an edit.
func (c *Client) RenewLease(token uint32) error { return c.leaseAction(1, token) }

// ReleaseLease gives the lease back (kind 10, action 2).
func (c *Client) ReleaseLease(token uint32) error { return c.leaseAction(2, token) }

func (c *Client) leaseAction(action byte, token uint32) error {
	reply, err := c.TypedRequest(leaseRequest(action, token), nil)
	if err != nil {
		return err
	}
	if len(reply) != 0 {
		return errors.New("Unsupported dongle settings lease renewal.")
	}
	return nil
}

// Clock returns the client's time source, for waits that belong to one operation.
func (c *Client) Clock() Clock { return c.clock }
