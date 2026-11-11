package dongle

import (
	"bytes"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
)

func TestFirmwareMajor(t *testing.T) {
	for value, want := range map[string]int{
		"6F21": 6, "9A5347a": 9, "6.8.8": 6, " 10A1 ": 10,
		"6619472": 0, "90.3431.6807": 0, "9": 0, "9A": 0, "9A348garbage": 0, "": 0, "100A1": 0, "0A1": 0,
	} {
		if got := FirmwareMajor(value); got != want {
			t.Errorf("%q: %d want %d", value, got, want)
		}
	}
}

func TestParseMetadata(t *testing.T) {
	meta, ok := ParseMetadata(sim.Metadata("A3063", "9A348", "ABC123"))
	if !ok || meta != (Metadata{"A3063", "9A348", "ABC123"}) {
		t.Fatalf("%+v %v", meta, ok)
	}
	packed := sim.Metadata("A2698", "9442752", "S")
	meta, ok = ParseMetadata(packed)
	if !ok || meta.Firmware != "9A348" {
		t.Errorf("packed: %+v %v", meta, ok)
	}
	if meta, _ := ParseMetadata(sim.Metadata("A2698", "9442753", "S")); meta.Firmware != "" {
		t.Errorf("prerelease decoded as %q", meta.Firmware)
	}
	for name, bad := range map[string][]byte{
		"short":   {4, 0, 4, 0, 0x1d},
		"header":  append([]byte{9}, sim.Metadata("A", "9A1", "S")[1:]...),
		"kind":    {4, 0, 4, 0, 0x1e, 0, 0, 0, 0, 0, 0, 0},
		"cut":     sim.Metadata("A3063", "9A348", "S")[:20],
		"control": sim.Metadata("A\x01", "9A1", "S"),
		"empty":   sim.Metadata("", "", ""),
	} {
		if _, ok := ParseMetadata(bad); ok {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCachedSlots(t *testing.T) {
	cfg := sim.Demo()
	cfg.Cached[5] = sim.CachedReport{Age: 12, Revision: 7, Packet: []byte{4, 0, 4, 0, 6, 0, 0, 0}}
	delete(cfg.Cached, SlotHearingAid)
	client := NewClient(sim.New(cfg))
	got, err := client.Cached(SlotEarStatus)
	if err != nil || got == nil || got.AgeMs != 12 || got.Revision != 7 || len(got.Packet) != 8 {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := client.Cached(SlotHearingAid); got != nil || err != nil {
		t.Errorf("empty slot: %+v %v", got, err)
	}
	// Slot 6 answers with the settings snapshot, not a kept report, and 8 is past the last slot.
	for _, slot := range []byte{0, 6, 8} {
		if _, err := client.Cached(slot); err == nil {
			t.Errorf("slot %d read", slot)
		}
	}
	eq := []byte{4, 0, 4, 0, 0x63, 0, 5, 0, 1, 1, 30, 50, 70}
	cfg.Cached[SlotEQ] = sim.CachedReport{Age: 3, Revision: 9, Packet: eq}
	client = NewClient(sim.New(cfg))
	if got, err := client.Cached(SlotEQ); err != nil || got == nil || got.Revision != 9 || !bytes.Equal(got.Packet, eq) {
		t.Errorf("EQ slot: %+v %v", got, err)
	}
	if _, err := DecodeCached(make([]byte, 8)); err == nil {
		t.Error("short envelope")
	}
	if _, err := DecodeCached(make([]byte, 12)); err == nil {
		t.Error("revision zero")
	}
}
