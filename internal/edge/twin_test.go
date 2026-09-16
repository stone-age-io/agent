package edge

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// --- fakes -------------------------------------------------------------------

// fakeEntry is a jetstream.KeyValueEntry carrying only what the relay reads.
type fakeEntry struct {
	key   string
	value []byte
	op    jetstream.KeyValueOp
	rev   uint64
}

func (e *fakeEntry) Bucket() string                  { return twinBucket }
func (e *fakeEntry) Key() string                     { return e.key }
func (e *fakeEntry) Value() []byte                   { return e.value }
func (e *fakeEntry) Revision() uint64                { return e.rev }
func (e *fakeEntry) Created() time.Time              { return time.Time{} }
func (e *fakeEntry) Delta() uint64                   { return 0 }
func (e *fakeEntry) Operation() jetstream.KeyValueOp { return e.op }

// fakeWatcher is a hand-fed jetstream.KeyWatcher.
type fakeWatcher struct {
	ch chan jetstream.KeyValueEntry
}

func (w *fakeWatcher) Updates() <-chan jetstream.KeyValueEntry { return w.ch }
func (w *fakeWatcher) Stop() error                             { return nil }

// fakeTwinKV is an in-memory twinSide. Every Put and Delete is also published to
// its watchers, so wiring two of them through pumpReported reproduces the real
// feedback path — which is the only way a ping-pong bug shows up in a test.
type fakeTwinKV struct {
	mu       sync.Mutex
	store    map[string][]byte
	watchers []*fakeWatcher
	rev      uint64

	puts    []string
	deletes []string
	getErr  error

	// writeErr makes Put and Delete fail, modelling an unreachable hub.
	// writeFails counts attempts it rejected, so a test can wait for a real
	// failure instead of guessing at timing.
	writeErr   error
	writeFails int
}

func newFakeTwinKV(seed map[string][]byte) *fakeTwinKV {
	if seed == nil {
		seed = map[string][]byte{}
	}
	return &fakeTwinKV{store: seed}
}

func (f *fakeTwinKV) Get(_ context.Context, key string) (jetstream.KeyValueEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	v, ok := f.store[key]
	if !ok {
		return nil, jetstream.ErrKeyNotFound
	}
	return &fakeEntry{key: key, value: v, op: jetstream.KeyValuePut}, nil
}

func (f *fakeTwinKV) Put(_ context.Context, key string, value []byte) (uint64, error) {
	f.mu.Lock()
	if f.writeErr != nil {
		err := f.writeErr
		f.writeFails++
		f.mu.Unlock()
		return 0, err
	}
	f.puts = append(f.puts, key)
	f.store[key] = value
	f.rev++
	rev := f.rev
	f.mu.Unlock()
	f.emit(&fakeEntry{key: key, value: value, op: jetstream.KeyValuePut, rev: rev})
	return rev, nil
}

func (f *fakeTwinKV) Delete(_ context.Context, key string, _ ...jetstream.KVDeleteOpt) error {
	f.mu.Lock()
	if f.writeErr != nil {
		err := f.writeErr
		f.writeFails++
		f.mu.Unlock()
		return err
	}
	f.deletes = append(f.deletes, key)
	delete(f.store, key)
	f.rev++
	rev := f.rev
	f.mu.Unlock()
	f.emit(&fakeEntry{key: key, op: jetstream.KeyValueDelete, rev: rev})
	return nil
}

func (f *fakeTwinKV) WatchAll(_ context.Context, _ ...jetstream.WatchOpt) (jetstream.KeyWatcher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &fakeWatcher{ch: make(chan jetstream.KeyValueEntry, 256)}
	// Replay current values, then the nil end-of-replay marker, exactly as a
	// real WatchAll does.
	keys := make([]string, 0, len(f.store))
	for k := range f.store {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w.ch <- &fakeEntry{key: k, value: f.store[k], op: jetstream.KeyValuePut}
	}
	w.ch <- nil
	f.watchers = append(f.watchers, w)
	return w, nil
}

// emit delivers a change to every live watcher, like the server would.
func (f *fakeTwinKV) emit(e jetstream.KeyValueEntry) {
	f.mu.Lock()
	ws := append([]*fakeWatcher(nil), f.watchers...)
	f.mu.Unlock()
	for _, w := range ws {
		select {
		case w.ch <- e:
		default: // full buffer: drop rather than deadlock the test
		}
	}
}

func (f *fakeTwinKV) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.store))
	for k := range f.store {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (f *fakeTwinKV) get(key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.store[key]
	return string(v), ok
}

func (f *fakeTwinKV) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.puts)
}

