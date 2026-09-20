package edge

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stone-age-io/agent/internal/health"
	"github.com/stone-age-io/agent/internal/natsd"
)

// Edge is this gateway's leaf-node duties: the embedded server if it hosts one,
// the connection to the local leaf, and the declared KV sync.
//
// It is built before it is run, because what it knows has to be registered with
// the agent's readiness registry and metrics before either starts serving. The
// agent owns those endpoints — an agent with no leaf still answers /ready, and
// it did not always: `observability.addr` defaults to a real address, so every
// agent in the fleet used to satisfy edgeEnabled() and start this subsystem,
// dial a second NATS connection, and register leaf checks about a leaf it did
// not have. A plain device then reported nats_local FAIL for ever while its
// actual connection was fine.
type Edge struct {
	cfg *Config
	st  *state
}

// New builds the edge without starting anything.
func New(cfg *Config) *Edge {
	return &Edge{cfg: cfg, st: &state{}}
}

// RegisterChecks adds the questions only a box with a leaf on it can answer.
// Call it before the agent starts its prober.
func (e *Edge) RegisterChecks(reg *health.Registry) {
	registerChecks(reg, e.st)
}

// Collector returns the agent_edge_* gauges, for the agent's metrics endpoint.
func (e *Edge) Collector() prometheus.Collector {
	return &collector{state: e.st}
}

// Run brings up the edge and blocks until ctx is done.
//
// It is started as a goroutine from agent.Run, like the Nebula manager, and
// stopped by the same cancel. It owns no signal handling and no ticker of its
// own: the agent already has both.
//
// The edge opens its OWN connection to the local leaf rather than reusing the
// agent's NATS client. They are genuinely different connections with different
// requirements — the agent's may point at the hub and retries on failed connect
// so it can come up before the overlay does, while this one is always local and
// reconnects forever. Sharing one would entangle the credential-rotation
// reconnect path with the twin watchers for no gain.
func (e *Edge) Run(ctx context.Context) error {
	if e.cfg.EmbeddedConfig != "" {
		srv, err := startEmbeddedNATS(e.cfg)
		if err != nil {
			return err
		}
		defer srv.Stop()
	}

	nc, err := nats.Connect(e.cfg.LocalNatsURL, localConnectOptions(e.cfg)...)
	if err != nil {
		return fmt.Errorf("connect to local NATS at %s: %w", e.cfg.LocalNatsURL, err)
	}
	defer nc.Close()
	e.st.setConn(nc)

	// Returns immediately when nothing is declared, and is fail-soft throughout:
	// sync moves data-plane traffic and is opt-in, so failing to wire up a bucket
	// logs, records it as down, and leaves the bus alone rather than taking the
	// site down with it. Entries that fail are retried on a timer; see sync.go.
	startSync(ctx, nc, e.cfg, e.st)

	<-ctx.Done()
	return nil
}

// localConnectOptions dials the leaf on this box.
//
// MaxReconnects(-1) is load-bearing and was a bug fix: the nats.go default gives
// up after about two minutes, after which this process is a zombie holding a
// dead connection and reporting nothing. An edge box outlives its own bus
// restarting, so it retries forever.
//
// The credential is a .creds file because a leaf node's own identity always is:
// `agent -leaf-config` writes one, and a gateway authenticates to its local leaf
// with it. There is no token or userpass branch here for that reason — and this
// used to be reached by agents that had neither, which is half of why a plain
// device must not run the edge at all.
func localConnectOptions(cfg *Config) []nats.Option {
	return []nats.Option{
		nats.UserCredentials(cfg.CredsFile),
		nats.Name("agent-edge"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Printf("⚠️ agent: disconnected from local NATS: %v (retrying indefinitely)", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			log.Printf("agent: reconnected to local NATS at %s", c.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			// Unreachable while MaxReconnects is -1 unless something calls
			// Close(), which is Run's deferred shutdown. If it ever fires for
			// another reason, say so rather than spinning in silence.
			log.Printf("⚠️ agent: local NATS connection closed permanently")
		}),
	}
}

// startEmbeddedNATS runs this site's leaf node inside the agent process, loading
// the same nats-leaf.conf the one-shot bootstrap writes.
//
// It reuses internal/natsd, the same wrapper the Control Plane's `serve --nats`
// uses, and inherits its two useful properties: it is an ordinary nats-server
// reading an ordinary config file, so splitting the two apart again is a config
// key rather than a migration; and it refuses to return a server that started
// but reported a fatal — a leaf whose JetStream silently failed to open is worse
// than one that did not start.
//
// It is not the default. Where an init system is already managing services, a
// separately supervised nats-server is still the better shape: the bus then
// survives an agent restart, which is exactly what you want when upgrading the
// agent on a live site.
func startEmbeddedNATS(cfg *Config) (*natsd.Server, error) {
	if _, err := os.Stat(cfg.EmbeddedConfig); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"leaf config not found at %s\n"+
					"       Bootstrap it first:  agent -leaf-config",
				cfg.EmbeddedConfig)
		}
		return nil, fmt.Errorf("cannot read leaf config %s: %w", cfg.EmbeddedConfig, err)
	}

	// LocalNatsURL is passed so natsd can check it against the port the server
	// actually binds. Getting those two out of step leaves this process dialling
	// a leaf that only it is hosting, and failing.
	return natsd.Start(cfg.EmbeddedConfig, cfg.LocalNatsURL, "nats.urls")
}
