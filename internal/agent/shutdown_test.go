package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"go.uber.org/zap"

	"github.com/stone-age-io/agent/internal/config"
	natsclient "github.com/stone-age-io/agent/internal/nats"
	"github.com/stone-age-io/agent/internal/scheduler"
	"github.com/stone-age-io/agent/internal/tasks"
)

// Every stop calls Shutdown twice, at the same instant, and the second call
// must not be the one that reports an error.
//
// Under systemd, SIGTERM reaches both Run's signal.Notify and the service
// library's, so Run and program.Stop each call Shutdown. gocron's Shutdown
// gives its one result to whichever caller receives it first; the other
// waited out StopTimeout + 2s (12s) and logged "gocron: timed out waiting for
// scheduler to stop" about a scheduler that had stopped cleanly.
func TestConcurrentShutdownIsQuick(t *testing.T) {
	a := newShutdownAgent(t)
	a.scheduler.Start()

	const callers = 2
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Go(func() {
			<-start
			errs[i] = a.Shutdown()
		})
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	close(start)

	// Far below gocron's 12s, which is what the losing caller used to wait.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a concurrent Shutdown is still waiting: only one caller can receive gocron's stop result, " +
			"so the agent must shut down once and have every caller wait for that")
	}

	for i, err := range errs {
		if err != nil {
			t.Errorf("Shutdown call %d returned %v", i, err)
		}
	}
}

// A later call, after the first has finished, returns at once as well.
func TestShutdownTwiceInSequence(t *testing.T) {
	a := newShutdownAgent(t)
	a.scheduler.Start()

	if err := a.Shutdown(); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}

	began := time.Now()
	if err := a.Shutdown(); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
	if waited := time.Since(began); waited > time.Second {
		t.Errorf("second Shutdown took %v; it should return what the first one did", waited)
	}
}

// newShutdownAgent builds the parts of an Agent that Shutdown touches: a
// started-able scheduler with one job, and a NATS client connected to a real
// in-process server, so Drain does what it does in production.
func newShutdownAgent(t *testing.T) *Agent {
	t.Helper()

	srv, err := server.NewServer(&server.Options{
		Host:   "127.0.0.1",
		Port:   -1,
		NoLog:  true,
		NoSigs: true,
	})
	if err != nil {
		t.Fatalf("failed to create nats-server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats-server did not start")
	}
	t.Cleanup(srv.Shutdown)

	cfg := &config.Config{
		Code:          "srv-01",
		SubjectPrefix: "agents",
		NATS: config.NATSConfig{
			URLs:          []string{srv.ClientURL()},
			Auth:          config.AuthConfig{Type: "none"},
			MaxReconnects: -1,
			ReconnectWait: time.Second,
			DrainTimeout:  2 * time.Second,
		},
	}
	// One job, so the scheduler has an executor to stop. Long enough never to
	// fire during the test.
	cfg.Tasks.Heartbeat.Enabled = true
	cfg.Tasks.Heartbeat.Interval = time.Hour

	logger := zap.NewNop()

	client, err := natsclient.NewClient(&cfg.NATS, "agent-"+cfg.Code, logger)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	executor := tasks.NewExecutor(logger, 5*time.Second, ctx)
	sched, err := scheduler.New(logger, client, executor, cfg, "dev", nil, nil, nil, ctx)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}

	return &Agent{
		config:    cfg,
		logger:    logger,
		nats:      client,
		scheduler: sched,
		version:   "dev",
		ctx:       ctx,
		cancel:    cancel,
	}
}
