package edge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// errHubDown stands in for whatever the hub returns while it is unreachable.
var errHubDown = errors.New("hub unreachable")

// setWriteErr flips the fake's write failure on or off.
func (f *fakeTwinKV) setWriteErr(err error) {
	f.mu.Lock()
	f.writeErr = err
	f.mu.Unlock()
}

// writeFailCount reports how many writes the fake has rejected.
func (f *fakeTwinKV) writeFailCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeFails
}

// withShortRetry shrinks the relay's retry interval for the duration of a test.
func withShortRetry(t *testing.T, d time.Duration) {
	t.Helper()
	previous := twinRetryInterval
	twinRetryInterval = d
	t.Cleanup(func() { twinRetryInterval = previous })
}

// runRelayUntil starts the reported pump and stops it once cond holds, or fails
// the test after timeout. Polling beats a fixed sleep here because the retry
// tick is what is under test.
func runRelayUntil(t *testing.T, local, hub *fakeTwinKV, timeout time.Duration, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = pumpReported(ctx, local, hub) }()

	deadline := time.Now().Add(timeout)
	met := false
	for time.Now().Before(deadline) {
		if cond() {
			met = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	if !met {
		t.Fatalf("condition never held within %s", timeout)
	}
}

// A write the hub rejects must be retried until it lands.
//
// This is the bug the pending set exists for. The previous relay logged a failed
// write and dropped it, on the stated grounds that the key would be "re-offered
// by the next watcher restart's replay" -- but the watcher is on the LOCAL
// bucket, which does not die when the hub does, and superviseReportedPump only
// restarts the pump when the WATCHER fails. So a value that changed during an
// outage, failed its hub write, and never changed again was absent from the hub
// permanently and silently.
//
// Note the existing partition test covers convergence via a relay RESTART,
// which is exactly the path that always worked. Nothing exercised a live relay
// against a failing destination.
func TestRelayRetriesAFailedWriteUntilTheHubAcceptsIt(t *testing.T) {
	withShortRetry(t, 20*time.Millisecond)

	local := newFakeTwinKV(map[string][]byte{"thing.S01.setpoint": []byte("21")})
	hub := newFakeTwinKV(nil)
	hub.setWriteErr(errHubDown)

	// Recover ONLY after the hub has actually rejected a write. Clearing the
	// error on a timer instead lets the very first attempt succeed, and the test
	// then passes without the retry path ever running -- which it did, against a
	// deliberately broken build, before this was made to wait on a real failure.
	runRelayUntil(t, local, hub, 2*time.Second, func() bool {
		if hub.writeFailCount() > 0 {
			hub.setWriteErr(nil)
		}
		v, ok := hub.get("thing.S01.setpoint")
		return ok && v == "21"
	})

	if hub.writeFailCount() == 0 {
		t.Fatal("the hub never rejected a write, so the retry path was not exercised")
	}

	if v, ok := hub.get("thing.S01.setpoint"); !ok || v != "21" {
		t.Errorf("hub has %q (present=%v), want 21 -- the failed write was never retried", v, ok)
	}
}

// A retry must send the value the device reports NOW, not the one that failed.
//
// The device keeps reporting while the hub is unreachable, so remembering the
// failed value and replaying it later would write a stale reading over a newer
// one. retryPending re-reads from the local bucket for exactly this reason.
func TestRelayRetrySendsTheCurrentValueNotTheFailedOne(t *testing.T) {
	local := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("21")})
	hub := newFakeTwinKV(nil)
	hub.setWriteErr(errHubDown)

	// The first attempt fails and the key is held.
	if _, err := relayEntry(context.Background(), hub, "thing.S01.temp", []byte("21"), jetstream.KeyValuePut); err == nil {
		t.Fatal("expected the relay to fail while the hub is down")
	}
	pending := map[string]struct{}{"thing.S01.temp": {}}

	// The device reports again while the hub is still down.
	local.store["thing.S01.temp"] = []byte("25")

	// Hub recovers; the retry should carry 25.
	hub.setWriteErr(nil)
	retryPending(context.Background(), local, hub, pending)

	if v, ok := hub.get("thing.S01.temp"); !ok || v != "25" {
		t.Errorf("hub has %q (present=%v), want 25 -- a retry replayed the stale failed value", v, ok)
	}
	if len(pending) != 0 {
		t.Errorf("%d key(s) still pending after a successful retry", len(pending))
	}
}

// A key deleted locally during the outage must be relayed as a delete, not
// resurrected. A KV delete is a tombstone, not an absence: dropping it would
// leave the key gone at the edge and live at the hub forever.
func TestRelayRetryRelaysADeleteForAKeyRemovedDuringTheOutage(t *testing.T) {
	local := newFakeTwinKV(nil) // already gone locally
	hub := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("21")})

	pending := map[string]struct{}{"thing.S01.temp": {}}
	retryPending(context.Background(), local, hub, pending)

	if _, ok := hub.get("thing.S01.temp"); ok {
		t.Error("hub still holds a key that was deleted at the edge during the outage")
	}
	if len(pending) != 0 {
		t.Errorf("%d key(s) still pending after the delete was relayed", len(pending))
	}
}

// A key the hub will never accept must not block every other key behind it.
// This is why the pump holds failures and keeps going, rather than returning an
// error and letting the supervisor replay: a permanent per-key rejection would
// otherwise tear down the watcher on every replay, forever.
func TestRelayRetryKeepsAPoisonKeyFromBlockingOthers(t *testing.T) {
	local := newFakeTwinKV(nil)
	hub := newFakeTwinKV(nil)

	// One key the hub refuses, one it accepts. poisonHub fails only the former.
	hub.setWriteErr(errHubDown)
	pending := map[string]struct{}{"thing.S01.bad": {}}
	local.store["thing.S01.bad"] = []byte("nope")
	retryPending(context.Background(), local, hub, pending)
	if len(pending) != 1 {
		t.Fatalf("expected the failing key to stay pending, got %d", len(pending))
	}

	// A healthy key still relays while the other is stuck.
	hub.setWriteErr(nil)
	local.store["thing.S02.temp"] = []byte("19")
	if _, err := relayEntry(context.Background(), hub, "thing.S02.temp", []byte("19"), jetstream.KeyValuePut); err != nil {
		t.Fatalf("a healthy key failed to relay while another was pending: %v", err)
	}
	if v, ok := hub.get("thing.S02.temp"); !ok || v != "19" {
		t.Errorf("hub has %q (present=%v) for the healthy key, want 19", v, ok)
	}
}
