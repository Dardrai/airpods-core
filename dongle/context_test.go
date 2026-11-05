package dongle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dardrai/airpods-core/dongle/sim"
)

// soon is far below every wait these tests interrupt, and above any scheduling noise.
const soon = 2 * time.Second

func realClient(cfg sim.Config) (*Client, *sim.Device) {
	device := sim.New(cfg)
	return NewClient(device), device
}

func cancelSoon(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	t.Cleanup(func() {
		timer.Stop()
		cancel()
	})
	return ctx
}

func promptly(t *testing.T, start time.Time) {
	t.Helper()
	if elapsed := time.Since(start); elapsed > soon {
		t.Errorf("took %v", elapsed)
	}
}

func TestContextStopsATypedRequestAndCancelsItOnTheDongle(t *testing.T) {
	cfg := sim.Default()
	cfg.StallCommit = true
	client, device := realClient(cfg)
	start := time.Now()
	_, err := client.WithContext(cancelSoon(t)).TypedRequest([]byte{9}, nil)
	promptly(t, start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrNotSent) {
		t.Error("the request had gone out")
	}
	device.Update(func(c *sim.Config) { c.StallCommit = false })
	if _, err := client.TypedRequest([]byte{9}, nil); err != nil {
		t.Errorf("the channel was left busy: %v", err)
	}
}

func TestContextEndsTheWaitForATurn(t *testing.T) {
	cfg := sim.Default()
	cfg.StallCommit = true
	client, _ := realClient(cfg)
	holderCtx, stopHolder := context.WithCancel(context.Background())
	defer stopHolder()
	held := make(chan error, 1)
	go func() {
		_, err := client.WithContext(holderCtx).TypedRequest([]byte{9}, nil)
		held <- err
	}()
	// Wait until the holder really has the turn.
	for len(client.turn) == 0 {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	_, err := client.WithContext(cancelSoon(t)).Status()
	promptly(t, start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	select {
	case err := <-held:
		t.Fatalf("the holder should still be waiting: %v", err)
	default:
	}
	stopHolder()
	if err := <-held; !errors.Is(err, context.Canceled) {
		t.Fatalf("holder: %v", err)
	}
}

func TestContextEndsASettingConfirmation(t *testing.T) {
	cfg := sim.Default()
	cfg.WriteMode = sim.WriteLost
	client, device := realClient(cfg)
	settings, err := client.WithContext(cancelSoon(t)).SettingsClient()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = settings.Set("mode", 1)
	promptly(t, start)
	var unconfirmed *Unconfirmed
	if !errors.As(err, &unconfirmed) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrNotSent) {
		t.Error("the write had gone out")
	}
	if device.Writes() != 1 {
		t.Errorf("writes %d", device.Writes())
	}
}

func TestContextEndsAControlWait(t *testing.T) {
	cfg := sim.Default()
	cfg.ControlNoReceipt = true
	client, _ := realClient(cfg)
	start := time.Now()
	err := client.WithContext(cancelSoon(t)).Reconnect()
	promptly(t, start)
	var unconfirmed *Unconfirmed
	if !errors.As(err, &unconfirmed) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnEndedContextSendsNothing(t *testing.T) {
	client, device := newClient(sim.Default())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	view := client.WithContext(ctx)
	for name, call := range map[string]func() error{
		"status": func() error { _, err := view.Status(); return err },
		"typed":  func() error { _, err := view.TypedRequest([]byte{9}, nil); return err },
		"cached": func() error { _, err := view.Cached(SlotMetadata); return err },
		"sample": func() error { _, err := view.AudioState(); return err },
		"open":   func() error { _, err := view.OpenSession(); return err },
		"settings": func() error {
			_, err := view.SettingsClient()
			return err
		},
		"control": view.Reconnect,
	} {
		if err := call(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if ops, _ := device.TraceSummary(); ops != 0 {
		t.Errorf("%d operations reached the device", ops)
	}
	if _, err := client.Status(); err != nil {
		t.Errorf("the client itself is unaffected: %v", err)
	}
}

func TestViewsShareOneControlSequence(t *testing.T) {
	cfg := sim.Default()
	client, device := newClient(cfg)
	view := client.WithContext(context.Background())
	if err := client.SetListeningMode(ListeningANC); err != nil {
		t.Fatal(err)
	}
	if err := view.SetListeningMode(ListeningTransparency); err != nil {
		t.Fatal(err)
	}
	controls := device.Controls()
	if len(controls) != 2 || controls[0][18] == controls[1][18] {
		t.Errorf("sequences did not move on: %x", controls)
	}
}

func TestSessionCloseGivesTheLeaseBackAfterTheContextEnded(t *testing.T) {
	cfg := sim.Demo()
	cfg.Exchange = func([]byte) []byte { return []byte{0x0b} }
	client, device := newClient(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	session, err := client.WithContext(ctx).OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.AcquireLease(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := session.Exchange([]byte{1}); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrNotSent) {
		t.Fatalf("err = %v", err)
	}
	session.Close()
	if device.LeaseHeld() {
		t.Error("the lease was kept")
	}
}

func TestRealClockSleepEndsWithItsContext(t *testing.T) {
	sleeper := RealClock.(ContextSleeper)
	start := time.Now()
	if err := sleeper.SleepContext(cancelSoon(t), time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	promptly(t, start)
	if err := sleeper.SleepContext(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("err = %v", err)
	}
	start = time.Now()
	client, _ := realClient(sim.Default())
	client.WithContext(cancelSoon(t)).Clock().Sleep(time.Hour)
	promptly(t, start)
}

func TestClockKeepsAPlainClockAndHonoursAContextOnAny(t *testing.T) {
	client, _ := newClient(sim.Default())
	if _, plain := client.Clock().(*sim.VirtualClock); !plain {
		t.Errorf("without a context the clock is the one set: %T", client.Clock())
	}
	ctx, cancel := context.WithCancel(context.Background())
	view := client.WithContext(ctx)
	before := client.Clock().Now()
	view.Clock().Sleep(time.Second)
	if got := client.Clock().Now().Sub(before); got != time.Second {
		t.Errorf("slept %v", got)
	}
	cancel()
	before = client.Clock().Now()
	view.Clock().Sleep(time.Second)
	if got := client.Clock().Now().Sub(before); got != 0 {
		t.Errorf("slept %v after the context ended", got)
	}
}

func TestClientClose(t *testing.T) {
	client, device := newClient(sim.Default())
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if !device.Closed() {
		t.Error("device left open")
	}
	if _, err := client.Status(); err == nil {
		t.Error("a closed client still answered")
	}
	if err := client.Close(); err != nil {
		t.Errorf("closing twice: %v", err)
	}
}

func TestClientCloseWaitsForATransaction(t *testing.T) {
	cfg := sim.Default()
	cfg.StallCommit = true
	client, device := realClient(cfg)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.WithContext(ctx).TypedRequest([]byte{9}, nil)
	}()
	for len(client.turn) == 0 {
		time.Sleep(time.Millisecond)
	}
	closed := make(chan error, 1)
	go func() { closed <- client.Close() }()
	select {
	case <-closed:
		t.Fatal("Close did not wait")
	case <-time.After(20 * time.Millisecond):
	}
	if device.Closed() {
		t.Error("closed under the transaction")
	}
	stop()
	<-done
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}
