package disconnect

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dardrai/airpods-core/dongle"
	"github.com/Dardrai/airpods-core/dongle/sim"
	"github.com/Dardrai/airpods-core/hid"
)

// before0159 is what firmware up to 0.1.58 advertises: everything below routing.
const before0159 uint16 = 0x1ff

func connect(t *testing.T, cfg sim.Config) (*sim.Device, *dongle.Client, dongle.Status) {
	t.Helper()
	device := sim.New(cfg)
	client := dongle.NewClient(device)
	client.SetClock(sim.NewVirtualClock())
	status, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	return device, client, status
}

// handovers returns every kind 21 request the dongle received.
func handovers(device *sim.Device) [][]byte {
	var found [][]byte
	for _, payload := range device.Commits() {
		if len(payload) > 0 && payload[0] == 21 {
			found = append(found, payload)
		}
	}
	return found
}

func TestErrUnsupportedText(t *testing.T) {
	const want = "this dongle firmware has no command to disconnect AirPods while keeping them paired; update it to 0.1.59 or newer, or put the AirPods in their case to disconnect"
	if got := ErrUnsupported.Error(); got != want {
		t.Errorf("text = %q", got)
	}
}

func TestFirmwareWithoutRoutingNeverGetsARequest(t *testing.T) {
	for _, capabilities := range []uint16{0, dongle.CapControl, 0x7f, before0159, 0xffff &^ dongle.CapRouting} {
		cfg := sim.Default()
		cfg.Capabilities = capabilities
		device, client, status := connect(t, cfg)
		backend := Pick(client, status)
		if kind := backend.Kind(); kind != KindUnsupported {
			t.Errorf("capabilities %#x: Kind() = %q", capabilities, kind)
		}
		if _, err := backend.Disconnect(); !errors.Is(err, ErrUnsupported) {
			t.Errorf("capabilities %#x: Disconnect() = %v", capabilities, err)
		}
		if _, err := Acquire(client); !errors.Is(err, ErrAcquireUnsupported) {
			t.Errorf("capabilities %#x: Acquire() = %v", capabilities, err)
		}
		if _, _, sets := device.Counts(); sets != 0 {
			t.Errorf("capabilities %#x: %d writes", capabilities, sets)
		}
	}
}

func TestPickFollowsTheRoutingBit(t *testing.T) {
	if got := Pick(nil, dongle.Status{Capabilities: dongle.CapRouting}).Kind(); got != "routing" {
		t.Errorf("with the bit: %q", got)
	}
	if got := Pick(nil, dongle.Status{Capabilities: before0159}).Kind(); got != "unsupported" {
		t.Errorf("without the bit: %q", got)
	}
	if _, err := Pick(nil, dongle.Status{Capabilities: dongle.CapRouting}).Disconnect(); err == nil {
		t.Error("no error without a client")
	}
}

func TestReleaseWaitsForTheCloseAndKeepsThePair(t *testing.T) {
	cfg := sim.Default()
	cfg.RoutingCloseDelay = 3
	device, client, status := connect(t, cfg)
	outcome, err := Pick(client, status).Disconnect()
	if err != nil || outcome != Released {
		t.Fatalf("Disconnect() = %v, %v", outcome, err)
	}
	sent := handovers(device)
	if len(sent) != 1 {
		t.Fatalf("sent %d handovers", len(sent))
	}
	le := binary.LittleEndian
	if len(sent[0]) != 10 || sent[0][1] != dongle.HandoverRelease || le.Uint32(sent[0][2:]) != 1 || le.Uint32(sent[0][6:]) == 0 {
		t.Errorf("request % x", sent[0])
	}
	if len(device.Controls()) != 0 {
		t.Error("a control command was sent; pair (opcode 2) would erase the pairing")
	}
	after, _ := client.Status()
	if after.Connected || after.Peer != status.Peer || after.Generation == status.Generation {
		t.Errorf("after release: %+v", after)
	}
	routing, err := client.Routing()
	if err != nil || !routing.Has(dongle.RoutingReleased) || routing.LastAction != 1 || routing.Revision != 2 {
		t.Errorf("routing %+v %v", routing, err)
	}
	// A second release finds it done and sends nothing.
	if outcome, err := Pick(client, status).Disconnect(); err != nil || outcome != Released || len(handovers(device)) != 1 {
		t.Errorf("again: %v %v, %d handovers", outcome, err, len(handovers(device)))
	}
}

