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
// that hosts a NATS server has a server to supervise and a box that syncs KV
// buckets has buckets to wire up — each is its own key, and either means the
// edge goroutine has a reason to exist.
//
// The alternative, a single flag naming the role, is a second control that can
// disagree with the first: `edge.enabled: false` beside `sync.twin: true` has
// no correct behaviour.
//
// OBSERVABILITY IS NOT IN THIS LIST, and used to be. `observability.addr`
// defaults to 127.0.0.1:9100, so every agent ever deployed satisfied this
// predicate and started the edge subsystem — which dials its own second NATS
// connection using nothing but a .creds file, ignoring token and userpass auth
// and the whole nats.tls block, and registers three checks about a leaf node
// the box does not have. On a creds-authenticated device that bought a
// duplicate idle connection per agent and a nats_local check reporting the hub
// as "the local leaf"; on a token-authenticated one the dial failed and /ready
// returned 503 for ever while the agent's real connection was fine.
//
// Serving /ready everywhere was the correct intent — the README's argument for
// it still holds, and cmd.health travels over the link that breaks. It is the
// agent that serves it now, with checks it can actually answer, and the edge
// contributes its leaf checks only when there is a leaf. See observe.go.
func edgeEnabled(cfg *config.Config) bool {
	return cfg.NATS.ServerConfig != "" || cfg.Sync.Any()
}

// edgeConfig maps the agent's YAML onto the edge package's own struct.
//
// The indirection is what let the whole edge package arrive from the platform
// repo without a single changed function signature — and it keeps the edge
// package honest about what it actually needs, which is rather less than the
// agent's full config.
func edgeConfig(cfg *config.Config, hubDomain string) *edge.Config {
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

		EmbeddedConfig: cfg.NATS.ServerConfig,

		TwinEnabled: cfg.Sync.Twin,
		Mirrors:     syncBuckets(cfg.Sync.Mirrors),
		Relays:      syncBuckets(cfg.Sync.Relays),

		// HubDomain is not a config key. It arrives with the leaf config from
		// the platform, which is why a fleet of gateways is told it once on the
		// Control Plane instead of per box — and it is passed in here because
		// resolving it needs the platform client, which lives in agent.New.
		HubDomain: hubDomain,
	}
}

func syncBuckets(in []config.SyncBucket) []edge.Bucket {
	if len(in) == 0 {
		return nil
	}
	out := make([]edge.Bucket, 0, len(in))
	for _, b := range in {
		out = append(out, edge.Bucket{Name: b.Bucket, Keys: b.Keys})
	}
	return out
}

func firstURL(urls []string) string {
	if len(urls) == 0 {
		return ""
	}
	return urls[0]
}
