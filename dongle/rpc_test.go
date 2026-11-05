package dongle

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
	"github.com/Dardrai/airpods-core/hid"
)

func newClient(cfg sim.Config) (*Client, *sim.Device) {
	device := sim.New(cfg)
	client := NewClient(device)
	client.SetClock(sim.NewVirtualClock())
	return client, device
}

func TestTypedRequestPayloadBounds(t *testing.T) {
	client, device := newClient(sim.Default())
	for _, size := range []int{0, 43} {
		_, err := client.TypedRequest(make([]byte, size), nil)
		if err == nil || err.Error() != "Typed requests must fit one 1..42 byte HID fragment" {
			t.Errorf("size %d: %v", size, err)
		}
	}
	if ops, _ := device.TraceSummary(); ops != 0 {
		t.Errorf("bad payload reached the device: %d ops", ops)
	}
}

func TestTypedRequestGenerationPin(t *testing.T) {
	client, _ := newClient(sim.Default())
	stale := uint32(6)
	_, err := client.TypedRequest([]byte{2}, &stale)
	if err == nil || err.Error() != "Pico session changed; run the command again" {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, ErrGenerationChanged) || !errors.Is(err, ErrNotSent) {
		t.Fatalf("err = %v lost its identity", err)
	}
}

func TestTypedRequestGenerationChanges(t *testing.T) {
	// Traffic: 1 G2, 2 G3, 3 G2 (send), 4 G2 + 5 G3 (wait), 6 G2 (send), 7 G2 + 8 G3 (wait), 9 G2 (end).
	for at, want := range map[int]string{
		3: "Pico session changed during request",
		4: "Pico session changed during request",
		6: "Pico session changed during request",
		7: "Pico session changed during request",
		9: "Pico session changed during read",
	} {
		cfg := sim.Default()
		cfg.GenerationChangeAtGet = at
		client, _ := newClient(cfg)
		_, err := client.TypedRequest([]byte{9}, nil)
		if err == nil || err.Error() != want {
			t.Errorf("change at get %d: %v, want %q", at, err, want)
		}
		if !errors.Is(err, ErrGenerationChanged) {
			t.Errorf("change at get %d: not ErrGenerationChanged", at)
		}
		if notSent := at == 3; errors.Is(err, ErrNotSent) != notSent {
			t.Errorf("change at get %d: ErrNotSent should be %v", at, notSent)
		}
	}
}

func TestTypedRequestFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*sim.Config)
		check  func(error) bool
	}{
		{"busy staged", func(c *sim.Config) { c.InitialPhase = 1 }, rejected(2)},
		{"busy processing", func(c *sim.Config) { c.InitialPhase = 2 }, rejected(2)},
		{"phase 4 code", func(c *sim.Config) { c.Fail = map[byte]byte{2: 5} }, rejected(5)},
		{"phase 4 unsupported", func(c *sim.Config) { c.Fail = map[byte]byte{2: 7} }, func(err error) bool {
			var target *Unsupported
			return errors.As(err, &target) && err.Error() == "Firmware does not support this request for this device"
		}},
		{"stage timeout", func(c *sim.Config) { c.StallStage = true }, timedOut},
		{"commit timeout", func(c *sim.Config) { c.StallCommit = true }, timedOut},
		{"foreign sequence", func(c *sim.Config) { c.ForeignSequence = true }, timedOut},
		{"oversized", func(c *sim.Config) { c.Tamper = sim.TamperOversizedPage }, text("Oversized HID page")},
		{"huge total", func(c *sim.Config) { c.Tamper = sim.TamperHugeTotal }, text("Invalid diagnostic length")},
		{"empty page", func(c *sim.Config) { c.Tamper = sim.TamperEmptyPage }, text("Invalid diagnostic pagination")},
		{"excess", func(c *sim.Config) { c.Tamper = sim.TamperExcessPage }, text("Diagnostic page exceeds declared length")},
		{"total changes", func(c *sim.Config) { c.Tamper = sim.TamperTotalChanges }, text("Invalid diagnostic pagination")},
		{"get transport", func(c *sim.Config) { c.GetFailAt = 2; c.FailMessage = "boom" }, text("boom")},
		{"set transport", func(c *sim.Config) { c.SetFailAt = 2; c.FailMessage = "boom" }, text("boom")},
		{"bad identity", func(c *sim.Config) { c.BadIdentityReport = 3 }, func(err error) bool { return errors.Is(err, hid.ErrUnsupportedReport) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sim.Default()
			tc.mutate(&cfg)
			client, _ := newClient(cfg)
			if _, err := client.TypedRequest([]byte{2}, nil); !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func rejected(code byte) func(error) bool {
	return func(err error) bool {
		var target *RequestRejected
		return errors.As(err, &target) && target.Code == code
	}
}
func timedOut(err error) bool {
	return errors.Is(err, ErrRPCTimeout) && err.Error() == "Pico diagnostic read timed out"
}
func text(want string) func(error) bool {
	return func(err error) bool { return err != nil && err.Error() == want }
}

func TestTypedRequestPagination(t *testing.T) {
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	for _, size := range []int{1, 13, 42} {
		cfg := sim.Default()
		cfg.Replies = map[byte]sim.Hex{9: payload[:200]}
		cfg.PageSize = size
		client, _ := newClient(cfg)
		got, err := client.TypedRequest([]byte{9}, nil)
		if err != nil || !bytes.Equal(got, payload[:200]) {
			t.Fatalf("page size %d: %d bytes, %v", size, len(got), err)
		}
	}
	cfg := sim.Default()
	cfg.Replies = map[byte]sim.Hex{9: payload}
	client, _ := newClient(cfg)
	if got, err := client.TypedRequest([]byte{9}, nil); err != nil || len(got) != 4096 {
		t.Fatalf("largest response: %d %v", len(got), err)
	}
}

func TestSequenceComesFromReportAndSkipsZero(t *testing.T) {
	for initial, want := range map[uint32]uint32{0: 1, 41: 42, 0xffffffff: 1} {
		cfg := sim.Default()
		cfg.InitialSequence = initial
		device := &recorder{Device: sim.New(cfg)}
		client := NewClient(device)
		client.SetClock(sim.NewVirtualClock())
		if _, err := client.TypedRequest([]byte{2}, nil); err != nil {
			t.Fatal(err)
		}
		for _, frame := range device.sent {
			if got := binary.LittleEndian.Uint32(frame[12:]); got != want {
				t.Errorf("initial %d: sequence %d, want %d", initial, got, want)
			}
		}
	}
}

type recorder struct {
	*sim.Device
	sent [][hid.ReportSize]byte
}

func (r *recorder) Set(data [hid.ReportSize]byte) error {
	r.sent = append(r.sent, data)
	return r.Device.Set(data)
}

func TestRequestFrameLayout(t *testing.T) {
	device := &recorder{Device: sim.New(sim.Default())}
	client := NewClient(device)
	client.SetClock(sim.NewVirtualClock())
	if _, err := client.TypedRequest([]byte{1, 0x0d, 3, 0, 0, 0}, nil); err != nil {
		t.Fatal(err)
	}
	frame := device.sent[0]
	want := []byte{3, 'A', 'P', 'D', 'G', 1, 64, 7, 7, 0, 0, 0, 1, 0, 0, 0, 6, 0, 0, 0, 6, 0, 1, 0x0d, 3, 0, 0, 0}
	if !bytes.Equal(frame[:len(want)], want) {
		t.Fatalf("frame = % x", frame[:len(want)])
	}
	if commit := device.sent[1]; commit[7] != 6 || commit[20] != 0 {
		t.Fatalf("commit = % x", commit[:24])
	}
}

func TestFeatureHelpers(t *testing.T) {
	client, device := newClient(sim.Default())
	audio, err := client.AudioState()
	if err != nil || audio.Codec != 2 || audio.Bitrate != 192000 || audio.ReportedDelayTenthsMs != nil || audio.PrimingMs != 100 {
		t.Fatalf("audio: %+v %v", audio, err)
	}
	if _, err := client.SetAudioProfile(3, true, 174000); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetAudioProfile(2, true, 174000); err == nil {
		t.Fatal("variable AAC accepted")
	}
	if _, err := client.SetGameMode(true); err != nil {
		t.Fatal(err)
	}
	perf, err := client.Performance()
	if err != nil || perf.UptimeMs == nil {
		t.Fatalf("performance: %+v %v", perf, err)
	}
	near, err := client.NearbyStart()
	if err != nil || !near.Active || near.RSSI != nil || near.LastHeardAgeMs != nil {
		t.Fatalf("nearby start: %+v %v", near, err)
	}
	if near, err = client.NearbySnapshot(); err != nil || near.Active {
		t.Fatalf("nearby snapshot: %+v %v", near, err)
	}
	if err := client.NearbyStop(); err != nil {
		t.Fatal(err)
	}
	token, err := client.AcquireLease()
	if err != nil || token != 0x76543210 {
		t.Fatalf("lease: %#x %v", token, err)
	}
	if err := client.RenewLease(token); err != nil {
		t.Fatal(err)
	}
	if err := client.ReleaseLease(token); err != nil {
		t.Fatal(err)
	}
	if err := client.RenewLease(1); !rejected(3)(err) {
		t.Fatalf("wrong token: %v", err)
	}
	want := [][]byte{{9}, {3, 3, 1, 0xb0, 0xa7, 2, 0}, {4, 1}, {5}, {6}, {7}, {8}, {10, 0, 0, 0, 0, 0},
		{10, 1, 0x10, 0x32, 0x54, 0x76}, {10, 2, 0x10, 0x32, 0x54, 0x76}, {10, 1, 1, 0, 0, 0}}
	got := device.Commits()
	if len(got) != len(want) {
		t.Fatalf("commits: %v", got)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("commit %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestFeatureDecodersRejectBadLayouts(t *testing.T) {
	audio := make([]byte, 20)
	audio[0] = 1
	binary.LittleEndian.PutUint16(audio[18:], 100)
	if _, err := DecodeAudioState(audio); err != nil {
		t.Fatal(err)
	}
	for offset, value := range map[int]byte{0: 2, 1: 4, 2: 4, 3: 2, 12: 2, 13: 7, 14: 3, 15: 16, 18: 99} {
		bad := bytes.Clone(audio)
		bad[offset] = value
		if _, err := DecodeAudioState(bad); err == nil {
			t.Errorf("audio offset %d accepted", offset)
		}
	}
	perf := bytes.Repeat([]byte{0xff}, 64)
	perf[0], perf[1], perf[2], perf[3] = 1, 1, 3, 15
	binary.LittleEndian.PutUint32(perf[8:], 10)
	binary.LittleEndian.PutUint32(perf[12:], 20)
	state, err := DecodePerformance(perf)
	if err != nil || state.UptimeMs != nil || *state.PCMUsed != 10 {
		t.Fatalf("performance: %+v %v", state, err)
	}
	binary.LittleEndian.PutUint32(perf[8:], 21)
	if _, err := DecodePerformance(perf); err == nil {
		t.Error("over capacity accepted")
	}
	near := make([]byte, 24)
	near[0], near[1], near[2], near[3] = 1, 0x82, byte(0xbf), 1
	binary.LittleEndian.PutUint32(near[4:], 123)
	q8 := int32(-64 * 256)
	binary.LittleEndian.PutUint32(near[20:], uint32(q8))
	parsed, err := DecodeNearby(near)
	if err != nil || !parsed.Active || *parsed.RSSI != -64 || *parsed.LastHeardAgeMs != 123 || parsed.Trend != "stronger" {
		t.Fatalf("nearby: %+v %v", parsed, err)
	}
	near[1] &= 0x7f
	if _, err := DecodeNearby(near); err == nil {
		t.Error("nearby without identity accepted")
	}
	for _, bad := range [][]byte{{}, {1}, {1, 0, 0, 0, 0}, {2, 1, 0, 0, 0}, {1, 1, 0, 0, 0, 0}} {
		if _, err := DecodeLeaseToken(bad); err == nil {
			t.Errorf("lease reply %v accepted", bad)
		}
	}
}

// A failed request must not leave the channel busy for the next one, on a client or a session.
func TestFailedRequestsAreCancelled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*sim.Config)
		clear  func(*sim.Config)
	}{
		{"commit timeout", func(c *sim.Config) { c.StallCommit = true }, func(c *sim.Config) { c.StallCommit = false }},
		{"stage timeout", func(c *sim.Config) { c.StallStage = true }, func(c *sim.Config) { c.StallStage = false }},
		{"commit stuck in phase 2", func(c *sim.Config) { c.CommitDelay = 100000 }, func(c *sim.Config) { c.CommitDelay = 0 }},
		{"bad page", func(c *sim.Config) { c.Tamper = sim.TamperEmptyPage }, func(c *sim.Config) { c.Tamper = "" }},
	} {
		for _, kind := range []string{"client", "session"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				cfg := sim.Default()
				tc.mutate(&cfg)
				client, device := newClient(cfg)
				request := func() ([]byte, error) { return client.TypedRequest([]byte{9}, nil) }
				if kind == "session" {
					session, err := client.OpenSession()
					if err != nil {
						t.Fatal(err)
					}
					defer session.Close()
					request = func() ([]byte, error) { return session.Feature([]byte{9}) }
				}
				if _, err := request(); err == nil {
					t.Fatal("expected the first request to fail")
				}
				device.Update(tc.clear)
				if _, err := request(); err != nil {
					t.Fatalf("the next request: %v", err)
				}
			})
		}
	}
}