// A release that closes at once changes the generation before its reply is read; the
// receipt is found in a fresh routing read instead of sending again.
func TestReleaseReplyLostToTheCloseIsRecovered(t *testing.T) {
	device, client, status := connect(t, sim.Default())
	outcome, err := Pick(client, status).Disconnect()
	if err != nil || outcome != Released {
		t.Fatalf("Disconnect() = %v, %v", outcome, err)
	}
	if n := len(handovers(device)); n != 1 {
		t.Errorf("sent %d handovers", n)
	}
}

func TestReleaseNotClosedInTimeIsPendingNotAnError(t *testing.T) {
	cfg := sim.Default()
	cfg.RoutingCloseDelay = 1000
	device, client, status := connect(t, cfg)
	outcome, err := Pick(client, status).Disconnect()
	if err != nil || outcome != ReleasePending {
		t.Fatalf("Disconnect() = %v, %v", outcome, err)
	}
	if outcome.String() != "release accepted, not yet closed" {
		t.Errorf("text %q", outcome)
	}
	if n := len(handovers(device)); n != 1 {
		t.Errorf("sent %d handovers", n)
	}
	// Asking again while it closes waits instead of sending another release.
	if outcome, err := Pick(client, status).Disconnect(); err != nil || outcome != ReleasePending || len(handovers(device)) != 1 {
		t.Errorf("again: %v %v, %d handovers", outcome, err, len(handovers(device)))
	}
}

func TestStaleIntentIsReportedNotRetried(t *testing.T) {
	stale := make([]byte, 32)
	stale[0], stale[1], stale[2] = 1, 32, 0x63
	binary.LittleEndian.PutUint32(stale[4:], 99) // the sim's own revision is 1
	cfg := sim.Default()
	cfg.Replies = map[byte]sim.Hex{20: stale}
	device, client, status := connect(t, cfg)
	_, err := Pick(client, status).Disconnect()
	if !errors.Is(err, ErrIntentChanged) || !strings.Contains(err.Error(), "read status again") || !strings.Contains(err.Error(), "now owned by the dongle") {
		t.Fatalf("Disconnect() = %v", err)
	}
	if n := len(handovers(device)); n != 1 {
		t.Errorf("sent %d handovers", n)
	}
}

func TestRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*sim.Config)
		code   byte
		text   string
	}{
		{"busy", func(c *sim.Config) { c.RoutingBusy = true }, 2, "Game Mode change"},
		{"microphone", func(c *sim.Config) { c.Flags = 0x13 }, 2, "the microphone or a call is in use"},
		{"no pair", func(c *sim.Config) { c.Peer = sim.Hex{0, 0, 0, 0, 0, 0} }, 4, "no saved AirPods pair"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sim.Default()
			tc.mutate(&cfg)
			device, client, status := connect(t, cfg)
			_, err := Pick(client, status).Disconnect()
			var rejected *dongle.RequestRejected
			if !errors.As(err, &rejected) || rejected.Code != tc.code || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("Disconnect() = %v", err)
			}
			if n := len(handovers(device)); n != 1 {
				t.Errorf("sent %d handovers", n)
			}
		})
	}
}

func TestSettingsLeaseBlocksRelease(t *testing.T) {
	_, client, status := connect(t, sim.Default())
	session, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.AcquireLease(); err != nil {
		t.Fatal(err)
	}
	_, err = Pick(client, status).Disconnect()
	var rejected *dongle.RequestRejected
	if !errors.As(err, &rejected) || rejected.Code != 2 || !strings.Contains(err.Error(), "settings lease") {
		t.Errorf("Disconnect() = %v", err)
	}
	session.Close()
	if outcome, err := Pick(client, status).Disconnect(); err != nil || outcome != Released {
		t.Errorf("after the lease: %v %v", outcome, err)
	}
}

