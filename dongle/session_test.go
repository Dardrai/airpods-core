package dongle

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
)

func openSession(t *testing.T, cfg sim.Config) (*Session, *sim.Device) {
	t.Helper()
	dev := sim.New(cfg)
	client := NewClient(dev)
	client.SetClock(sim.NewVirtualClock())
	session, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	return session, dev
}

func TestMultiFragmentPayloadsAndPagedReplies(t *testing.T) {
	payload := bytes.Repeat([]byte{0xab}, 140) // the size of the hearing packet
	response := bytes.Repeat([]byte{0xcd}, 107)
	var got []byte
	cfg := sim.Demo()
	cfg.Exchange = func(pdu []byte) []byte { got = append([]byte(nil), pdu...); return response }
	session, dev := openSession(t, cfg)
	defer session.Close()
	reply, err := session.Exchange(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload arrived as %d bytes", len(got))
	}
	if !bytes.Equal(reply, response) {
		t.Errorf("reply %d bytes", len(reply))
	}
	if len(dev.Exchanges()) != 1 {
		t.Errorf("exchanges %d", len(dev.Exchanges()))
	}
}

func TestRawRequestsNeedTheLeaseCapability(t *testing.T) {
	cfg := sim.Demo()
	cfg.Capabilities &^= CapLease
	session, dev := openSession(t, cfg)
	defer session.Close()
	if _, err := session.Exchange([]byte{1}); err == nil || !strings.Contains(err.Error(), "Update the dongle firmware") {
		t.Errorf("err %v", err)
	}
	if err := session.SendAAP([]byte{1}); err == nil {
		t.Error("no capability")
	}
	if len(dev.Commits()) != 0 {
		t.Errorf("sent %x", dev.Commits())
	}
}

func TestLeaseLifetime(t *testing.T) {
	cfg := sim.Demo()
	cfg.Exchange = func([]byte) []byte { return []byte{0x0b} }
	session, dev := openSession(t, cfg)
	if _, err := session.Feature([]byte{9}); err != nil {
		t.Fatal(err)
	}
	if len(dev.LeaseEvents()) != 0 {
		t.Error("a typed read needs no lease")
	}
	if _, err := session.Exchange([]byte{0x0a, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := session.SendAAP([]byte{4, 0, 4, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Cached(SlotMetadata); err != nil {
		t.Fatal(err)
	}
	session.Close()
	want := "acquire renew renew release"
	if got := strings.Join(dev.LeaseEvents(), " "); got != want {
		t.Errorf("events %q want %q", got, want)
	}
	if dev.LeaseHeld() {
		t.Error("lease still held")
	}
	if _, err := session.Feature([]byte{9}); !errors.Is(err, ErrLeaseEnded) {
		t.Errorf("after close: %v", err)
	}
}

func TestRejectedRenewalEndsTheSession(t *testing.T) {
	cfg := sim.Demo()
	cfg.Exchange = func([]byte) []byte { return []byte{0x0b} }
	session, dev := openSession(t, cfg)
	defer session.Close()
	if err := session.AcquireLease(); err != nil {
		t.Fatal(err)
	}
	dev.Update(func(c *sim.Config) { c.Fail = map[byte]byte{10: 3} })
	if _, err := session.Exchange([]byte{1}); err == nil {
		t.Fatal("renewal should fail")
	}
	dev.Update(func(c *sim.Config) { c.Fail = nil })
	if _, err := session.Exchange([]byte{1}); !errors.Is(err, ErrLeaseEnded) {
		t.Errorf("a replacement lease must not be taken: %v", err)
	}
	if got := dev.LeaseEvents(); len(got) != 1 {
		t.Errorf("events %v", got)
	}
}

func TestFailureCancelsAndIsNotRetried(t *testing.T) {
	cfg := sim.Demo()
	cfg.StallCommit = true
	session, dev := openSession(t, cfg)
	if _, err := session.Feature([]byte{9}); !errors.Is(err, ErrRPCTimeout) {
		t.Fatalf("err %v", err)
	}
	if session.request.active {
		t.Error("the cancel receipt was not seen")
	}
	if got := len(dev.Commits()); got != 1 {
		t.Errorf("attempts %d", got)
	}
}

func TestSessionRefusesAnotherGeneration(t *testing.T) {
	session, dev := openSession(t, sim.Demo())
	dev.BumpGeneration()
	if _, err := session.Feature([]byte{9}); err == nil {
		t.Error("stale session")
	}
}

func TestSessionSeesABusyChannel(t *testing.T) {
	cfg := sim.Demo()
	cfg.InitialPhase = 1
	session, dev := openSession(t, cfg)
	var rejected *RequestRejected
	if _, err := session.Feature([]byte{9}); !errors.As(err, &rejected) || rejected.Code != 2 {
		t.Errorf("err %v", err)
	}
	if len(dev.Commits()) != 0 {
		t.Error("sent on a busy channel")
	}
}

func TestPayloadBounds(t *testing.T) {
	session, _ := openSession(t, sim.Demo())
	if _, err := session.Feature(nil); err == nil {
		t.Error("empty")
	}
	if _, err := session.Feature(make([]byte, 513)); err == nil {
		t.Error("too long")
	}
	if _, err := session.Cached(9); err == nil {
		t.Error("slot")
	}
}