// runRelay starts the reported pump (edge -> hub) and lets it settle.
func runRelay(t *testing.T, local, hub *fakeTwinKV, settle time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = pumpReported(ctx, local, hub) }()
	time.Sleep(settle)
	cancel()
	wg.Wait()
}

// --- relayEntry --------------------------------------------------------------

// The equality short-circuit: WatchAll replays every value on start, so a relay
// that wrote unconditionally would rewrite the whole bucket on every restart and
// burn a revision per key.
func TestRelayEntrySkipsWhenDestinationAgrees(t *testing.T) {
	dst := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("21")})

	wrote, err := relayEntry(context.Background(), dst, "thing.S01.temp", []byte("21"), jetstream.KeyValuePut)
	if err != nil {
		t.Fatalf("relayEntry: %v", err)
	}
	if wrote {
		t.Error("relayEntry wrote an identical value; the equality check did not fire")
	}
	if n := dst.putCount(); n != 0 {
		t.Errorf("dst saw %d puts, want 0", n)
	}
}

func TestRelayEntryWritesWhenDestinationDiffersOrIsAbsent(t *testing.T) {
	dst := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("21")})
	ctx := context.Background()

	if wrote, err := relayEntry(ctx, dst, "thing.S01.temp", []byte("22"), jetstream.KeyValuePut); err != nil || !wrote {
		t.Fatalf("changed value: wrote=%v err=%v, want wrote=true", wrote, err)
	}
	if v, _ := dst.get("thing.S01.temp"); v != "22" {
		t.Errorf("dst holds %q, want 22", v)
	}

	if wrote, err := relayEntry(ctx, dst, "thing.S02.temp", []byte("30"), jetstream.KeyValuePut); err != nil || !wrote {
		t.Fatalf("absent key: wrote=%v err=%v, want wrote=true", wrote, err)
	}
}

// A KV delete is a tombstone, not an absence. If the relay treats DEL as
// "nothing to send", the key stays live on the far side forever — the equality
// check can never catch it up, because it only compares values that exist.
func TestRelayEntryPropagatesDelete(t *testing.T) {
	dst := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("21")})

	wrote, err := relayEntry(context.Background(), dst, "thing.S01.temp", nil, jetstream.KeyValueDelete)
	if err != nil {
		t.Fatalf("relayEntry: %v", err)
	}
	if !wrote {
		t.Fatal("delete was not relayed")
	}
	if _, ok := dst.get("thing.S01.temp"); ok {
		t.Error("key survived a relayed delete")
	}
}

func TestRelayEntryDeleteOfAbsentKeyIsNoOp(t *testing.T) {
	dst := newFakeTwinKV(nil)

	wrote, err := relayEntry(context.Background(), dst, "thing.S01.temp", nil, jetstream.KeyValueDelete)
	if err != nil {
		t.Fatalf("relayEntry: %v", err)
	}
	if wrote {
		t.Error("deleting an already-absent key issued a write")
	}
	if len(dst.deletes) != 0 {
		t.Errorf("dst saw %d deletes, want 0", len(dst.deletes))
	}
}

func TestRelayEntrySurfacesGetFailure(t *testing.T) {
	dst := newFakeTwinKV(nil)
	dst.getErr = errors.New("bucket unreachable")

	if _, err := relayEntry(context.Background(), dst, "thing.S01.temp", []byte("1"), jetstream.KeyValuePut); err == nil {
		t.Fatal("expected an unreadable destination to be surfaced, got nil")
	}
}

// --- the reported pump ---------------------------------------------------------

// The headline property: an edge write lands at the hub exactly once and does not
// bounce back. The fakes republish their own writes to their watchers, so an
// echo would show up here as runaway puts.
func TestRelayNoEchoLoop(t *testing.T) {
	local := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("21")})
	hub := newFakeTwinKV(nil)

	runRelay(t, local, hub, 200*time.Millisecond)

	if v, ok := hub.get("thing.S01.temp"); !ok || v != "21" {
		t.Fatalf("hub holds %q (present=%v), want 21", v, ok)
	}
	if n := hub.putCount(); n != 1 {
		t.Errorf("hub saw %d puts, want exactly 1 — the relay is echoing", n)
	}
	if n := local.putCount(); n != 0 {
		t.Errorf("local saw %d puts, want 0 — the value bounced back", n)
	}
}

