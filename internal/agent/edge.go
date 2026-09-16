package agent

import (
	"path/filepath"

	"github.com/stone-age-io/agent/internal/config"
	"github.com/stone-age-io/agent/internal/edge"
)

// edgeEnabled reports whether this box has any edge work to do.
//
// There is deliberately no `edge.enabled` key behind this. "Gateway" is not a
// mode the config declares; it is the sum of the capabilities it turns on. A box
// that hosts a NATS server has a server to supervise, a box that relays twin
// state has a relay to run, and a box that serves /ready has an endpoint to
// serve — each is its own key, and any of them means the edge goroutine has a
// reason to exist.
//
// The alternative, a single flag naming the role, is a second control that can
// disagree with the first: `edge.enabled: false` beside `twin.enabled: true`
// has no correct behaviour.
func edgeEnabled(cfg *config.Config) bool {
	return cfg.NATS.ServerConfig != "" ||
		cfg.Twin.Enabled ||
		cfg.Observability.Addr != ""
}

// edgeConfig maps the agent's YAML onto the edge package's own struct.
//
// The indirection is what let the whole edge package arrive from the platform
// repo without a single changed function signature — and it keeps the edge
// package honest about what it actually needs, which is rather less than the
// agent's full config.
func edgeConfig(cfg *config.Config) *edge.Config {
	return &edge.Config{
		// The edge always talks to the leaf on this box. When the agent hosts
		// that server itself, natsd checks this against the port the config
		// actually binds — getting them out of step leaves the process dialling
		// a server only it is hosting.
		LocalNatsURL: firstURL(cfg.NATS.URLs),
		CredsFile:    cfg.NATS.Auth.CredsFile,

		// Where the one-shot writes nats-leaf.conf and the creds. Derived from
		// the creds file's own directory rather than being a separate key: the
		// generated conf references the creds by base name and nats-server
		// resolves that relative to the conf, so the two have to live together
		// anyway.
		OutputDir: filepath.Dir(cfg.NATS.Auth.CredsFile),

		EmbeddedConfig:    cfg.NATS.ServerConfig,
		TwinEnabled:       cfg.Twin.Enabled,
		ObserveAddr:       cfg.Observability.Addr,
		MetricsToken:      cfg.Observability.MetricsToken,
		ReadinessInterval: cfg.Observability.Interval,

		// HubDomain is not configured here. It arrives with the leaf config from
		// the platform, which is why a fleet of gateways is told it once on the
		// Control Plane instead of per box.
	}
}

func firstURL(urls []string) string {
	if len(urls) == 0 {
		return ""
	}
	return urls[0]
}
