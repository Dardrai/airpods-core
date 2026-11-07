package dongle

import (
	"errors"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
)

func TestDecodeRouting(t *testing.T) {
	good := make([]byte, 32)
	good[0], good[1], good[2], good[3] = 1, 32, 0x63, 2
	copy(good[4:], []byte{7, 0, 0, 0, 9, 0, 0, 0, 1, 2, 3, 4, 5, 6, 2, 0, 10, 0, 0, 0, 20, 0, 0, 0, 0x44, 0x33, 0x22, 0x11})
	r, err := DecodeRouting(good)
	if err != nil {
		t.Fatal(err)
	}
	want := Routing{Flags: 0x63, TakeoverStage: 2, Revision: 7, ControlSession: 9, Peer: [6]byte{1, 2, 3, 4, 5, 6},
		LastAction: 2, UptimeMs: 10, TakeoverAgeMs: 20, LastNonce: 0x11223344}
	if r != want || !r.Has(RoutingOwned|RoutingKnown) || r.Has(RoutingReleased) {
		t.Errorf("%+v", r)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"short":      func(b []byte) []byte { return b[:31] },
		"version":    func(b []byte) []byte { b[0] = 2; return b },
		"length":     func(b []byte) []byte { b[1] = 31; return b },
		"revision 0": func(b []byte) []byte { clear(b[4:8]); return b },
		"action 3":   func(b []byte) []byte { b[18] = 3; return b },
		"long":       func(b []byte) []byte { return append(b, 0) },
	} {
		if _, err := DecodeRouting(mutate(append([]byte(nil), good...))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestHandoverFencing(t *testing.T) {
	client, device := newClient(sim.Default())
	if _, err := client.Handover(2, 1, 1); err == nil {
		t.Error("action 2 accepted")
	}
	if _, err := client.Handover(HandoverRelease, 0, 1); err == nil {
		t.Error("revision 0 accepted")
	}
	if _, err := client.Handover(HandoverRelease, 1, 0); err == nil {
		t.Error("nonce 0 accepted")
	}
	if len(device.Commits()) != 0 {
		t.Fatal("an invalid request was sent")
	}
	before, err := client.Routing()
	if err != nil || before.Revision != 1 || !before.Has(RoutingOwned) {
		t.Fatalf("%+v %v", before, err)
	}
	stale := func(revision, nonce uint32) {
		t.Helper()
		var rejected *RequestRejected
		if _, err := client.Handover(HandoverAcquire, revision, nonce); !errors.As(err, &rejected) || rejected.Code != 3 {
			t.Errorf("revision %d nonce %d: %v", revision, nonce, err)
		}
	}
	stale(2, 5)
	after, err := client.Handover(HandoverAcquire, 1, 5)
	if err != nil || after.Revision != 2 || after.LastNonce != 5 || after.LastAction != 2 {
		t.Fatalf("%+v %v", after, err)
	}
	stale(1, 6) // the old revision
	stale(2, 5) // the nonce already used
}

func TestHandoverBlockedByAnyLease(t *testing.T) {
	client, _ := newClient(sim.Default())
	token, err := client.AcquireLease()
	if err != nil {
		t.Fatal(err)
	}
	var rejected *RequestRejected
	if _, err := client.Handover(HandoverRelease, 1, 1); !errors.As(err, &rejected) || rejected.Code != 2 {
		t.Errorf("with the lease: %v", err)
	}
	if _, err := client.Routing(); err != nil {
		t.Errorf("reading stays possible: %v", err)
	}
	if err := client.ReleaseLease(token); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Handover(HandoverAcquire, 1, 1); err != nil {
		t.Errorf("after the lease: %v", err)
	}
}

func TestReleaseFlagsMoveFromPendingToReleased(t *testing.T) {
	cfg := sim.Default()
	cfg.RoutingCloseDelay = 2
	client, _ := newClient(cfg)
	accepted, err := client.Handover(HandoverRelease, 1, 77)
	if err != nil || !accepted.Has(RoutingReleasePending) || accepted.Has(RoutingReleased) || accepted.LastAction != 1 {
		t.Fatalf("%+v %v", accepted, err)
	}
	for range 2 {
		if r, err := client.Routing(); err != nil || !r.Has(RoutingReleasePending) {
			t.Fatalf("%+v %v", r, err)
		}
	}
	// The close ends the AirPods session, so this read sees the generation change.
	if _, err := client.Routing(); err == nil {
		t.Fatal("the closing read kept its generation")
	}
	r, err := client.Routing()
	if err != nil || !r.Has(RoutingReleased) || r.Has(RoutingReleasePending|RoutingA2DP) {
		t.Fatalf("%+v %v", r, err)
	}
	var rejected *RequestRejected
	if _, err := client.Handover(HandoverRelease, r.Revision, 78); err != nil {
		t.Errorf("a released dongle accepts another release: %v", err)
	}
	cfg.Flags = 0x13
	client, _ = newClient(cfg)
	if _, err := client.Handover(HandoverRelease, 1, 1); !errors.As(err, &rejected) || rejected.Code != 2 {
		t.Errorf("microphone: %v", err)
	}
}