// `twin` has one writer: the edge. A hub-side value for the same key — however it
// got there — loses, and does so in a bounded number of writes.
//
// This is the case that makes a two-writer bucket oscillate forever: with both
// ends relaying, two concurrent values swap across the link and swap back, each
// write generating the next event (measured at ~170k writes to a single key in
// 300ms before the buckets were split). Sole ownership makes it unrepresentable
// rather than merely unlikely — there is no second pump to swap it back.
func TestRelayEdgeValueWinsWithoutOscillating(t *testing.T) {
	local := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("edge")})
	hub := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("stale")})

	runRelay(t, local, hub, 300*time.Millisecond)

	lv, _ := local.get("thing.S01.temp")
	hv, _ := hub.get("thing.S01.temp")
	if lv != "edge" || hv != "edge" {
		t.Errorf("converged to local=%q hub=%q, want both 'edge' (the owning side)", lv, hv)
	}
	if n := hub.putCount(); n > 1 {
		t.Errorf("hub saw %d puts for one key, want 1 — that is an oscillation, not a relay", n)
	}
	if n := local.putCount(); n != 0 {
		t.Errorf("local saw %d puts, want 0 — the edge bucket must never be written by the relay", n)
	}
}

// Direction is the bucket now, not a segment of the key, so the relay carries any
// key shape. This is the footgun the split removed: under the old convention a
// key without a recognised owner segment silently never synced.
func TestRelayCarriesAnyKeyShape(t *testing.T) {
	local := newFakeTwinKV(map[string][]byte{
		"thing.S01.temp":                  []byte("21"),
		"location.LOC_01.occupancy":       []byte("4"),
		"flat":                            []byte("x"),
		"deeply.nested.key.with.segments": []byte("y"),
	})
	hub := newFakeTwinKV(nil)

	runRelay(t, local, hub, 300*time.Millisecond)

	want := []string{"deeply.nested.key.with.segments", "flat", "location.LOC_01.occupancy", "thing.S01.temp"}
	if got := hub.keys(); !reflect.DeepEqual(got, want) {
		t.Errorf("hub keys = %v, want %v — every key shape must relay", got, want)
	}
}

// A restart replays every current value. With both sides already in agreement
// that must produce no writes at all, or every restart burns a revision per key
// and eventually trims the history the UI reads.
func TestRelayRestartIsIdempotent(t *testing.T) {
	local := newFakeTwinKV(map[string][]byte{
		"thing.S01.temp":     []byte("21"),
		"thing.S01.humidity": []byte("48"),
	})
	hub := newFakeTwinKV(map[string][]byte{
		"thing.S01.temp":     []byte("21"),
		"thing.S01.humidity": []byte("48"),
	})

	runRelay(t, local, hub, 200*time.Millisecond)

	if n := local.putCount() + hub.putCount(); n != 0 {
		t.Errorf("a restart with both sides in agreement issued %d puts, want 0", n)
	}
}

// Edge writes made while the relay was down reach the hub on restart, via the
// WatchAll replay. That covers a relay RESTART; a relay that stays up while the
// HUB is down is a different path, and is covered by the pending-set tests in
// twin_retry_test.go. Hub-side keys the edge has never heard of are left alone: the relay is
// an upsert of what this site knows, not a reconcile of the whole bucket, so one
// site cannot purge another's state.
func TestRelayConvergesAfterPartitionWithoutPurgingOtherSites(t *testing.T) {
	local := newFakeTwinKV(map[string][]byte{
		"thing.S01.temp": []byte("21"),
		"thing.S02.temp": []byte("19"),
	})
	hub := newFakeTwinKV(map[string][]byte{
		"thing.OTHERSITE.temp": []byte("30"),
	})

	runRelay(t, local, hub, 300*time.Millisecond)

	want := []string{"thing.OTHERSITE.temp", "thing.S01.temp", "thing.S02.temp"}
	if got := hub.keys(); !reflect.DeepEqual(got, want) {
		t.Errorf("hub keys = %v, want %v", got, want)
	}
	if got := local.keys(); !reflect.DeepEqual(got, []string{"thing.S01.temp", "thing.S02.temp"}) {
		t.Errorf("local keys = %v, want only this site's own", got)
	}
}

// A delete made at the edge has to reach the hub, or a decommissioned key lives
// on in the console forever.
func TestRelayDeletePropagatesToHub(t *testing.T) {
	local := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("21")})
	hub := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("21")})

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = pumpReported(ctx, local, hub) }()

	time.Sleep(100 * time.Millisecond) // let the initial replay settle
	if err := local.Delete(ctx, "thing.S01.temp"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	wg.Wait()

	if _, ok := hub.get("thing.S01.temp"); ok {
		t.Error("hub still holds a key deleted at the edge")
	}
}
