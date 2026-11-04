package hearing

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/Dardrai/airpods-core/model"
)

var (
	diagnosticsUUID = ParseUUID("a412000295c54d6f90980f0b41457e0a")
	testModeUUID    = ParseUUID("a412000195c54d6f90980f0b41457e0a")
	sensitivityUUID = ParseUUID("82f6bc0a1bca4873afc9ec5890e3469a")
	curveUUID       = ParseUUID("d5f96afa2f2c41bba7e6f54abe6235b4")
)

// EarResult is one AirPod's saved self-check. It reports what the AirPods stored; it is
// not a current diagnosis, and the clock it uses is not established.
type EarResult struct {
	MeasuredAtSeconds uint64
	FirstUseSeconds   uint32
	General           byte
	Measurement       byte
	Failures          []string
	HasFailureData    bool
}

// Diagnostics is the saved record for both AirPods.
type Diagnostics struct {
	ReadAt time.Time
	Left   EarResult
	Right  EarResult
}

// SameMeasurements compares the stored measurement times of two records.
func (d Diagnostics) SameMeasurements(other Diagnostics) bool {
	return d.Left.MeasuredAtSeconds == other.Left.MeasuredAtSeconds && d.Right.MeasuredAtSeconds == other.Right.MeasuredAtSeconds
}

var failureNames = []struct {
	bit  uint32
	name string
}{
	{1 << 7, "total harmonic distortion"}, {1 << 8, "frequency accuracy"}, {1 << 9, "speaker"},
	{1 << 10, "rear vent"}, {1 << 11, "front vent"}, {1 << 12, "bottom microphone"},
	{1 << 13, "top microphone"}, {1 << 14, "inner microphone"},
}

func decodeEar(value []byte, timeOffset, resultOffset, firstUseOffset int) EarResult {
	le := binary.LittleEndian
	raw := le.Uint32(value[resultOffset:])
	ear := EarResult{
		MeasuredAtSeconds: le.Uint64(value[timeOffset:]),
		FirstUseSeconds:   le.Uint32(value[firstUseOffset:]),
		General:           byte(raw & 7),
		Measurement:       byte(raw >> 3 & 7),
	}
	if ear.MeasuredAtSeconds != 0 && ear.Measurement <= 1 {
		ear.HasFailureData = true
		for _, flag := range failureNames {
			if raw&flag.bit != 0 {
				ear.Failures = append(ear.Failures, flag.name)
			}
		}
	}
	return ear
}

// ParseDiagnostics reads the version 8, 71 byte record.
func ParseDiagnostics(value []byte) (EarResult, EarResult, error) {
	if len(value) != 71 || value[0] != 8 {
		return EarResult{}, EarResult{}, errors.New("Unsupported hardware diagnostic record; expected version 8 and 71 bytes")
	}
	return decodeEar(value, 1, 17, 33), decodeEar(value, 9, 21, 37), nil
}

// Text explains a saved result.
func (e EarResult) Text() string {
	if e.MeasuredAtSeconds == 0 || e.General == 2 || e.General == 4 {
		return "No saved result"
	}
	switch e.Measurement {
	case 2:
		return "Stopped: the lid was opened"
	case 3:
		return "Stopped: the case moved"
	case 4:
		return "Stopped: temperature out of range"
	case 5:
		return "Stopped: too noisy"
	case 6:
		return "Stopped: interrupted"
	}
	text := "No saved result"
	switch e.General {
	case 0:
		text = "Passed"
	case 1:
		text = "Saved result reports a failure"
	case 3:
		text = "Could not reach the test unit"
	}
	if len(e.Failures) > 0 {
		text += " (" + strings.Join(e.Failures, ", ") + ")"
	}
	return text
}

// When formats a stored time; these are the AirPods' own seconds, shown as they are.
func When(seconds uint64) string {
	if seconds == 0 || seconds > 1<<40 {
		return ""
	}
	return time.Unix(int64(seconds), 0).Format("2006-01-02 15:04")
}

// Supported reports whether the hardware cards apply to a model.
func Supported(number string) bool {
	profile, ok := model.Find(number)
	return ok && profile.HearingAid
}

// ReadDiagnostics reads the saved self-check record. Nil means the AirPods have none.
func ReadDiagnostics(exchange Exchanger, number string, now time.Time) (*Diagnostics, error) {
	if !Supported(number) {
		return nil, errors.New("Hardware diagnostics are not available on these AirPods.")
	}
	found, err := DiscoverRead(exchange, "Hardware diagnostics", diagnosticsUUID)
	if err != nil || found[0] == nil {
		return nil, err
	}
	value, err := readValue(exchange, found[0].Handle, "Could not read hardware diagnostics.")
	if err != nil {
		return nil, err
	}
	left, right, err := ParseDiagnostics(value)
	if err != nil {
		return nil, err
	}
	return &Diagnostics{ReadAt: now, Left: left, Right: right}, nil
}

// DeviceData is readable hearing data. The dosimetry values are raw: they do not
// establish a sound pressure.
type DeviceData struct {
	ReadAt            time.Time
	TestMode          *byte
	Sensitivity       *float32
	VolumeCurve       []float32
	AnyCharacteristic bool
}

func floats(value []byte, count int) ([]float32, error) {
	if len(value) != 4*count {
		return nil, errors.New("AirPods returned an unsupported dosimetry value length.")
	}
	out := make([]float32, count)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(value[4*i:]))
		if math.IsNaN(float64(out[i])) || math.IsInf(float64(out[i]), 0) {
			return nil, errors.New("AirPods returned a non-finite dosimetry value.")
		}
	}
	return out, nil
}

// ReadDeviceData reads what the AirPods expose; each item may be missing.
func ReadDeviceData(exchange Exchanger, now time.Time) (DeviceData, error) {
	data := DeviceData{ReadAt: now}
	found, err := DiscoverRead(exchange, "Hearing metadata", testModeUUID, sensitivityUUID, curveUUID)
	if err != nil {
		return data, err
	}
	for index, characteristic := range found {
		if characteristic == nil {
			continue
		}
		data.AnyCharacteristic = true
		value, err := readValue(exchange, characteristic.Handle, "Could not read hearing metadata.")
		if err != nil {
			return data, err
		}
		switch index {
		case 0:
			if len(value) != 1 {
				return data, errors.New("AirPods returned an unsupported hearing-test mode length.")
			}
			mode := value[0]
			data.TestMode = &mode
		case 1:
			values, err := floats(value, 1)
			if err != nil {
				return data, err
			}
			data.Sensitivity = &values[0]
		case 2:
			if data.VolumeCurve, err = floats(value, 17); err != nil {
				return data, err
			}
		}
	}
	return data, nil
}
