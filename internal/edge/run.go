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
	"github.com/stone-age-io/agent/internal/observe"
)

// Run brings up this gateway's edge subsystems and blocks until ctx is done.
//
// It is started as a goroutine from agent.New, like the Nebula manager, and
// stopped by the same cancel. It owns no signal handling and no ticker of its
// own: the agent already has both.
//
// The edge opens its OWN connection to the local leaf rather than reusing the
// agent's NATS client. They are genuinely different connections with different
// requirements — the agent's may point at the hub and retries on failed connect
// so it can come up before the overlay does, while this one is always local and
// reconnects forever. Sharing one would entangle the credential-rotation
// reconnect path with the twin watchers for no gain.
func Run(ctx context.Context, cfg *Config, version string) error {
	if cfg.EmbeddedConfig != "" {
		srv, err := startEmbeddedNATS(cfg)
		if err != nil {
			return err
		}
		defer srv.Stop()
	}

	st := &state{}

	// Observability starts BEFORE the connection attempt, so a box that cannot
	// reach its own bus still answers /ready — reporting the failure is the
	// whole job, and an endpoint that only appears once things work is useless
	// at exactly the moment it is needed.
	var reg health.Registry
	registerChecks(&reg, st)
	observe.New(observe.Options{
		Namespace:  "agent",
		Version:    version,
		Addr:       cfg.ObserveAddr,
		Token:      cfg.MetricsToken,
		Interval:   cfg.ReadinessInterval,
		Registry:   &reg,
		Collectors: []prometheus.Collector{&collector{state: st}},
		LogLabel:   "agent edge readiness",
	}).Start(ctx)

	nc, err := nats.Connect(cfg.LocalNatsURL, localConnectOptions(cfg)...)
	if err != nil {
		return fmt.Errorf("connect to local NATS at %s: %w", cfg.LocalNatsURL, err)
	}
	defer nc.Close()
	st.setConn(nc)

	// Guards on cfg.TwinEnabled itself, and is fail-soft throughout: twin moves
	// data-plane traffic and is opt-in, so failing to wire it up logs and leaves
	// the bus alone rather than taking the site down with it.
	startTwin(ctx, nc, cfg)

	<-ctx.Done()
	return nil
}

// localConnectOptions dials the leaf on this box.
//
// MaxReconnects(-1) is load-bearing and was a bug fix: the nats.go default gives
// up after about two minutes, after which this process is a zombie holding a
// dead connection and reporting nothing. An edge box outlives its own bus
// restarting, so it retries forever.
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
