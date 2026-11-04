package hearing

import (
	"errors"
	"math"
)

// TransparencySettings is the Custom Transparency record's editable part. The record's
// curves and own-voice fields are carried over untouched by any write.
type TransparencySettings struct {
	Enabled                                               bool
	LeftAmplification, RightAmplification                 float32
	LeftTone, RightTone                                   float32
	LeftConversationBoost, RightConversationBoost         bool
	LeftAmbientNoiseReduction, RightAmbientNoiseReduction float32
}

// Valid reports whether every field is finite and in range: amplification and tone -1
// to 1, ambient noise reduction 0 to 1.
func (s TransparencySettings) Valid() bool {
	return within(s.LeftAmplification, -1, 1) && within(s.RightAmplification, -1, 1) &&
		within(s.LeftTone, -1, 1) && within(s.RightTone, -1, 1) &&
		within(s.LeftAmbientNoiseReduction, 0, 1) && within(s.RightAmbientNoiseReduction, 0, 1)
}

func parseTransparency(value []byte) (TransparencySettings, error) {
	bad := errors.New("The AirPods returned a Custom Transparency record in a format this app does not know.")
	if len(value) != recordSize {
		return TransparencySettings{}, bad
	}
	for offset := 0; offset < recordSize; offset += 4 {
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
	if !s.Valid() {
		return TransparencySettings{}, errors.New("The AirPods returned Custom Transparency values this app does not know.")
	}
	return s, nil
}

// readTransparency reads the record; a nil characteristic means the AirPods expose none.
func readTransparency(exchange Exchanger) (*Characteristic, []byte, TransparencySettings, error) {
	found, err := DiscoverRead(exchange, "Custom Transparency", transparencyUUID)
	if err != nil || found[0] == nil {
		return nil, nil, TransparencySettings{}, err
	}
	value, err := readValue(exchange, found[0].Handle, "Could not read Custom Transparency.")
	if err != nil {
		return nil, nil, TransparencySettings{}, err
	}
	settings, err := parseTransparency(value)
	if err != nil {
		return nil, nil, TransparencySettings{}, err
	}
	return found[0], value, settings, nil
}

// requireCleanTransparency refuses unless Custom Transparency is known to be off. A model
// that does not expose the record is refused as well: its state cannot be established.
func requireCleanTransparency(exchange Exchanger) error {
	found, _, settings, err := readTransparency(readOnly(exchange))
	if err != nil {
		return err
	}
	if found == nil {
		return refuse("Could not confirm Custom Transparency is off, so hearing assistance stays as it is.")
	}
	if settings.Enabled {
		return refuse("Custom Transparency is on. Turn it off before enabling hearing assistance.")
	}
	return nil
}
