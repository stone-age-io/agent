package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/stone-age-io/agent/internal/config"
	"github.com/stone-age-io/agent/internal/health"
	natsclient "github.com/stone-age-io/agent/internal/nats"
	"github.com/stone-age-io/agent/internal/nebula"
	"github.com/stone-age-io/agent/internal/platform"
	"github.com/stone-age-io/agent/internal/tasks"
)

// registerAgentChecks adds the questions every agent can answer about itself,
// leaf or no leaf.
//
// These live here rather than in internal/edge because they are not about a
// leaf. The edge package owns nats_local, hub_uplink and sync — all of which
// need a leaf on the box to mean anything — and it used to own the endpoint as
// well, which is how a plain device came to serve /ready with a failing check
// about a NATS leaf it had never been asked to run.
//
// Every check here is answerable first-hand and cheap. None of them dials
// anything: they read state the agent already maintains, because a readiness
// probe that makes network calls turns the probe rate into a load generator and
// a slow dependency into an outage.
func registerAgentChecks(
	reg *health.Registry,
	cfg *config.Config,
	natsClient *natsclient.Client,
	executor *tasks.Executor,
	nebulaManager *nebula.Manager,
	platformClient *platform.Client,
) {
	// The connection the agent actually uses — not a second one opened to
	// inspect it. This is the only check here that can fail, because it is the
	// only one whose failure means the agent is not doing its job at all.
	reg.Register("nats", func(ctx context.Context) health.Result {
		if !natsClient.IsConnected() {
			return health.Fail(
				"not connected to NATS",
				"Check nats.urls, the credentials for nats.auth.type, and whether the server is reachable "+
					"from this box. The agent retries on its own; it does not need restarting for this.",
			)
		}
		return health.OK("connected")
	})

	// Warn, not fail: commands still work over core NATS, and an agent that can
	// be asked what is wrong is worth more than one taken out of rotation.
	// Telemetry is what stops.
	reg.Register("jetstream", func(ctx context.Context) health.Result {
		if !natsClient.IsConnected() {
			return health.Skip("not connected, so JetStream cannot be checked")
		}
		if !natsClient.IsJetStreamAvailable() {
			return health.Warn(
				"JetStream is not usable on the connected server",
				"Telemetry is published to JetStream and is going nowhere; heartbeats and commands are "+
					"unaffected. Check that the account has JetStream enabled and that the stream binding "+
					"{prefix}.*.telemetry.> exists.",
			)
		}
		return health.OK("available")
	})

	// The scrape failure rate, which used to be a branch inside cmd.health's
	// status function. It is a check now so that one registry decides what
	// "degraded" means, instead of the NATS command and the /ready endpoint each
	// holding their own opinion.
	reg.Register("task_metrics", func(ctx context.Context) health.Result {
		if !cfg.Tasks.SystemMetrics.Enabled {
			return health.Skip("system metrics are disabled")
		}
		m := executor.GetTaskMetrics()
		if m.MetricsCount == 0 {
			return health.Skip("no metrics collected yet")
		}
		rate := float64(m.MetricsFailures) / float64(m.MetricsCount)
		if rate > 0.5 {
			return health.Warn(
				fmt.Sprintf("%d of %d metrics collections failed", m.MetricsFailures, m.MetricsCount),
				"With tasks.system_metrics.source: exporter, check that the exporter at "+
					cfg.Tasks.SystemMetrics.ExporterURL+" is running and exposes the expected metric names.",
			)
		}
		return health.OK(fmt.Sprintf("%d collected, %d failed", m.MetricsCount, m.MetricsFailures))
	})

	// The overlay, when there is one. Warn rather than fail for the reason the
	// health command already gave: NATS and telemetry are unaffected, and
	// turning a fleet dashboard red over someone else's network problem is
	// noise.
	reg.Register("nebula", func(ctx context.Context) health.Result {
		if nebulaManager == nil {
			return health.Skip("the overlay is not enabled")
		}

		// Never Health(): it waits on the manager's lock, which Sync holds
		// across apply, verify and any rollback — minutes, in the worst case.
		// A mutex does not respect this check's deadline, so blocking here
		// would hold up the whole probe and every other check in it.
		h := nebulaManager.HealthIfIdle()
		if h == nil {
			return health.Skip("a sync is in progress; the overlay will be reported on the next probe")
		}

		switch {
		case !h.Running:
			return health.Warn(
				"the overlay is enabled but not running: "+h.Error,
				"Ask for cmd.nebula {\"action\":\"restart\"}, and check the logs for why the config was "+
					"refused. On Windows a missing wintun.dll is the usual cause.",
			)
		case h.RolledBack:
			return health.Warn(
				"running on the cached config: the last one fetched could not reach the mesh",
				"The device is on the overlay with its previous config. Check the config the platform holds "+
					"for this host — a rollback means the new one did not hand shake with a lighthouse.",
			)
		case h.Tunnels == 0:
			return health.Warn(
				"on the overlay with no tunnels established",
				"Check that a lighthouse is reachable from this box and that this host's certificate has "+
					"not been revoked through pki.blocklist.",
			)
		}
		return health.OK(fmt.Sprintf("%d tunnel(s), config %s", h.Tunnels, h.ConfigRevision))
	})

	// The platform conversation, when the agent depends on one.
	//
	// A credential sync that quietly stops working is fatal on a delay: the
	// .creds on disk keeps working until the platform re-mints or revokes it,
	// and only then does the agent discover it has been unable to reach the
	// platform for weeks. Until now creds_sync appeared in cmd.health as a
	// string in a list of task names and nowhere else.
	reg.Register("platform_sync", func(ctx context.Context) health.Result {
		if platformClient == nil {
			return health.Skip("this agent does not get its credentials from the platform")
		}
		last := platformClient.LastSync()
		if last.IsZero() {
			return health.Warn(
				"no successful platform sync since this agent started",
				"The credential on disk still works until the platform re-mints or revokes it. Check "+
					"platform.url, and that the session file or password_env is still valid.",
			)
		}

		// Two intervals, so a single missed run is not an alert. The token TTL
		// is seven days and each sync renews it, which is the real deadline
		// this is watching for.
		age := time.Since(last)
		if budget := 2 * cfg.Platform.SyncInterval; budget > 0 && age > budget {
			return health.Warn(
				fmt.Sprintf("last successful platform sync was %s ago", age.Round(time.Minute)),
				"The platform session token expires after seven days and every sync renews it. Once it "+
					"lapses the agent needs platform.password_env to get back in.",
			)
		}
		return health.OK(fmt.Sprintf("last sync %s ago", age.Round(time.Second)))
	})
}
