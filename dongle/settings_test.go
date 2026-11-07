package dongle

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

type settingsGolden struct {
	Settings []struct {
		Name    string             `json:"name"`
		Control byte               `json:"control"`
		Help    string             `json:"help"`
		Encode  map[string]*uint32 `json:"encode"`
		Decode  map[string]string  `json:"decode"`
	} `json:"settings"`
	Snapshots []struct {
		Name    string              `json:"name"`
		Hex     string              `json:"hex"`
		Records map[string][]uint32 `json:"records"`
		Error   string              `json:"error"`
	} `json:"snapshots"`
	Rejected    map[string]string `json:"rejected"`
	Unsupported string            `json:"unsupported"`
}

func loadSettingsGolden(t *testing.T) settingsGolden {
	t.Helper()
	data, err := os.ReadFile("testdata/settings_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden settingsGolden
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	return golden
}

func TestSettingsTableGolden(t *testing.T) {
	golden := loadSettingsGolden(t)
	if len(golden.Settings) != len(Settings) {
		t.Fatalf("%d settings, want %d", len(Settings), len(golden.Settings))
	}
	for i, want := range golden.Settings {
		s := &Settings[i]
		if s.Name != want.Name || s.Control != want.Control || s.ValueHelp() != want.Help {
			t.Errorf("row %d = %s %#x %q, want %s %#x %q", i, s.Name, s.Control, s.ValueHelp(), want.Name, want.Control, want.Help)
		}
		for input, expected := range want.Encode {
			got, err := s.Encode(input)
			switch {
			case expected == nil && (err == nil || err.Error() != want.Help):
				t.Errorf("%s encode %q = %d %v, want rejection", s.Name, input, got, err)
			case expected != nil && (err != nil || got != *expected):
				t.Errorf("%s encode %q = %d %v, want %d", s.Name, input, got, err, *expected)
			}
		}
		for rawText, expected := range want.Decode {
			raw, _ := strconv.ParseUint(rawText, 10, 32)
			if got := s.Decode(uint32(raw)); got != expected {
				t.Errorf("%s decode %s = %q, want %q", s.Name, rawText, got, expected)
			}
		}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	inputs := func(s *Setting) []string {
		switch s.Kind {
		case KindNamed:
			var words []string
			for _, v := range s.Values {
				words = append(words, v.Word)
			}
			return words
		case KindPercent:
			return []string{"0", "1", "50", "100"}
		case KindModes:
			return []string{"off,anc", "off,anc,transparency,adaptive", "transparency,adaptive"}
		}
		return []string{"noise-control,siri", "siri,noise-control", "siri,siri", "noise-control,noise-control"}
	}
	for _, table := range [][]Setting{Settings, ExtraSettings} {
		for i := range table {
			s := &table[i]
			for _, input := range inputs(s) {
				raw, err := s.Encode(input)
				if err != nil {
					t.Fatalf("%s encode %q: %v", s.Name, input, err)
				}
				if got := s.Decode(raw); got != input {
					t.Errorf("%s: %q -> %#x -> %q", s.Name, input, raw, got)
				}
			}
		}
	}
}

func TestPercentRejectsHugeDigitStrings(t *testing.T) {
	s, _ := LookupSetting("case-volume")
	for _, input := range []string{"18446744073709551716", strings.Repeat("9", 400), "1" + strings.Repeat("0", 64)} {
		if _, err := s.Encode(input); err == nil {
			t.Errorf("%q accepted", input)
		}
	}
	if raw, err := s.Encode(strings.Repeat("0", 400) + "100"); err != nil || raw != 100 {
		t.Errorf("leading zeros: %d %v", raw, err)
	}
}

func TestHoldActionsLeftIsLowByte(t *testing.T) {
	s, _ := LookupSetting("hold-actions")
	raw, err := s.Encode("noise-control,siri")
	if err != nil || raw != 0x0501 {
		t.Fatalf("raw = %#x, %v", raw, err)
	}
}

func TestExtraSettings(t *testing.T) {
	for name, control := range map[string]byte{"connect-automatically": 0x20, "allow-auto-connect": 0x36} {
		if _, ok := LookupSetting(name); ok {
			t.Errorf("%s is in the CLI table", name)
		}
		s, ok := LookupAnySetting(name)
		if !ok || s.Control != control {
			t.Fatalf("%s: %v", name, s)
		}
		if on, _ := s.Encode("on"); on != 1 {
			t.Errorf("%s on = %d", name, on)
		}
		if off, _ := s.Encode("off"); off != 2 {
			t.Errorf("%s off = %d", name, off)
		}
	}
}

func TestSnapshotGolden(t *testing.T) {
	for _, tc := range loadSettingsGolden(t).Snapshots {
		data, _ := hex.DecodeString(tc.Hex)
		records, err := DecodeSnapshot(data)
		if tc.Error != "" {
			if err == nil || err.Error() != tc.Error {
				t.Errorf("%s: %v, want %q", tc.Name, err, tc.Error)
			}
			continue
		}
		if err != nil || len(records) != len(tc.Records) {
			t.Errorf("%s: %v %v", tc.Name, records, err)
		}
		for key, want := range tc.Records {
			control, _ := strconv.Atoi(key)
			got := records[byte(control)]
			if got.AgeMs != want[0] || got.Revision != want[1] || got.Value != want[2] {
				t.Errorf("%s %s: %+v, want %v", tc.Name, key, got, want)
			}
		}
	}
}

func TestErrorTextGolden(t *testing.T) {
	golden := loadSettingsGolden(t)
	for code, want := range golden.Rejected {
		n, _ := strconv.Atoi(code)
		if got := (&RequestRejected{byte(n)}).Error(); got != want {
			t.Errorf("code %s: %q, want %q", code, got, want)
		}
	}
	if got := (&Unsupported{Message: golden.Unsupported}).Error(); got != golden.Unsupported {
		t.Errorf("unsupported: %q", got)
	}
}
