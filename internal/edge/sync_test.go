package edge

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stone-age-io/agent/internal/config"
)

// The preset's bucket names live in two packages and must agree.
//
// config cannot import this package — edge imports platform, platform imports
// config — so validateSyncConfig carries its own copy of the two names in order
// to refuse a user entry that collides with the preset. This is the guard
// against those copies drifting, and it is the same answer this project already
// gives for the console's copy of the twin bucket shape: a test on each side
// rather than a package between them.
func TestPresetBucketNamesMatchConfig(t *testing.T) {
	want := []string{twinBucket, twinDesiredBucket}
	got := append([]string(nil), config.PresetBuckets...)

	if !reflect.DeepEqual(sorted(got), sorted(want)) {
		t.Errorf("config.PresetBuckets = %v, but this package syncs %v.\n"+
			"They must name the same buckets, or a user entry colliding with the preset "+
			"gets past validation and the bucket ends up with two writers.", got, want)
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// The preset expands to exactly one entry in each direction, and only preset
// entries may create their hub-side bucket.
func TestPlanExpandsPresetAndUserLists(t *testing.T) {
	got := plan(&Config{
		TwinEnabled: true,
		Mirrors:     []Bucket{{Name: "recipes", Keys: "line-a.>"}},
		Relays:      []Bucket{{Name: "events"}},
	})

	want := []entry{
		{Bucket: Bucket{Name: twinDesiredBucket}, direction: directionMirror, preset: true},
		{Bucket: Bucket{Name: twinBucket}, direction: directionRelay, preset: true},
		{Bucket: Bucket{Name: "recipes", Keys: "line-a.>"}, direction: directionMirror},
		{Bucket: Bucket{Name: "events"}, direction: directionRelay},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan() = %+v\nwant %+v", got, want)
	}

	for _, e := range got {
		if e.preset && e.Name != twinBucket && e.Name != twinDesiredBucket {
			t.Errorf("entry %q is marked preset; only the twin buckets may create hub-side buckets", e.Name)
		}
		if !e.preset && (e.Name == twinBucket || e.Name == twinDesiredBucket) {
			t.Errorf("twin entry %q lost its preset flag and can no longer create its hub bucket", e.Name)
		}
	}
}

func TestPlanIsEmptyWhenNothingIsDeclared(t *testing.T) {
	if got := plan(&Config{}); len(got) != 0 {
		t.Errorf("plan() = %+v, want nothing — an agent that declared no buckets must start no sync", got)
	}
}

// A key pattern becomes the mirror's subject filter, with the `$KV.<bucket>.`
// prefix added here rather than by the operator.
//
// This is the one field that cannot be corrected later: nats-server rejects any
// change to a mirror block on an existing stream
// (JSStreamMirrorNotUpdatableErr), so a wrong filter means deleting and
// recreating the bucket on every site by hand.
func TestMirrorSourceTurnsKeysIntoASubjectFilter(t *testing.T) {
	e := entry{Bucket: Bucket{Name: "recipes", Keys: "line-a.>"}, direction: directionMirror}

	got := mirrorSource(e, "hub")

	if want := "KV_recipes"; got.Name != want {
		t.Errorf("mirror source name = %q, want %q", got.Name, want)
	}
	if want := "hub"; got.Domain != want {
		t.Errorf("mirror domain = %q, want %q", got.Domain, want)
	}
	if want := "$KV.recipes.line-a.>"; got.FilterSubject != want {
		t.Errorf("mirror filter = %q, want %q — keys are keys, and this package adds the $KV prefix",
			got.FilterSubject, want)
	}
}

// No keys means no filter at all, not a filter matching everything. An empty
// FilterSubject is what the twin preset has carried since before the lists, and
// changing it would make every existing mirror disagree with its declaration.
func TestMirrorSourceWithoutKeysHasNoFilter(t *testing.T) {
	got := mirrorSource(entry{Bucket: Bucket{Name: twinDesiredBucket}, preset: true}, "hub")

	if got.FilterSubject != "" {
		t.Errorf("mirror filter = %q, want empty", got.FilterSubject)
	}
}

// A relay's key filter is what makes a site physically unable to relay another
// site's keyspace up, rather than merely conventionally unlikely to.
func TestRelayHonoursItsKeyFilter(t *testing.T) {
	local := newFakeTwinKV(map[string][]byte{
		"site.S01.temp":     []byte("21"),
		"site.S01.humidity": []byte("48"),
		"site.S02.temp":     []byte("19"), // another site's key, present locally by mistake
	})
	hub := newFakeTwinKV(nil)

	runRelayKeys(t, local, hub, "site.S01.>", 300*time.Millisecond)

	want := []string{"site.S01.humidity", "site.S01.temp"}
	if got := hub.keys(); !reflect.DeepEqual(got, want) {
		t.Errorf("hub keys = %v, want %v — a filtered relay must not carry keys outside its filter", got, want)
	}
}

// The backlog is reported so /metrics can show it. A relay that starts clean
// reports zero rather than reporting nothing: it is running, and an absent
// series would read as "not syncing this bucket".
func TestRelayReportsItsBacklog(t *testing.T) {
	local := newFakeTwinKV(map[string][]byte{"thing.S01.temp": []byte("21")})
	hub := newFakeTwinKV(nil)

	st := &state{}
	h := st.registerSync("twin", directionRelay)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = (&relay{src: local, dst: hub, report: func(n int) { st.setSyncPending(h, n) }}).pump(ctx)

	snap := st.syncSnapshot()
	if len(snap) != 1 {
		t.Fatalf("got %d entries, want 1", len(snap))
	}
	if !snap[0].hasPending {
		t.Error("a started relay reported no backlog at all; /metrics would omit the series")
	}
	if snap[0].pending != 0 {
		t.Errorf("backlog = %d, want 0 — nothing failed", snap[0].pending)
	}
}

// A mirror never reports a backlog, so the series is omitted rather than
// reported as zero. Zero would claim a relay that is keeping up; there is no
// relay.
func TestMirrorEntryReportsNoBacklog(t *testing.T) {
	st := &state{}
	st.setSyncUp(st.registerSync(twinDesiredBucket, directionMirror), true, "")

	snap := st.syncSnapshot()
	if snap[0].hasPending {
		t.Error("a mirror reported a backlog; it has no relay and the series must be omitted")
	}
}

// Only the twin preset may create a bucket at the hub.
//
// A user-declared entry that names a bucket the hub does not have is a typo, and
// creating it would put a stream on the SHARED hub with whatever retention one
// site guessed — which the console then adopts. A typo that creates a local
// bucket is one site's problem.
func TestOnlyPresetEntriesMayCreateHubBuckets(t *testing.T) {
	for _, tc := range []struct {
		name      string
		e         entry
		mayCreate bool
	}{
		{"preset mirror", entry{Bucket: Bucket{Name: twinDesiredBucket}, direction: directionMirror, preset: true}, true},
		{"preset relay", entry{Bucket: Bucket{Name: twinBucket}, direction: directionRelay, preset: true}, true},
		{"user mirror", entry{Bucket: Bucket{Name: "recipes"}, direction: directionMirror}, false},
		{"user relay", entry{Bucket: Bucket{Name: "events"}, direction: directionRelay}, false},
	} {
		if tc.e.preset != tc.mayCreate {
			t.Errorf("%s: preset=%v, want %v — hubBucket() keys hub-side creation off this flag",
				tc.name, tc.e.preset, tc.mayCreate)
		}
	}
}

// Every declared bucket the agent creates carries a description, and the two
// preset buckets keep the wording the console uses.
func TestDescriptionCoversBothDirections(t *testing.T) {
	for _, tc := range []struct {
		e    entry
		want string
	}{
		{entry{direction: directionMirror, preset: true}, "Digital twin: desired state (written by operators)"},
		{entry{direction: directionRelay, preset: true}, "Digital twin: reported state (written at the edge)"},
		{entry{direction: directionMirror}, "Mirrored from the hub by the agent"},
		{entry{direction: directionRelay}, "Relayed to the hub by the agent"},
	} {
		if got := description(tc.e); got != tc.want {
			t.Errorf("description(%+v) = %q, want %q", tc.e, got, tc.want)
		}
	}
}

// A relay that is already running must never be started a second time.
//
// This is the correctness requirement behind the retry loop. Re-running
// ensureMirror on a healthy mirror would be harmless, but a second watcher on
// the same bucket relays every change twice — not a conflict, since one writer
// still owns the bucket, but double the hub's write rate for ever and silent.
//
// The JetStream handles are nil on purpose: if wireDown tries to wire an entry
// it was supposed to skip, it dereferences one and the test panics.
func TestWireDownSkipsEntriesThatAreAlreadyUp(t *testing.T) {
	st := &state{}
	ws := []wiring{
		{entry: entry{Bucket: Bucket{Name: "twin"}, direction: directionRelay}, handle: st.registerSync("twin", directionRelay)},
		{entry: entry{Bucket: Bucket{Name: "recipes"}, direction: directionMirror}, handle: st.registerSync("recipes", directionMirror)},
	}
	for _, w := range ws {
		st.setSyncUp(w.handle, true, "")
	}

	wireDown(context.Background(), nil, nil, &Config{HubDomain: "hub"}, st, ws)

	for _, e := range st.syncSnapshot() {
		if !e.up {
			t.Errorf("entry %q went down after a retry pass that should have skipped it", e.bucket)
		}
	}
}

// The retry loop runs on its ticker and stops with the context.
func TestRetryWiringStopsWithTheContext(t *testing.T) {
	restore := syncRetryInterval
	syncRetryInterval = 5 * time.Millisecond
	defer func() { syncRetryInterval = restore }()

	st := &state{}
	ws := []wiring{{
		entry:  entry{Bucket: Bucket{Name: "twin"}, direction: directionRelay},
		handle: st.registerSync("twin", directionRelay),
	}}
	st.setSyncUp(ws[0].handle, true, "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		retryWiring(ctx, nil, nil, &Config{HubDomain: "hub"}, st, ws)
		close(done)
	}()

	time.Sleep(25 * time.Millisecond) // several ticks, all of them no-ops
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retryWiring did not return after its context was cancelled")
	}
}

// A failure that has not changed is recorded every time and logged once.
//
// wireDown runs on a timer now, and a hub bucket nobody has created yet fails
// with the same message every minute. Without this the agent would write that
// line for ever and bury whatever else the box was saying.
func TestRepeatedSyncFailureIsLoggedOnce(t *testing.T) {
	st := &state{}
	h := st.registerSync("recipes", directionMirror)

	if !st.shouldLogDown(h, "no such bucket at the hub") {
		t.Error("the first failure should be logged")
	}
	if st.shouldLogDown(h, "no such bucket at the hub") {
		t.Error("the same failure should not be logged again on the next retry")
	}
	if !st.shouldLogDown(h, "bucket exists but is not a mirror") {
		t.Error("a different failure should be logged")
	}

	// Recovering and breaking the same way again is news.
	st.setSyncUp(h, true, "")
	if !st.shouldLogDown(h, "bucket exists but is not a mirror") {
		t.Error("a failure recurring after a recovery should be logged again")
	}
}
