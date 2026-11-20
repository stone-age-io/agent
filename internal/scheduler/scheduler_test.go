package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/stone-age-io/agent/internal/config"
)

// slowSyncer is a NebulaSyncer whose Sync outlasts the job's interval, and
// which records the most calls it ever saw in flight at once.
type slowSyncer struct {
	hold time.Duration

	mu      sync.Mutex
	active  int
	maxSeen int
	calls   int
}

func (f *slowSyncer) Sync() (bool, error) {
	f.mu.Lock()
	f.active++
	f.calls++
	if f.active > f.maxSeen {
		f.maxSeen = f.active
	}
	f.mu.Unlock()

	time.Sleep(f.hold)

	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return false, nil
}

// A job whose run outlasts its interval must not start again until that run
// finishes. gocron's default is to start it anyway, and a Nebula sync can wait
// out verify_timeout (up to 5m) on a 1m interval; the runs queued behind the
// manager's lock and then ran back to back.
func TestJobsDoNotOverlapThemselves(t *testing.T) {
	cfg := &config.Config{Code: "srv-01", SubjectPrefix: "agents"}
	cfg.Nebula.SyncInterval = 20 * time.Millisecond

	syncer := &slowSyncer{hold: 150 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Only the Nebula sync is scheduled, so nothing touches the nil NATS
	// client or executor.
	s, err := New(zap.NewNop(), nil, nil, cfg, "dev", nil, syncer, nil, ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Start()

	// Long enough for several runs back to back, and for dozens of ticks.
	time.Sleep(600 * time.Millisecond)

	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	if syncer.calls < 2 {
		t.Fatalf("Sync ran %d times; the test needs at least 2 to mean anything", syncer.calls)
	}
	if syncer.maxSeen != 1 {
		t.Errorf("%d Sync calls were in flight at once; a job must not overlap itself", syncer.maxSeen)
	}
}

// Start and Shutdown can arrive together: Run starts the scheduler while a stop
// that came in at startup shuts it down. gocron's Start then waited for ever
// on a confirmation its loop never sent, which hung Run. Start must return
// whichever comes first.
func TestStartAndShutdownTogetherNeverHang(t *testing.T) {
	cfg := &config.Config{Code: "srv-01", SubjectPrefix: "agents"}
	cfg.Nebula.SyncInterval = time.Hour

	// gocron lost the race a few percent of the time, so enough rounds to be
	// all but certain to hit it without the fix, and still well under a second.
	for range 200 {
		ctx, cancel := context.WithCancel(context.Background())
		s, err := New(zap.NewNop(), nil, nil, cfg, "dev", nil, &slowSyncer{}, nil, ctx)
		if err != nil {
			cancel()
			t.Fatalf("New: %v", err)
		}

		started := make(chan struct{})
		go func() {
			s.Start()
			close(started)
		}()
		if err := s.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("Start is still blocked after Shutdown returned")
		}
		cancel()
	}
}
