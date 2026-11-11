// Package model knows what each AirPods model can do. A report from the AirPods
// confirms a value, not what a model is capable of, so the UI checks both.
package model

import "strings"

// Profile is one AirPods hardware family member.
type Profile struct {
	Models             []string
	Name               string
	Family             string
	Headphone          bool
	Listening          bool
	Adaptive           bool
	AllowOffOption     bool
	HighQualityMic     bool
	CustomEQ           bool
	HearingAid         bool
	CustomTransparency bool
	LoudSoundReduction bool
	Controls           []byte
}

const (
	familyPro2 = "airpods_pro_2"
	familyPro3 = "airpods_pro_3"
)

var (
	pro2Controls = []byte{0x01, 0x0a, 0x0d, 0x16, 0x17, 0x18, 0x1a, 0x1b, 0x1f, 0x20, 0x23, 0x24, 0x25, 0x26, 0x28, 0x2e, 0x2f, 0x31, 0x34, 0x35, 0x36, 0x3b, 0x40}
	pro3Controls = []byte{0x01, 0x0a, 0x0d, 0x16, 0x17, 0x18, 0x1a, 0x1b, 0x1f, 0x20, 0x23, 0x24, 0x25, 0x26, 0x28, 0x2e, 0x2f, 0x31, 0x34, 0x35, 0x36, 0x37, 0x3b, 0x40}
	maxControls  = []byte{0x0a, 0x0d, 0x1a, 0x1c, 0x1f, 0x20, 0x24, 0x3b}
	max2Controls = []byte{0x0a, 0x0d, 0x1a, 0x1c, 0x1f, 0x20, 0x24, 0x26, 0x28, 0x2e, 0x35, 0x3b}
	airPods5     = []byte{0x01, 0x0a, 0x0d, 0x16, 0x17, 0x18, 0x1a, 0x1b, 0x1f, 0x20, 0x24, 0x26, 0x28, 0x2e, 0x35, 0x3b}
)

var profiles = []Profile{
	{Models: []string{"A1523", "A1722"}, Name: "AirPods (1st generation)", Family: "airpods_1",
		Controls: []byte{0x01, 0x0a, 0x1f}},
	{Models: []string{"A2032", "A2031"}, Name: "AirPods (2nd generation)", Family: "airpods_2",
		Controls: []byte{0x01, 0x0a, 0x1f, 0x20}},
	{Models: []string{"A2565", "A2564"}, Name: "AirPods (3rd generation)", Family: "airpods_3",
		Controls: []byte{0x01, 0x0a, 0x16, 0x17, 0x18, 0x1f, 0x20, 0x24, 0x3b}},
	{Models: []string{"A3053", "A3050", "A3054"}, Name: "AirPods 4", Family: "airpods_4", HighQualityMic: true, CustomEQ: true,
		Controls: []byte{0x01, 0x0a, 0x16, 0x17, 0x18, 0x1f, 0x20, 0x24, 0x26, 0x35, 0x3b}},
	{Models: []string{"A3056", "A3055", "A3057"}, Name: "AirPods 4 (ANC)", Family: "airpods_4", Listening: true, Adaptive: true, HighQualityMic: true, CustomEQ: true,
		Controls: []byte{0x01, 0x0a, 0x0d, 0x16, 0x17, 0x18, 0x1a, 0x1b, 0x1f, 0x20, 0x24, 0x26, 0x28, 0x2e, 0x31, 0x35, 0x3b, 0x40}},
	{Models: []string{"A3531", "A3532", "A3533"}, Name: "AirPods 5 (USB-C case)", Family: "airpods_5", Listening: true, Adaptive: true, HighQualityMic: true, CustomEQ: true,
		Controls: airPods5},
	{Models: []string{"A3439", "A3440", "A3441"}, Name: "AirPods 5 (wireless charging case)", Family: "airpods_5", Listening: true, Adaptive: true, HighQualityMic: true, CustomEQ: true,
		Controls: []byte{0x01, 0x0a, 0x0d, 0x16, 0x17, 0x18, 0x1a, 0x1b, 0x1f, 0x20, 0x23, 0x24, 0x25, 0x26, 0x28, 0x2e, 0x31, 0x35, 0x3b, 0x40}},
	{Models: []string{"A2084", "A2083"}, Name: "AirPods Pro (1st generation)", Family: "airpods_pro_1", Listening: true, CustomTransparency: true,
		Controls: []byte{0x01, 0x0a, 0x0d, 0x16, 0x17, 0x18, 0x1a, 0x1b, 0x1f, 0x20, 0x24, 0x3b}},
	{Models: []string{"A2931", "A2699", "A2698"}, Name: "AirPods Pro 2 (Lightning)", Family: familyPro2, Listening: true, Adaptive: true, AllowOffOption: true, HighQualityMic: true, CustomEQ: true, HearingAid: true, CustomTransparency: true, LoudSoundReduction: true,
		Controls: pro2Controls},
	{Models: []string{"A3047", "A3048", "A3049"}, Name: "AirPods Pro 2 (USB-C)", Family: familyPro2, Listening: true, Adaptive: true, AllowOffOption: true, HighQualityMic: true, CustomEQ: true, HearingAid: true, CustomTransparency: true, LoudSoundReduction: true,
		Controls: pro2Controls},
	{Models: []string{"A3063", "A3064", "A3065"}, Name: "AirPods Pro 3", Family: familyPro3, Listening: true, Adaptive: true, AllowOffOption: true, HighQualityMic: true, CustomEQ: true, HearingAid: true, CustomTransparency: true, LoudSoundReduction: true,
		Controls: pro3Controls},
	{Models: []string{"A2096"}, Name: "AirPods Max (Lightning)", Family: "airpods_max", Headphone: true, Listening: true,
		Controls: maxControls},
	{Models: []string{"A3184"}, Name: "AirPods Max (USB-C)", Family: "airpods_max", Headphone: true, Listening: true,
		Controls: maxControls},
	{Models: []string{"A3454"}, Name: "AirPods Max 2", Family: "airpods_max", Headphone: true, Listening: true, Adaptive: true, HighQualityMic: true, CustomEQ: true,
		Controls: max2Controls},
	{Models: []string{"A3157"}, Name: "Powerbeats Pro 2", Family: "powerbeats_pro_2", Listening: true,
		Controls: []byte{0x0a, 0x0d}},
}

// Find looks a model number up, ignoring case and surrounding space.
func Find(model string) (*Profile, bool) {
	model = strings.TrimSpace(model)
	for i := range profiles {
		for _, candidate := range profiles[i].Models {
			if strings.EqualFold(candidate, model) {
				return &profiles[i], true
			}
		}
	}
	return nil, false
}

// HasControl reports whether a profile lists a control.
func (p *Profile) HasControl(control byte) bool {
	for _, candidate := range p.Controls {
		if candidate == control {
			return true
		}
	}
	return false
}
