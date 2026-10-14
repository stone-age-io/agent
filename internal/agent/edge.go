package agent

import (
	"os"
	"path/filepath"
	"runtime"

	"go.uber.org/zap"

	"github.com/stone-age-io/agent/internal/config"
	"github.com/stone-age-io/agent/internal/edge"
	"github.com/stone-age-io/agent/internal/platform"
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
		// generated conf and the creds it names (by absolute path) are written
		// side by side, and the refresh finds the conf there again.
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

// leafRefresher builds the job that keeps a generated nats-leaf.conf's account
// JWT current, or returns nil when there is nothing for it to do.
//
// There is no key that turns it on. It runs where `agent -leaf-config` has
// written a conf -- beside nats.auth.creds_file, the only place that one-shot
// writes -- on a platform-managed agent, because the leaf config comes from the
// platform. That is the same "capabilities, not a role" rule as edgeEnabled:
// the file existing is the declaration. A hand-written nats-server config
// elsewhere is never touched.
//
// How the leaf is reloaded depends on who runs it:
//
//   - nats.server_config IS that file (os.SameFile, so a symlink or a
//     differently spelled path still counts): this process hosts the leaf, and
//     reloads it in-process.
//   - anything else: something else supervises it, and it is sent SIGHUP via
//     the pid_file the generated conf names. On Windows it cannot be, and the
//     operator is told so once here and again by leaf_preload.
//
// It says which, once, at startup -- "why did it not reload?" should be
// answerable from the first lines of the log.
func leafRefresher(cfg *config.Config, client *platform.Client, edgeNode *edge.Edge, logger *zap.Logger) *edge.LeafRefresher {
	if client == nil {
		return nil
	}

	credsFile := cfg.NATS.Auth.CredsFile
	confPath := edge.LeafConfPath(credsFile)
	confInfo, err := os.Stat(confPath)
	if err != nil {
		logger.Info("Leaf preload refresh inactive: no generated leaf config",
			zap.String("path", confPath))
		return nil
	}

	embedded := false
	if edgeNode != nil && cfg.NATS.ServerConfig != "" {
		hosted, err := os.Stat(cfg.NATS.ServerConfig)
		embedded = err == nil && os.SameFile(hosted, confInfo)
	}

	pidPath := edge.LeafPidPath(credsFile)
	reload := func() error { return edge.SignalLeafReload(pidPath) }
	mode := "separately supervised (SIGHUP via " + pidPath + ")"
	if embedded {
		reload = edgeNode.ReloadEmbedded
		mode = "embedded (reloaded in-process)"
	}

	interval := cfg.Platform.LeafRefreshInterval
	if interval > 0 {
		logger.Info("Leaf preload refresh watching the generated leaf config",
			zap.String("path", confPath),
			zap.String("leaf", mode),
			zap.Duration("interval", interval))
	} else {
		logger.Info("Leaf preload refresh off (platform.leaf_refresh_interval: 0); cmd.leaf_refresh still runs it",
			zap.String("path", confPath),
			zap.String("leaf", mode))
	}
	if runtime.GOOS == "windows" && !embedded {
		logger.Warn("A separately supervised leaf cannot be signalled to reload on Windows: " +
			"restart it after each refresh that rewrites the leaf config, or set nats.server_config to host it in-process. " +
			"The leaf_preload check reports when it is behind.")
	}

	return edge.NewLeafRefresher(credsFile, client, reload, logger)
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