func TestUncertainReplyWithoutReceiptIsUnconfirmed(t *testing.T) {
	cfg := sim.Default()
	cfg.Fail = map[byte]byte{21: 5}
	device, client, status := connect(t, cfg)
	_, err := Pick(client, status).Disconnect()
	var unconfirmed *dongle.Unconfirmed
	if !errors.As(err, &unconfirmed) || !strings.HasPrefix(err.Error(), "release: not confirmed: RPC failure: device timeout") {
		t.Fatalf("Disconnect() = %v", err)
	}
	if n := len(handovers(device)); n != 1 {
		t.Errorf("sent %d handovers", n)
	}
}

func TestReconnectEndsTheRelease(t *testing.T) {
	_, client, status := connect(t, sim.Default())
	if _, err := Pick(client, status).Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := client.Reconnect(); err != nil {
		t.Fatal(err)
	}
	after, _ := client.Status()
	routing, err := client.Routing()
	if err != nil || !after.Connected || routing.Has(dongle.RoutingReleased) || routing.LastAction != 0 || routing.Revision != 3 {
		t.Errorf("after reconnect: %+v %+v %v", after, routing, err)
	}
}

func TestAcquire(t *testing.T) {
	device, client, status := connect(t, sim.Default())
	if _, err := Pick(client, status).Disconnect(); err != nil {
		t.Fatal(err)
	}
	outcome, err := Acquire(client)
	if err != nil || outcome != Acquired {
		t.Fatalf("Acquire() = %v, %v", outcome, err)
	}
	sent := handovers(device)
	if len(sent) != 2 || sent[1][1] != dongle.HandoverAcquire || binary.LittleEndian.Uint32(sent[1][2:]) != 2 {
		t.Fatalf("handovers % x", sent)
	}
	if after, _ := client.Status(); !after.Connected {
		t.Error("not connected after the takeover")
	}

	cfg := sim.Default()
	cfg.Flags = 0
	cfg.RoutingTakeoverDelay = 1000
	_, client, _ = connect(t, cfg)
	if outcome, err := Acquire(client); err != nil || outcome != AcquirePending {
		t.Errorf("slow takeover: %v %v", outcome, err)
	}
}

func TestAcquireWhenOwnedSendsNothing(t *testing.T) {
	device, client, _ := connect(t, sim.Default())
	if outcome, err := Acquire(client); err != nil || outcome != Acquired || len(handovers(device)) != 0 {
		t.Errorf("Acquire() = %v %v, %d handovers", outcome, err, len(handovers(device)))
	}
}

// Ownership alone is not a finished takeover: audio and controls must be up too.
func TestAcquireNeedsTheWholeOwningState(t *testing.T) {
	cfg := sim.Default()
	cfg.Flags = 0x01 // connected, controls not ready
	device, client, _ := connect(t, cfg)
	if r, _ := client.Routing(); !r.Has(dongle.RoutingOwned) || acquired(r) {
		t.Fatalf("flags %#x", r.Flags)
	}
	if outcome, err := Acquire(client); err != nil || outcome != AcquirePending || len(handovers(device)) != 1 {
		t.Errorf("Acquire() = %v %v, %d handovers", outcome, err, len(handovers(device)))
	}
}

func TestReleaseNeedsTheWholeReleasedState(t *testing.T) {
	routing := func(flags byte) sim.Hex {
		reply := make([]byte, 32)
		reply[0], reply[1], reply[2], reply[4] = 1, 32, flags, 1
		return reply
	}
	for _, tc := range []struct {
		flags byte
		want  Outcome
	}{
		{0x08, Released},
		{0x89, Released},       // whether the AirPods said who owns them and microphone use do not count
		{0x48, ReleasePending}, // released, but the AirPods controls are still up
	} {
		cfg := sim.Default()
		cfg.Replies = map[byte]sim.Hex{20: routing(tc.flags)}
		device, client, status := connect(t, cfg)
		if outcome, err := Pick(client, status).Disconnect(); err != nil || outcome != tc.want || len(handovers(device)) != 0 {
			t.Errorf("flags %#x: %v %v, %d handovers", tc.flags, outcome, err, len(handovers(device)))
		}
	}
}