// A reconnect in the middle of a request must not leave it staged under the old generation.
func TestAReconnectMidRequestIsCancelled(t *testing.T) {
	// Traffic as in TestTypedRequestGenerationChanges, then the wait for the stalled commit.
	for at := 4; at <= 9; at++ {
		for _, kind := range []string{"client", "session"} {
			cfg := sim.Default()
			cfg.GenerationChangeAtGet = at
			cfg.StallCommit = true
			device := &recordingDevice{Device: sim.New(cfg)}
			client := NewClient(device)
			client.SetClock(sim.NewVirtualClock())
			var err error
			if kind == "session" {
				// OpenSession reads the status first, so the same traffic is one Get later.
				device.Update(func(c *sim.Config) { c.GenerationChangeAtGet++ })
				session, openErr := client.OpenSession()
				if openErr != nil {
					t.Fatal(openErr)
				}
				_, err = session.Feature([]byte{9})
				session.Close()
			} else {
				_, err = client.TypedRequest([]byte{9}, nil)
			}
			if !errors.Is(err, ErrGenerationChanged) || errors.Is(err, ErrNotSent) {
				t.Errorf("change at get %d, %s: %v", at, kind, err)
			}
			if device.ops[opCancel] != 1 {
				t.Errorf("change at get %d, %s: sent %d cancels", at, kind, device.ops[opCancel])
			}
			device.Update(func(c *sim.Config) { c.StallCommit = false })
			for range 2 {
				if _, err := client.TypedRequest([]byte{9}, nil); err != nil {
					t.Errorf("change at get %d, %s: the channel was left busy: %v", at, kind, err)
				}
			}
			if device.ops[opCancel] != 1 {
				t.Errorf("change at get %d, %s: later requests sent cancels", at, kind)
			}
		}
	}
}

