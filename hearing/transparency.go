package hearing

import (
	"bytes"
	"errors"
	"math"
)

// TransparencySettings is the Custom Transparency record's editable part. The record's
// curves and, in the 104 byte form, its own-voice value are carried over untouched by any write.
type TransparencySettings struct {
	Enabled                                               bool
	LeftAmplification, RightAmplification                 float32
	LeftTone, RightTone                                   float32
	LeftConversationBoost, RightConversationBoost         bool
	LeftAmbientNoiseReduction, RightAmbientNoiseReduction float32
}

// Valid reports whether every field is finite and in range: amplification -1 to 2, tone -1
// to 1, ambient noise reduction 0 to 1.
func (s TransparencySettings) Valid() bool {
	return within(s.LeftAmplification, -1, 2) && within(s.RightAmplification, -1, 2) &&
		within(s.LeftTone, -1, 1) && within(s.RightTone, -1, 1) &&
		within(s.LeftAmbientNoiseReduction, 0, 1) && within(s.RightAmbientNoiseReduction, 0, 1)
}

// Custom Transparency records come in two lengths: 100 bytes, and 104 with Own Voice at the end.
const shortTransparencySize = 100

// unsetTransparency is the record of AirPods that never had Custom Transparency set up.
var unsetTransparency = []byte{2, 0, 0, 0}

func parseTransparency(value []byte) (TransparencySettings, error) {
	bad := errors.New("The AirPods returned a Custom Transparency record in a format this app does not know.")
	if len(value) != recordSize && len(value) != shortTransparencySize {
		return TransparencySettings{}, bad
	}
	for offset := 0; offset < len(value); offset += 4 {
		v := float64(floatAt(value, offset))
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return TransparencySettings{}, bad
		}
	}
	for _, offset := range []int{0, offLeftConv, offRightConv} {
		if v := floatAt(value, offset); v != 0 && v != 1 {
			return TransparencySettings{}, bad
		}
	}
	s := TransparencySettings{
		Enabled:           floatAt(value, 0) == 1,
		LeftAmplification: floatAt(value, offLeftAmp), RightAmplification: floatAt(value, offRightAmp),
		LeftTone: floatAt(value, offLeftTone), RightTone: floatAt(value, offRightTone),
		LeftConversationBoost: floatAt(value, offLeftConv) == 1, RightConversationBoost: floatAt(value, offRightConv) == 1,
		LeftAmbientNoiseReduction: floatAt(value, offLeftANR), RightAmbientNoiseReduction: floatAt(value, offRightANR),
	}
	if !s.Valid() || (len(value) == recordSize && !within(floatAt(value, offOwnVoice), 0, 1)) {
		return TransparencySettings{}, errors.New("The AirPods returned Custom Transparency values this app does not know.")
	}
	return s, nil
}

// findTransparency discovers the record; a nil characteristic means the AirPods expose none.
func findTransparency(exchange Exchanger) (*Characteristic, []byte, error) {
	found, err := DiscoverRead(exchange, "Custom Transparency", transparencyUUID)
	if err != nil || found[0] == nil {
		return nil, nil, err
	}
	value, err := readValue(exchange, found[0].Handle, "Could not read Custom Transparency.")
	if err != nil {
		return nil, nil, err
	}
	return found[0], value, nil
}

// readTransparency reads the record; a nil characteristic means the AirPods expose none.
func readTransparency(exchange Exchanger) (*Characteristic, []byte, TransparencySettings, error) {
	found, value, err := findTransparency(exchange)
	if err != nil || found == nil {
		return nil, nil, TransparencySettings{}, err
	}
	settings, err := parseTransparency(value)
	if err != nil {
		return nil, nil, TransparencySettings{}, err
	}
	return found, value, settings, nil
}

// requireCleanTransparency refuses unless Custom Transparency is known to be off. The 4 byte
// record of AirPods that never set it up counts as off. A model that does not expose the
// record is refused as well: its state cannot be established.
func requireCleanTransparency(exchange Exchanger) error {
	found, value, err := findTransparency(readOnly(exchange))
	if err != nil {
		return err
	}
	if found == nil {
		return refuse("Could not confirm Custom Transparency is off, so hearing assistance stays as it is.")
	}
	if bytes.Equal(value, unsetTransparency) {
		return nil
	}
	settings, err := parseTransparency(value)
	if err != nil {
		return err
	}
	if settings.Enabled {
		return refuse("Custom Transparency is on. Turn it off before enabling hearing assistance.")
	}
	return nil
}