// injectOnHandover runs inject just before a handover request reaches the dongle, a set
// number of times: another choice made between the routing read and the send.
type injectOnHandover struct {
	*sim.Device
	inject func()
	times  int
}

func (d *injectOnHandover) Set(data [hid.ReportSize]byte) error {
	if data[0] == 3 && data[7] == 7 && data[22] == 21 && d.times > 0 {
		d.times--
		d.inject()
	}
	return d.Device.Set(data)
}

// stalePair is a client whose first handovers each meet a choice made through another client.
func stalePair(t *testing.T, cfg sim.Config, times int, choose func(other *dongle.Client)) (*sim.Device, *dongle.Client, dongle.Status) {
	t.Helper()
	device := sim.New(cfg)
	other := dongle.NewClient(device)
	other.SetClock(sim.NewVirtualClock())
	client := dongle.NewClient(&injectOnHandover{device, func() { choose(other) }, times})
	client.SetClock(sim.NewVirtualClock())
	status, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	return device, client, status
}

func reconnect(other *dongle.Client) { _ = other.Reconnect() }

// A reconnect or an automatic attempt between the read and the send makes the request stale;
// nothing was applied, so one fresh request goes out, and only one.
func TestStaleFromATakeoverIntentGetsOneFreshRequest(t *testing.T) {
	device, client, status := stalePair(t, sim.Default(), 1, reconnect)
	if outcome, err := Pick(client, status).Disconnect(); err != nil || outcome != Released {
		t.Fatalf("Disconnect() = %v, %v", outcome, err)
	}
	sent := handovers(device)
	if len(sent) != 2 || binary.LittleEndian.Uint32(sent[0][2:]) != 1 || binary.LittleEndian.Uint32(sent[1][2:]) != 2 {
		t.Fatalf("handovers % x", sent)
	}

	device, client, status = stalePair(t, sim.Default(), 2, reconnect)
	_, err := Pick(client, status).Disconnect()
	if !errors.Is(err, ErrIntentChanged) || !strings.HasPrefix(err.Error(), "routing intent changed again during one fresh request; read status again (now owned by the dongle)") {
		t.Fatalf("stale twice: %v", err)
	}
	if n := len(handovers(device)); n != 2 {
		t.Errorf("sent %d handovers", n)
	}
}

// Another app's accepted handover is its choice: it is not overridden, unless it already
// reached what this request wanted.
func TestStaleFromAnotherHandoverIsNotRetried(t *testing.T) {
	release := func(other *dongle.Client) {
		r, _ := other.Routing()
		if _, err := other.Handover(dongle.HandoverRelease, r.Revision, 4242); err != nil {
			t.Error(err)
		}
	}
	away := sim.Default()
	away.Flags = 0
	device, client, _ := stalePair(t, away, 1, release)
	_, err := Acquire(client)
	if !errors.Is(err, ErrIntentChanged) || !strings.Contains(err.Error(), "now released") {
		t.Fatalf("Acquire() = %v", err)
	}
	if n := len(handovers(device)); n != 2 {
		t.Errorf("%d handovers, want the other app's and one", n)
	}
	device, client, status := stalePair(t, away, 1, release)
	if outcome, err := Pick(client, status).Disconnect(); err != nil || outcome != Released || len(handovers(device)) != 2 {
		t.Fatalf("Disconnect() = %v %v, %d handovers", outcome, err, len(handovers(device)))
	}
}

// automatic is a dongle away from its AirPods under the Automatic reconnect policy: right after
// each routing read it starts a takeover, so the revision just read is already stale.
func automatic(takeoverDelay int) sim.Config {
	cfg := sim.Default()
	cfg.Flags = 0
	cfg.ReconnectPolicy = 1
	cfg.RoutingTakeoverDelay = takeoverDelay
	return cfg
}

