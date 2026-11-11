package dongle

import (
	"errors"
	"strings"
	"testing"

	"github.com/Dardrai/airpods-core/dongle/sim"
)

// policyRequests returns every kind 23 request the dongle received.
func policyRequests(device *sim.Device) [][]byte {
	var found [][]byte
	for _, payload := range device.Commits() {
		if len(payload) > 0 && payload[0] == kindReconnectPolicy {
			found = append(found, payload)
		}
	}
	return found
}

func TestDecodeReconnectPolicy(t *testing.T) {
	for _, tc := range []struct {
		reply []byte
		want  ReconnectPolicy
		ok    bool
	}{
		{[]byte{1, 0}, ReconnectManual, true},
		{[]byte{1, 1}, ReconnectAutomatic, true},
		{[]byte{1, 2}, 0, false},
		{[]byte{2, 0}, 0, false},
		{[]byte{1}, 0, false},
		{[]byte{1, 0, 0}, 0, false},
	} {
		got, err := DecodeReconnectPolicy(tc.reply)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("% x: %v %v", tc.reply, got, err)
		}
	}
	if ReconnectManual.String() != "manual" || ReconnectAutomatic.String() != "auto" || ReconnectPolicy(5).String() != "unknown (5)" {
		t.Error("names")
	}
}

func TestReconnectPolicyIsSavedOnceAndVerified(t *testing.T) {
	client, device := newClient(sim.Default())
	if policy, err := client.ReconnectPolicy(); err != nil || policy != ReconnectManual {
		t.Fatalf("default: %v %v", policy, err)
	}
	if changed, err := client.SetReconnectPolicy(ReconnectAutomatic); err != nil || !changed {
		t.Fatalf("save: %v %v", changed, err)
	}
	if policy, err := client.ReconnectPolicy(); err != nil || policy != ReconnectAutomatic {
		t.Fatalf("after: %v %v", policy, err)
	}
	// The saved value is not written again.
	if changed, err := client.SetReconnectPolicy(ReconnectAutomatic); err != nil || changed {
		t.Fatalf("again: %v %v", changed, err)
	}
	var writes int
	for _, request := range policyRequests(device) {
		if len(request) == 2 {
			writes++
			if request[1] != 1 {
				t.Errorf("wrote % x", request)
			}
		}
	}
	if writes != 1 {
		t.Errorf("%d writes", writes)
	}
	if _, err := client.SetReconnectPolicy(2); !errors.Is(err, ErrNotSent) {
		t.Errorf("mode 2: %v", err)
	}
	if reply, err := client.TypedRequest([]byte{kindReconnectPolicy, 2}, nil); !rejected(1)(err) {
		t.Errorf("raw mode 2: %x %v", reply, err)
	}
}

// Any settings lease blocks a save, the caller's own too; reading stays possible.
func TestReconnectPolicySaveIsBusyUnderALeaseOrPlayback(t *testing.T) {
	client, device := newClient(sim.Default())
	token, err := client.AcquireLease()
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SetReconnectPolicy(ReconnectAutomatic)
	var refused *RequestRejected
	if !errors.As(err, &refused) || refused.Code != 2 || !strings.Contains(err.Error(), "this app's own included") {
		t.Fatalf("with the lease: %v", err)
	}
	if policy, err := client.ReconnectPolicy(); err != nil || policy != ReconnectManual {
		t.Errorf("read under the lease: %v %v", policy, err)
	}
	if err := client.ReleaseLease(token); err != nil {
		t.Fatal(err)
	}
	device.SetFlags(0x43)
	if _, err := client.SetReconnectPolicy(ReconnectAutomatic); !errors.As(err, &refused) || refused.Code != 2 {
		t.Errorf("during playback: %v", err)
	}
	device.SetFlags(0x03)
	if changed, err := client.SetReconnectPolicy(ReconnectAutomatic); err != nil || !changed {
		t.Errorf("idle: %v %v", changed, err)
	}
}

func TestReconnectPolicyOnOldFirmware(t *testing.T) {
	cfg := sim.Default()
	cfg.ReconnectPolicyUnsupported = true
	client, device := newClient(cfg)
	_, err := client.ReconnectPolicy()
	var unsupported *Unsupported
	if !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "older than 0.1.63, or its saved configuration is unreadable") {
		t.Fatalf("read: %v", err)
	}
	_, err = client.SetReconnectPolicy(ReconnectAutomatic)
	var unconfirmed *Unconfirmed
	if !errors.As(err, &unsupported) || errors.As(err, &unconfirmed) || !strings.HasSuffix(err.Error(), "nothing was saved") {
		t.Fatalf("save: %v", err)
	}
	if n := len(policyRequests(device)); n != 3 {
		t.Errorf("%d requests, want two reads and one save", n)
	}
}

// A save the dongle could not verify may have persisted: it is Unconfirmed, never repeated,
// and the policy reads as unsupported until a later save verifies.
func TestUnverifiedReconnectPolicySave(t *testing.T) {
	cfg := sim.Default()
	cfg.ReconnectPolicyWriteFails = true
	client, device := newClient(cfg)
	_, err := client.SetReconnectPolicy(ReconnectAutomatic)
	var unconfirmed *Unconfirmed
	if !errors.As(err, &unconfirmed) || !rejected(6)(err) || errors.Is(err, ErrNotSent) ||
		!strings.Contains(err.Error(), "may have been saved. Do not retry it automatically") {
		t.Fatalf("save: %v", err)
	}
	if n := len(policyRequests(device)); n != 2 {
		t.Errorf("%d requests, want the read and one save", n)
	}
	var unsupported *Unsupported
	if _, err := client.ReconnectPolicy(); !errors.As(err, &unsupported) {
		t.Errorf("read after: %v", err)
	}
	device.Update(func(c *sim.Config) { c.ReconnectPolicyWriteFails = false })
	if changed, err := client.SetReconnectPolicy(ReconnectManual); err != nil || !changed {
		t.Errorf("a later save: %v %v", changed, err)
	}
	if policy, err := client.ReconnectPolicy(); err != nil || policy != ReconnectManual {
		t.Errorf("read after the later save: %v %v", policy, err)
	}
}

func TestReconnectPolicyMalformedReplies(t *testing.T) {
	cfg := sim.Default()
	cfg.Replies = map[byte]sim.Hex{kindReconnectPolicy: {1, 2}}
	client, device := newClient(cfg)
	if _, err := client.ReconnectPolicy(); err == nil || err.Error() != "Unsupported dongle reconnect policy response." {
		t.Errorf("read: %v", err)
	}
	if _, err := client.SetReconnectPolicy(ReconnectAutomatic); err == nil || len(policyRequests(device)) != 2 {
		t.Errorf("save after a malformed read: %v, %d requests", err, len(policyRequests(device)))
	}
	// A save whose echo is not the requested policy may still have been saved.
	cfg.Replies = map[byte]sim.Hex{kindReconnectPolicy: {1, 0}}
	client, _ = newClient(cfg)
	_, err := client.SetReconnectPolicy(ReconnectAutomatic)
	var unconfirmed *Unconfirmed
	if !errors.As(err, &unconfirmed) || !strings.Contains(err.Error(), "the dongle reports manual") {
		t.Errorf("echo: %v", err)
	}
}
