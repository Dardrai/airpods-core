package dongle

import (
	"sync"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
	"github.com/Dardrai/airpods-core/hid"
)

// transactionChecker fails when report 3 traffic of two requests interleaves: a stage must be
// followed by its own commit, on the same sequence, before another request starts.
type transactionChecker struct {
	*sim.Device
	t        *testing.T
	mu       sync.Mutex
	open     bool
	sequence [4]byte
}

func (d *transactionChecker) Set(data [hid.ReportSize]byte) error {
	if data[0] == 3 {
		d.mu.Lock()
		switch op := data[7]; {
		case d.open && ([4]byte(data[12:16])) != d.sequence:
			d.t.Errorf("op %d of sequence %x inside the request of sequence %x", op, data[12:16], d.sequence)
		case op == opCommit:
			d.open = false
		case op == opCached || op == opFeature:
			d.open, d.sequence = true, [4]byte(data[12:16])
		}
		d.mu.Unlock()
	}
	return d.Device.Set(data)
}

// TestOneClientFromManyGoroutines is meant for -race: everything derived from one client
// shares its lock, so requests never interleave and every one of them succeeds.
func TestOneClientFromManyGoroutines(t *testing.T) {
	const rounds = 40
	device := &transactionChecker{Device: sim.New(sim.Demo()), t: t}
	client := NewClient(device)
	client.SetClock(sim.NewVirtualClock())
	settings, err := client.SettingsClient()
	if err != nil {
		t.Fatal(err)
	}
	other, err := client.SettingsClient()
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	work := map[string]func() error{
		"status": func() error { _, err := client.Status(); return err },
		"typed":  func() error { _, err := client.TypedRequest([]byte{kindPerformance}, nil); return err },
		"read":   func() error { _, err := settings.Read(); return err },
		"read2":  func() error { _, err := other.Read(); return err },
		"set":    func() error { _, err := settings.Set("ear-detection", 2); return err },
		"send":   func() error { _, err := other.Send("mode", 2); return err },
		"cached": func() error { _, err := client.Cached(SlotMetadata); return err },
		"audio":  func() error { _, err := client.AudioState(); return err },
		"feature": func() error {
			_, err := session.Feature([]byte{kindPerformance})
			return err
		},
		"session-cached": func() error { _, err := session.Cached(SlotMetadata); return err },
		"control":        func() error { return client.SetListeningMode(ListeningTransparency) },
	}
	var wg sync.WaitGroup
	for name, job := range work {
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range rounds {
					if err := job(); err != nil {
						t.Errorf("%s: %v", name, err)
						return
					}
				}
			}()
		}
	}
	wg.Wait()
}