func TestStaleUnderTheAutomaticPolicy(t *testing.T) {
	// The dongle would refuse a release as busy until its own takeover ends.
	device, client, status := connect(t, automatic(1000))
	_, err := Pick(client, status).Disconnect()
	if !errors.Is(err, ErrIntentChanged) || !strings.Contains(err.Error(), "now takeover pending") || len(handovers(device)) != 1 {
		t.Fatalf("release: %v, %d handovers", err, len(handovers(device)))
	}
	// An acquire follows the dongle's own takeover instead of sending another.
	device, client, _ = connect(t, automatic(3))
	if outcome, err := Acquire(client); err != nil || outcome != Acquired || len(handovers(device)) != 1 {
		t.Fatalf("acquire: %v %v, %d handovers", outcome, err, len(handovers(device)))
	}
	device, client, _ = connect(t, automatic(1000))
	if outcome, err := Acquire(client); err != nil || outcome != AcquirePending || len(handovers(device)) != 1 {
		t.Fatalf("slow acquire: %v %v, %d handovers", outcome, err, len(handovers(device)))
	}
}

// A reconnect while the release closes replaces it.
func TestReplacedReleaseIsIntentChanged(t *testing.T) {
	cfg := sim.Default()
	cfg.RoutingCloseDelay = 1000
	device := sim.New(cfg)
	other := dongle.NewClient(device)
	other.SetClock(sim.NewVirtualClock())
	client := dongle.NewClient(device)
	client.SetClock(reconnectOnSleep{sim.NewVirtualClock(), other})
	status, _ := client.Status()
	_, err := Pick(client, status).Disconnect()
	if !errors.Is(err, ErrIntentChanged) || len(handovers(device)) != 1 {
		t.Fatalf("Disconnect() = %v, %d handovers", err, len(handovers(device)))
	}
}

type reconnectOnSleep struct {
	*sim.VirtualClock
	other *dongle.Client
}

func (c reconnectOnSleep) Sleep(d time.Duration) {
	_ = c.other.Reconnect()
	c.VirtualClock.Sleep(d)
}

func TestNonceIsFreshAndNonzero(t *testing.T) {
	saved := nonceSource
	defer func() { nonceSource = saved }()
	draws := []uint32{0, 5, 5, 9}
	nonceSource = func() uint32 {
		n := draws[0]
		draws = draws[1:]
		return n
	}
	if got := newNonce(5); got != 9 {
		t.Errorf("newNonce = %d", got)
	}
}

// closedOnHandover refuses the first report of a handover as a closed device would.
type closedOnHandover struct{ *sim.Device }

func (d closedOnHandover) Set(data [hid.ReportSize]byte) error {
	if data[0] == 3 && data[7] == 7 && data[22] == 21 {
		return hid.ErrClosed
	}
	return d.Device.Set(data)
}

// A handover that never reached the dongle is returned as it is, not explained as a routing
// refusal and not looked for in later reads.
func TestHandoverNotSentIsReturnedAsIs(t *testing.T) {
	device := sim.New(sim.Default())
	client := dongle.NewClient(closedOnHandover{device})
	client.SetClock(sim.NewVirtualClock())
	status, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Pick(client, status).Disconnect()
	var unconfirmed *dongle.Unconfirmed
	if !errors.Is(err, dongle.ErrNotSent) || !errors.Is(err, hid.ErrClosed) || errors.As(err, &unconfirmed) {
		t.Fatalf("Disconnect() = %v", err)
	}
	if reads := len(device.Commits()); reads != 1 {
		t.Errorf("%d requests, want only the routing read before the handover", reads)
	}
}

// cancelOnSleep ends a context at the first sleep, which in a release is the first wait
// between routing reads.
type cancelOnSleep struct {
	*sim.VirtualClock
	cancel context.CancelFunc
}

func (c cancelOnSleep) Sleep(d time.Duration) {
	c.cancel()
	c.VirtualClock.Sleep(d)
}

func TestEndedContextStopsTheWaitWithThePendingOutcome(t *testing.T) {
	cfg := sim.Default()
	cfg.RoutingCloseDelay = 1000
	device := sim.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	client := dongle.NewClient(device)
	client.SetClock(cancelOnSleep{sim.NewVirtualClock(), cancel})
	view := client.WithContext(ctx)
	status, err := view.Status()
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := Pick(view, status).Disconnect()
	if err != nil || outcome != ReleasePending || ctx.Err() == nil {
		t.Fatalf("Disconnect() = %v, %v", outcome, err)
	}
	if n := len(handovers(device)); n != 1 {
		t.Errorf("sent %d handovers", n)
	}
}
