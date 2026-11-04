package hearing

import (
	"encoding/binary"
	"errors"
	"math"
)

var (
	hearingUUID      = ParseUUID("a412000595c54d6f90980f0b41457e0a")
	transparencyUUID = ParseUUID("d5621cc1f7ab45db94039eaf744d5390")
)

const (
	recordSize = 104

	offLeftEQ    = 4
	offLeftAmp   = 36
	offLeftTone  = 40
	offLeftConv  = 44
	offLeftANR   = 48
	offRightEQ   = 52
	offRightAmp  = 84
	offRightTone = 88
	offRightConv = 92
	offRightANR  = 96
	offOwnVoice  = 100
)

// Bands is the number of audiogram points per ear: 250 Hz to 8 kHz.
const Bands = 8

// BandHz names each audiogram point.
var BandHz = [Bands]int{250, 500, 1000, 2000, 3000, 4000, 6000, 8000}

// Settings is the hearing profile stored on the AirPods. Audiograms are in dB HL. The
// struct is comparable; equal fields mean equal settings, whichever sign a zero has.
type Settings struct {
	LeftEQ, RightEQ                                       [Bands]float32
	LeftAmplification, RightAmplification                 float32
	LeftTone, RightTone                                   float32
	LeftConversationBoost, RightConversationBoost         bool
	LeftAmbientNoiseReduction, RightAmbientNoiseReduction float32
	OwnVoice                                              float32
}

func within(v, low, high float32) bool { return v >= low && v <= high } // false for NaN

// Valid reports whether every field is finite and in range: audiograms 0 to 90,
// amplification -1.5 to 1.5, tone -1 to 1, the rest 0 to 1.
func (s Settings) Valid() bool {
	for i := range Bands {
		if !within(s.LeftEQ[i], 0, 90) || !within(s.RightEQ[i], 0, 90) {
			return false
		}
	}
	return within(s.LeftAmplification, -1.5, 1.5) && within(s.RightAmplification, -1.5, 1.5) &&
		within(s.LeftTone, -1, 1) && within(s.RightTone, -1, 1) &&
		within(s.LeftAmbientNoiseReduction, 0, 1) && within(s.RightAmbientNoiseReduction, 0, 1) &&
		within(s.OwnVoice, 0, 1)
}

func floatAt(value []byte, offset int) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(value[offset:]))
}

func putFloat(value []byte, offset int, v float32) {
	binary.LittleEndian.PutUint32(value[offset:], math.Float32bits(v))
}

func parseSettings(value []byte) (Settings, error) {
	if len(value) != recordSize || value[0] != 2 || (value[1] != 0 && value[1] != 2) ||
		(value[2] != 0x60 && value[2] != 0x64) || value[3] != 0 {
		return Settings{}, errors.New("The AirPods returned a hearing profile in a format this app does not know.")
	}
	left, right := floatAt(value, offLeftConv), floatAt(value, offRightConv)
	if (left != 0 && left != 1) || (right != 0 && right != 1) {
		return Settings{}, errors.New("The AirPods returned hearing profile values this app does not know.")
	}
	s := Settings{
		LeftAmplification: floatAt(value, offLeftAmp), RightAmplification: floatAt(value, offRightAmp),
		LeftTone: floatAt(value, offLeftTone), RightTone: floatAt(value, offRightTone),
		LeftConversationBoost: left == 1, RightConversationBoost: right == 1,
		LeftAmbientNoiseReduction: floatAt(value, offLeftANR), RightAmbientNoiseReduction: floatAt(value, offRightANR),
		OwnVoice: floatAt(value, offOwnVoice),
	}
	for i := range Bands {
		s.LeftEQ[i] = floatAt(value, offLeftEQ+4*i)
		s.RightEQ[i] = floatAt(value, offRightEQ+4*i)
	}
	if !s.Valid() {
		return Settings{}, errors.New("The AirPods returned hearing profile values this app does not know.")
	}
	return s, nil
}

// readOnly stops a read path from ever sending anything but a search or a read.
func readOnly(exchange Exchanger) Exchanger {
	return func(pdu []byte) ([]byte, error) {
		if len(pdu) == 0 || (pdu[0] != attFindType && pdu[0] != attByType && pdu[0] != attRead) {
			return nil, errors.New("A hearing profile read tried to send something other than a read.")
		}
		return exchange(pdu)
	}
}

func readRecord(exchange Exchanger, handle uint16) ([]byte, Settings, error) {
	value, err := readValue(exchange, handle, "Could not read the hearing profile.")
	if err != nil {
		return nil, Settings{}, err
	}
	settings, err := parseSettings(value)
	if err != nil {
		return nil, Settings{}, err
	}
	return value, settings, nil
}

// readSettings finds the profile and reads it, keeping the raw bytes as a baseline.
func readSettings(exchange Exchanger) (Characteristic, []byte, Settings, error) {
	exchange = readOnly(exchange)
	found, err := DiscoverRead(exchange, "Hearing assistance", hearingUUID)
	if err != nil {
		return Characteristic{}, nil, Settings{}, err
	}
	if found[0] == nil {
		return Characteristic{}, nil, Settings{}, refuse("Hearing assistance is not available on these AirPods.")
	}
	value, settings, err := readRecord(exchange, found[0].Handle)
	if err != nil {
		return Characteristic{}, nil, Settings{}, err
	}
	return *found[0], value, settings, nil
}

// editableValue reports whether this record is one the app knows how to write back.
func editableValue(value []byte, c Characteristic) bool {
	return c.WriteOpcode != 0 && len(value) == recordSize && value[0] == 2 && value[1] == 2 &&
		(value[2] == 0x60 || value[2] == 0x64) && value[3] == 0
}
