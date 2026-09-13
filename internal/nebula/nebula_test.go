package nebula

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// NOTE ON COVERAGE. Everything below exercises the paths that do not need a TUN
// device, which means every failure and no-op path but not a successful start.
// Bringing Nebula up needs a real network device and root, so the apply ladder —
// reload, restart, roll back — is verified on a host, not here. What is tested is
// the part most likely to be got wrong by accident: the state machine around
// those calls.

// fakeSource is a Source whose answers the test controls.
type fakeSource struct {
	fetched  Fetched
	err      error
	calls    int
	lastSeen string // the knownRevision the manager passed in
}

func (s *fakeSource) Name() string { return "fake" }

func (s *fakeSource) Fetch(knownRevision string) (Fetched, error) {
	s.calls++
	s.lastSeen = knownRevision
	return s.fetched, s.err
}

func testManager(t *testing.T, src Source) *Manager {
	t.Helper()
	return New(Options{
		Source:        src,
		CacheFile:     filepath.Join(t.TempDir(), "nebula-cache.yaml"),
		VerifyTimeout: 5 * time.Second,
		Version:       "test",
		Logger:        zap.NewNop(),
	})
}

func TestFileSourceReadsConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nebula.yaml")
	if err := os.WriteFile(path, []byte("pki:\n  ca: x\n"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := FileSource{Path: path}.Fetch("any-revision")
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}

	// A file source has no revision and reports every read as changed. That is its
	// whole contract: it does not poll and cannot answer "unchanged".
	if !got.Changed {
		t.Error("Changed = false, want true")
	}
	if got.Revision != "" {
		t.Errorf("Revision = %q, want empty", got.Revision)
	}
	if got.YAML != "pki:\n  ca: x\n" {
		t.Errorf("YAML = %q, want the file contents", got.YAML)
	}
}

func TestFileSourceMissingFile(t *testing.T) {
	_, err := FileSource{Path: filepath.Join(t.TempDir(), "absent.yaml")}.Fetch("")
	if err == nil {
		t.Fatal("Fetch() returned no error for a missing file")
	}
}

// TestSyncUnchangedDoesNotApply is the routine path, and the one that runs most
// often: the source says nothing moved, so nothing is touched.
func TestSyncUnchangedDoesNotApply(t *testing.T) {
	src := &fakeSource{fetched: Fetched{Revision: "rev-1", Changed: false}}
	m := testManager(t, src)

	changed, err := m.Sync()
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if changed {
		t.Error("Sync() reported a change for an unchanged config")
	}

	h := m.Health()
	if h.Running {
		t.Error("Running = true after a no-op sync")
	}
	if h.Error != "" {
		t.Errorf("Error = %q, want empty after a successful no-op sync", h.Error)
	}
	if h.LastSync == "" {
		t.Error("LastSync is empty, want the time of the sync that just ran")
	}
}

// TestSyncPassesKnownRevision guards the probe that keeps key material off the
// wire: the manager has to tell the source what it already has, or the source
// cannot answer "unchanged" and will send the private key every cycle.
func TestSyncPassesKnownRevision(t *testing.T) {
	src := &fakeSource{fetched: Fetched{Changed: false}}
	m := testManager(t, src)
	m.revision = "rev-7"

	if _, err := m.Sync(); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	if src.lastSeen != "rev-7" {
		t.Errorf("source saw knownRevision %q, want %q", src.lastSeen, "rev-7")
	}
}

func TestSyncReportsSourceError(t *testing.T) {
	src := &fakeSource{err: errors.New("platform unreachable")}
	m := testManager(t, src)

	changed, err := m.Sync()
	if err == nil {
		t.Fatal("Sync() returned no error when the source failed")
	}
	if changed {
		t.Error("Sync() reported a change when the source failed")
	}

	if got := m.Health().Error; got == "" {
		t.Error("Health().Error is empty after a failed sync")
	}
}

// TestSyncRejectsUnparseableConfig covers the first rung of the apply ladder: a
// config that cannot even be parsed never reaches Nebula, and with nothing
// previously running there is nothing to roll back to.
func TestSyncRejectsUnparseableConfig(t *testing.T) {
	src := &fakeSource{fetched: Fetched{
		YAML:     "pki: [this is not: valid yaml",
		Revision: "rev-bad",
		Changed:  true,
	}}
	m := testManager(t, src)

	changed, err := m.Sync()
	if err == nil {
		t.Fatal("Sync() accepted an unparseable config")
	}
	if changed {
		t.Error("Sync() reported a change for a config it could not apply")
	}

	h := m.Health()
	if h.Running {
		t.Error("Running = true after a config that never started")
	}
	if h.ConfigRevision != "" {
		t.Errorf("ConfigRevision = %q, want empty: a config that failed to apply must not be reported as adopted", h.ConfigRevision)
	}

	// Nothing was adopted, so nothing should have been cached for the next boot.
	if _, err := os.Stat(m.cacheFile); !errors.Is(err, os.ErrNotExist) {
		t.Error("a config that failed to apply was written to the cache")
	}
}

func TestRestartWhenNotRunning(t *testing.T) {
	m := testManager(t, &fakeSource{})

	if err := m.Restart(); err == nil {
		t.Fatal("Restart() returned no error when Nebula was not running")
	}
}

func TestStopIsSafeWhenNeverStarted(t *testing.T) {
	testManager(t, &fakeSource{}).Stop()
}

func TestHealthReportsSourceAndEnabled(t *testing.T) {
	h := testManager(t, &fakeSource{}).Health()

	// The Manager only exists when the feature is on, so Enabled is constant —
	// the field is there so a reader does not have to infer it from an absent
	// object.
	if !h.Enabled {
		t.Error("Enabled = false, want true: a Manager only exists when the feature is on")
	}
	if h.Source != "fake" {
		t.Errorf("Source = %q, want %q", h.Source, "fake")
	}
	if h.Running {
		t.Error("Running = true before anything started")
	}
}

func TestWriteFileAtomicCreatesParentAndRoundTrips(t *testing.T) {
	// A nested path the installer never created: the cache lives under a state
	// directory that may not exist on a first run.
	path := filepath.Join(t.TempDir(), "state", "nebula-cache.yaml")

	if err := writeFileAtomic(path, []byte("config")); err != nil {
		t.Fatalf("writeFileAtomic() error: %v", err)
	}

	m := New(Options{CacheFile: path, Logger: zap.NewNop()})
	got, err := m.readCache()
	if err != nil {
		t.Fatalf("readCache() error: %v", err)
	}
	if got != "config" {
		t.Errorf("readCache() = %q, want %q", got, "config")
	}

	// No temp files left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want 1: a temp file was left behind", len(entries))
	}
}

func TestReadCacheWithoutCacheFile(t *testing.T) {
	m := New(Options{Logger: zap.NewNop()})
	if _, err := m.readCache(); err == nil {
		t.Fatal("readCache() returned no error when no cache file is configured")
	}
}