// What an earlier generation left staged is stale, not a competing app.
func TestAStaleStagedRequestIsCancelledAndAForeignOneIsBusy(t *testing.T) {
	for _, phase := range []byte{phaseStaged, phaseProcessing} {
		cfg := sim.Default()
		cfg.InitialPhase = phase
		cfg.InitialSequence = 41
		device := &recorder{Device: sim.New(cfg)}
		client := NewClient(device)
		client.SetClock(sim.NewVirtualClock())
		if _, err := client.TypedRequest([]byte{9}, nil); !rejected(2)(err) || !errors.Is(err, ErrNotSent) {
			t.Fatalf("phase %d, same generation: %v", phase, err)
		}
		if len(device.sent) != 0 {
			t.Fatalf("phase %d: sent %d reports to a busy channel", phase, len(device.sent))
		}
		device.BumpGeneration()
		if _, err := client.TypedRequest([]byte{9}, nil); err != nil {
			t.Fatalf("phase %d, earlier generation: %v", phase, err)
		}
		le := binary.LittleEndian
		cancel := device.sent[0]
		if cancel[7] != opCancel || le.Uint32(cancel[8:]) != cfg.Generation || le.Uint32(cancel[12:]) != 41 {
			t.Errorf("phase %d: cancel = % x", phase, cancel[:24])
		}
		if stage := device.sent[1]; le.Uint32(stage[8:]) != cfg.Generation+1 || le.Uint32(stage[12:]) != 42 {
			t.Errorf("phase %d: request = % x", phase, stage[:24])
		}
	}
}

func TestRejectedAndUnsentRequestsAreNotCancelled(t *testing.T) {
	const cancelOp = 5
	for _, tc := range []struct {
		name   string
		mutate func(*sim.Config)
	}{
		{"the dongle ended it", func(c *sim.Config) { c.Fail = map[byte]byte{9: 5} }},
		{"unsupported", func(c *sim.Config) { c.Fail = map[byte]byte{9: 7} }},
		{"busy before anything was sent", func(c *sim.Config) { c.InitialPhase = 2 }},
		{"the generation moved before the first Set", func(c *sim.Config) { c.GenerationChangeAtGet = 3 }},
		{"a read failed before the first Set", func(c *sim.Config) { c.GetFailAt = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sim.Default()
			tc.mutate(&cfg)
			device := &recordingDevice{Device: sim.New(cfg)}
			client := NewClient(device)
			client.SetClock(sim.NewVirtualClock())
			if _, err := client.TypedRequest([]byte{9}, nil); err == nil {
				t.Fatal("expected a failure")
			}
			if device.ops[cancelOp] != 0 {
				t.Errorf("sent %d cancels", device.ops[cancelOp])
			}
		})
	}
}

// recordingDevice counts the report 3 operations asked of a device.
type recordingDevice struct {
	*sim.Device
	ops [256]int
}

func (d *recordingDevice) Set(data [hid.ReportSize]byte) error {
	if data[0] == 3 {
		d.ops[data[7]]++
	}
	return d.Device.Set(data)
}

func TestSetFailureStillCancels(t *testing.T) {
	cfg := sim.Default()
	cfg.SetFailAt = 2 // the commit
	device := &recordingDevice{Device: sim.New(cfg)}
	client := NewClient(device)
	client.SetClock(sim.NewVirtualClock())
	if _, err := client.TypedRequest([]byte{9}, nil); err == nil {
		t.Fatal("expected a failure")
	}
	if device.ops[5] != 1 {
		t.Errorf("sent %d cancels", device.ops[5])
	}
}
