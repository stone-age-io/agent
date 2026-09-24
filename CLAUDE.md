# CLAUDE.MD - Agent Project Guide

## Project Overview

A lightweight, NATS-native system management and observability agent for Windows, Linux, and FreeBSD. The agent provides remote management and monitoring capabilities through secure NATS messaging.

**Key Design Principles:**
- Lightweight: <50MB RAM, <1% CPU target
- Secure: TLS support, allowlisted execution, and no inbound management API
- NATS-Native: management and telemetry are NATS (JetStream for telemetry, Core
  NATS for commands), always dialed outbound
- Cross-Platform: Windows, Linux, FreeBSD support with platform-specific implementations

**What listens.** "No listening ports" was true once and is not any more; say it
precisely instead. Nothing can *instruct* the agent except over its own
authenticated NATS connection, which it dials outbound -- that is the property
worth claiming. But `observability.addr` defaults to `127.0.0.1:9100`, so every
agent serves `/ready` and `/metrics` unless the key is set empty; the platform
block talks outbound HTTPS to the Control Plane; and `nats.server_config` makes
a gateway host a nats-server that local devices connect *in* to. Note the
readiness default collides with node_exporter's own 9100 on Linux and FreeBSD.

**Observability belongs to the agent, not to the edge.** It reads as an edge
feature because the whole `observe`/`health`/`metrics` stack arrived with the
edge -- and for one release it *was* one, which is how `observability.addr`
came to be a disjunct in `edgeEnabled()`. Since that key defaults to a real
address, every agent in the fleet then started the edge subsystem: a second
NATS connection built from nothing but a `.creds` file (ignoring token and
userpass auth and the whole `nats.tls` block), plus three readiness checks
about a leaf node the box did not have. Creds-authenticated devices carried a
duplicate idle connection and reported the hub as "the local leaf"; token-
authenticated ones failed the dial and served `/ready` as 503 for ever while
their real connection was fine. `internal/agent/observe.go` owns the registry
now and the edge contributes to it. Do not put the endpoint back in `edge`.

## Build & Test Commands

```bash
# Build for current platform
make build

# Build for all platforms (Linux amd64/arm64, Windows, FreeBSD)
make build-all VERSION=0.1.0

# Run tests with race detection and coverage
make test

# Run tests with HTML coverage report
make test-coverage

# Format code
make fmt

# Run linter
make lint

# Download and tidy dependencies
make deps

# Clean build artifacts
make clean

# Install dev tools (goimports, golangci-lint)
make install-tools
```

## Project Structure

```
agent/
├── cmd/agent/
│   ├── main.go                # Entry point, service management
│   └── leafconfig.go          # `agent -leaf-config`: one-shot leaf bootstrap
├── internal/
│   ├── agent/
│   │   ├── agent.go           # Core agent orchestration
│   │   ├── observe.go         # The readiness registry: checks any agent can answer
│   │   └── edge.go            # edgeEnabled() + the YAML -> edge.Config mapping
│   ├── config/                # Configuration loading & validation
│   │   ├── config.go          # Config structs and Load()
│   │   └── defaults.go        # Platform-specific defaults
│   ├── platform/              # stone-age.io platform credential lifecycle
│   │   ├── platform.go        # EnsureCredentials, Sync, Rotate
│   │   ├── nebula.go          # NebulaSource: this thing's nebula_host config
│   │   ├── leafconfig.go      # LeafConfig: GET /api/me/leaf-config
│   │   └── session.go         # Session file (token, revisions, hub domain)
│   ├── edge/                  # Leaf-node duties, on a box that runs one
│   │   ├── run.go             # Edge: build, RegisterChecks, Collector, Run
│   │   ├── leafconf.go        # buildLeafConf: nats-leaf.conf generator
│   │   ├── bootstrap.go       # WriteLeafConfig: conf 0644 + creds 0600
│   │   ├── config.go          # edge.Config, mapped from the agent's YAML
│   │   ├── sync.go            # Declared buckets: mirrors down, relays up, wiring retried
│   │   ├── twin.go / kv.go    # The twin preset + the relay itself
│   │   ├── checks.go          # nats_local (fail), hub_uplink + sync (warn)
│   │   └── collector.go       # agent_edge_* gauges from the leaf's varz/leafz
│   ├── health/                # Readiness check registry + background prober
│   ├── metrics/               # Prometheus exposition + scrape token
│   ├── natsd/                 # Embedded nats-server (shared by edge)
│   ├── observe/               # The /ready + /metrics listener
│   ├── nebula/                # Embedded Nebula overlay host (opt-in)
│   │   ├── nebula.go          # Manager: lifecycle, apply/verify/rollback, Health
│   │   ├── source.go          # Source interface and FileSource
│   │   ├── logger.go          # Nebula's log/slog output into zap
│   │   └── wintun_*.go        # Windows wintun.dll hint on a start failure
│   ├── nats/                  # NATS client and command handlers
│   │   ├── client.go          # Connection, publish, subscribe, ForceReconnect
│   │   └── handlers.go        # Command handlers (ping, exec, health, etc.)
│   ├── scheduler/             # Scheduled task execution
│   │   └── scheduler.go       # gocron-based task scheduling
│   ├── tasks/                 # Task implementations
│   │   ├── executor.go        # Task executor with stats tracking
│   │   ├── heartbeat.go       # Heartbeat message creation
│   │   ├── collector.go       # MetricsCollector interface
│   │   ├── collector_builtin.go   # gopsutil-based metrics (default)
│   │   ├── collector_exporter.go  # Prometheus exporter scraping (optional)
│   │   ├── metrics.go         # Metrics types and validation
│   │   ├── metrics_names.go   # Platform-specific metric names (exporter mode)
│   │   ├── service.go         # Service status constants
│   │   ├── service_*.go       # Platform-specific service control
│   │   ├── inventory_*.go     # Platform-specific inventory collection
│   │   ├── logs.go            # Log file retrieval
│   │   └── exec_*.go          # Platform-specific command execution
│   └── utils/
│       ├── math.go            # Utility functions (Round)
│       └── timeutil.go        # NowRFC3339 timestamp helper for wire payloads
├── docs/                      # Install guides, credentials, Nebula (+ nebula-design.md and
│                              # edge-sync-design.md, design records not guides)
├── Makefile                   # Build automation
└── go.mod                     # Go 1.26+ required (Nebula sets the floor)
```

## Architecture

### Communication Flow
- **Telemetry (JetStream)**: Metrics, service status, inventory → published asynchronously
- **Heartbeats (Core NATS)**: Fire-and-forget liveness beacons — deliberately NOT JetStream (last-write-wins; a backlog of stale beats after reconnect would be harmful). Matches access-control/kiosk heartbeat semantics.
- **Commands (Core NATS)**: Request/reply pattern with panic recovery
- **Subject Naming**: `{prefix}.{code}.{type}` (e.g., `agents.server-01.heartbeat`)
- **Stream contract**: The server-side JetStream stream must bind `{prefix}.*.telemetry.>` (NOT `{prefix}.>`) so heartbeats stay outside the stream by subject construction

### Key Components

1. **Agent** (`internal/agent/agent.go`): Main orchestrator - initializes config, logger, NATS, scheduler, and handlers

2. **Config** (`internal/config/`):
   - Validates code (alphanumeric, dash, underscore only; legacy key `device_id` accepted as fallback)
   - Optional location (single NATS token), carried in heartbeat/telemetry payloads
   - Supports auth types: creds, token, userpass, platform, none
   - The platform relationship is a TOP-LEVEL `platform:` block, not nested under
     `nats.auth`. Three subsystems read it — the NATS credential lifecycle, the
     Nebula config source, and the leaf bootstrap — so burying it under one of
     them made the other two ask "is some other section's type field set to a
     particular string" instead of "is the block present"
   - Requires https for the platform URL unless `allow_insecure_url` is set
   - Platform-specific defaults for paths and exporter URLs

3. **Platform** (`internal/platform/`): credential lifecycle for auth type `platform`
   - The agent is a Thing: authenticates as itself against the `things` auth collection;
     its credential lives on the related nats_user record's `creds_file` field
   - `EnsureCredentials()` — first boot only: password auth with
     `expand=nats_user,location`, writes .creds (0600, atomic). Skips if the file exists;
     restores it via the stored session (no password) if the file is gone but a token remains
   - `Sync()` — startup + `platform.sync_interval`: refreshes the session token
     (**no expand**), probes `?fields=updated`, and reads the credential only when that
     revision moved. A .creds file embeds the nkey seed, and PocketBase does not apply
     `fields` to auth responses, so this split is what keeps key material off the wire
   - `Rotate()` — `POST /api/me/nats-creds/rotate`, then read. pb-nats re-mints inside
     the record-update model hook before the save commits, so no polling is needed.
     Rotation is not revocation
   - Fails fast if the thing record's `code` doesn't match the config's `code`, on every
     authentication; warns if the expanded location's `code` differs
   - Session file (token + credential revision + nebula revision, 0600) makes `password_env`
     optional after first boot. The platform's thing token TTL is 7 days, renewed by each sync
   - `Client.mu` guards the session file: the credential sync and the Nebula sync are
     separate scheduled jobs that both read-modify-write it. Exported methods take the
     lock and delegate to a `...Locked` variant — Go mutexes are not reentrant, and
     `EnsureCredentials` calls `syncLocked`
   - Never logs response bodies — one of them is a private key

4. **Nebula** (`internal/nebula/`): embedded overlay host, opt-in via `nebula.enabled`
   - Free of HTTP on purpose. Fetching is a `Source`; the platform one lives in
     `internal/platform/nebula.go` beside the auth it shares
   - Applying is a ladder of OUTCOMES, never a table of config keys: reload → (if the
     overlay does not come back) restart → (still not) roll back to the cached config.
     Nebula already classifies its own reloads internally, and a duplicate table here
     would drift from it on every upgrade
   - Verification waits for a lighthouse handshake only when the config names a
     lighthouse to reach. A lighthouse has no peer to hand shake with, and a false
     positive would cause the outage the rollback prevents
   - `nebula.sync_interval` is the device's revocation latency (Nebula has no CRL),
     which is why it is bounded above as well as below

5. **NATS Client** (`internal/nats/client.go`):
   - `RetryOnFailedConnect`: an unreachable bus does not stop the agent from starting,
     so NATS and the Nebula overlay can come up in either order. Configuration errors
     (unknown auth type, unreadable TLS material) still fail fast
   - JetStream is validated on every connect and reported through `cmd.health`, not
     enforced once at startup
   - A connection nats.go abandons closes `Lost()`, which exits the agent for restart.
     That is the revoked-credential recovery path: nats.go gives up after two
     consecutive auth failures, and the restart re-runs the startup credential sync
   - TLS 1.2+ support with optional mTLS
   - Async publishing with automatic retries
   - `creds` and `platform` auth both connect with the .creds file; `UserCredentials`
     re-reads it on every reconnect, so `ForceReconnect()` adopts a replaced credential

6. **Scheduler** (`internal/scheduler/scheduler.go`):
   - Uses gocron/v2 for interval-based scheduling
   - Context-aware cancellation for clean shutdown
   - Panic recovery for all tasks
   - The internal `creds_sync` job is scheduled only when a CredsSyncer is supplied
     (platform auth). Its cadence is `platform.sync_interval` -- there is no longer a
     `tasks.creds_sync` config key, because the cadence of a platform conversation
     belongs beside the platform rather than in the task list
   - `nebula_sync` task is scheduled only when a NebulaSyncer is supplied (overlay enabled)

7. **Executor** (`internal/tasks/executor.go`):
   - Central task execution with stats tracking
   - Configurable metrics collection via MetricsCollector interface
   - Supports builtin (gopsutil) or exporter (Prometheus) sources
   - Command success/error recording

8. **Edge** (`internal/edge/`): what an agent does when the box it runs on is also
   a NATS **leaf node**. Absorbed from the platform repo, where it was a separate
   binary called `leaf-sync`.

   - **There is no `edge.enabled` key, deliberately.** "Gateway" is not a mode the
     config declares; it is the sum of the capabilities it turns on. `edgeEnabled()`
     (`internal/agent/edge.go`) is `nats.server_config != "" || sync.Any()`. A single
     flag naming the role would be a second control that can disagree with the first
     -- `edge.enabled: false` beside `sync.twin: true` has no correct behaviour.
     `observability.addr` is **not** in that list; see "What listens" above for what
     happened when it was
   - `edge.Edge` is built in `agent.New` and run from `agent.Run`. The split exists
     because `RegisterChecks` and `Collector()` have to reach the agent's readiness
     registry before anything probes or scrapes it
   - **A gateway is a Thing, not a special record.** It logs in against `things` like
     any other agent and calls `GET /api/me/leaf-config` for the leaf material. The
     platform serves that to ANY authenticated Thing and gates it on nothing, because
     everything in the payload is either public trust material (the operator, account
     and `$SYS` account JWTs, which every server validates anyway) or the caller's own
     credential, which it must already hold to connect. There was a `leaf_nodes`
     collection; it was dropped, because `thing_types` already says what a device is
   - **The JetStream domain IS the thing's code.** The platform computes it rather than
     storing it, and `buildLeafConf` writes it into both `server_name` and
     `jetstream { domain }`. The console matches a leaf's reported `server_name` back
     to a Thing's code to show a site attached, so those two must not diverge
   - The edge goroutine starts from `Run()` like the Nebula manager and stops on the
     same cancel

9. **Leaf config generation** (`internal/edge/leafconf.go`): **a generated
   `nats-leaf.conf` must satisfy operator-mode validation, and no string assertion
   can check that.** Two directives are mandatory and were both missing for months,
   so the generator produced a file `nats-server` refused to load -- invisible,
   because the only tests were `strings.Contains` over the output.

   1. Every leaf remote needs an `account` key naming the local account.
   2. `resolver_preload` needs the **`$SYS` account JWT** as well as the org's. The
      operator JWT names a system account and `resolver: MEMORY` has nowhere to fetch
      it, so without it the server dies with `error resolving system account: account
      missing` **before JetStream starts**. Preloading the `$SYS` *account* JWT is
      public trust material and grants nothing -- connecting AS `$SYS` needs a `$SYS`
      **user** credential, which the platform never serves.

   `TestBuildLeafConfIsAcceptedByNATSServer` runs the real generator's output through
   the `nats-server` package's own `ProcessConfigFile` + `NewServer` (no ports, no
   network). It moved from the platform repo unchanged apart from its imports, and that
   was deliberate: **keep it, and do not replace it with more `Contains` checks.**

10. **Edge sync** (`internal/edge/sync.go`, `internal/edge/twin.go`): KV buckets moved
    between this site's JetStream domain and the hub, off by default. Two directions,
    two mechanisms, a list each. See `docs/edge-sync-design.md` for the decision record.

    | Direction | Mechanism | Config |
    |---|---|---|
    | hub to edge | JetStream **mirror** (server-maintained) | `sync.mirrors` |
    | edge to hub | application **relay** | `sync.relays` |

    `sync.twin: true` is a preset expanding to one entry in each direction —
    `twin_desired` mirrored down, `twin` relayed up. It replaces the former
    `twin.enabled`, which is now **rejected by name** at config load: it never worked
    (see below) and viper silently ignores keys it does not know.

    - **One writer per bucket is the whole safety property.** A single bucket written
      from both ends does not pick a loser on a conflict, it *oscillates*: two
      concurrent values swap across the link, then swap back, each write generating the
      next event. Measured at ~170,000 writes to one key in 300 ms before the buckets
      were split. Encoding the owner in the key (`thing.S01.state.temp`) was tried and
      reverted -- same safety, but it taxes every key in firmware, rules and widgets,
      and a mistyped segment silently never syncs.
      **This used to be structural and is now a check.** With two built-in buckets going
      opposite directions it was unrepresentable; with a list it is one typo away, so
      `config.validateSyncConfig` refuses to start when a bucket appears in both
      directions. That check is the invariant now — do not weaken it.
    - **Wiring is retried, on a one-minute ticker** (`syncRetryInterval`, a var so
      tests can shrink it, like `twinRetryInterval`). It used to be attempted once
      at startup, so the ordinary deployment order -- install the gateway, then
      create the bucket at the hub -- left that bucket reporting down until somebody
      restarted the agent. Relays already healed themselves once started
      (`relay.supervise`) and mirrors are maintained by the server; the wiring step
      was the one with no second chance. `wireDown` **must** skip entries that are
      already up: re-running `ensureMirror` is harmless, but a second `startRelay`
      opens a second watcher on the same bucket and doubles the hub's write rate
      for ever, silently. The repeat-failure log is suppressed when the reason has
      not changed, because a missing hub bucket fails identically every minute
    - **Downstream is a mirror, not a relay,** because it has exactly one origin, and
      it serves last-known values offline since the edge never writes it. Configured on
      the RECEIVING side, so there is no hub-side stream to mutate and no race between
      sites.
    - **Upstream cannot be a source.** Aggregating N sites natively needs N sources
      all named `KV_twin`, which requires the server's internal `iname` that nats.go
      does not expose; the alternative is `twin_<code>` at every edge and a rule engine
      reading a different bucket name per site. Hence the relay for this one direction --
      do not "finish the job" by making it a source without solving that. **This is also
      why `SyncBucket` has no rename field:** a bucket carries the same name at both
      ends, or the reason the relay exists evaporates.
    - **A mirror's filter cannot be changed later.** nats-server rejects any change to a
      mirror block on an existing stream (`JSStreamMirrorNotUpdatableErr`), so narrowing
      an existing mirror means deleting and recreating the bucket on every site by hand.
      `ensureMirror` detects the mismatch and reports it rather than repairing it.
    - `keys:` is a KV **key pattern** in both directions (`line-a.>`, never
      `$KV.recipes.line-a.>`); the agent builds the `$KV.<bucket>.` prefix for a
      mirror's subject filter and passes the pattern straight to `WatchFiltered` for a
      relay. Config rejects a `$KV.` written there, because it would be silently
      doubled.
    - Relay mechanics, boring on purpose: one watcher edge-to-hub so there is no echo;
      compare-before-write (the watcher replays every current value on start, so without
      it each restart would burn a revision per key); that replay IS the resync after an
      outage; deletes are relayed explicitly, because a KV delete is a tombstone rather
      than an absence and dropping it leaves the key live at the hub forever; upsert and
      never reconcile, so one site can never purge another site's keys.
    - **The edge creates local buckets, never hub buckets** — except for the two preset
      names, whose shape the platform knows. A typo in one site's YAML that creates a
      local bucket is that site's problem; one that creates a hub bucket is everyone's,
      with whatever retention that site guessed, and the console then adopts it.
      `hubBucket()` is the single place that decides this, and **both directions must
      go through it.** The mirror path once did not, and JetStream cannot validate a
      cross-domain mirror source at creation time — so a mirror of a hub bucket that
      does not exist was accepted, reported healthy, and received nothing for ever.
    - Buckets are created if absent and otherwise **left alone** -- unlike a private
      mirror, these are shared with the console and operators, so the agent does not
      reassert retention over whatever they set. Keep `bucketConfig()` in step with
      `TWIN_BUCKET_CONFIG` in the platform's `ui/src/utils/twin.ts`: whoever creates a
      bucket first defines it, and the two now live in different repositories so nothing
      can enforce that they agree.
    - **The hub's JetStream domain is not a config key.** It arrives with the leaf config
      from the platform and is cached in the session file (`platform.Client.HubDomain`),
      so the network is touched at most once. It reached the running agent nowhere at
      all until this was added, which is why `twin.enabled` disabled itself on every
      start for the whole of its life. `config.PresetBuckets` duplicates the two preset
      names because `config` cannot import `edge` (edge → platform → config would
      cycle); `TestPresetBucketNamesMatchConfig` guards the drift.

11. **Readiness and metrics** (`internal/health`, `internal/metrics`, `internal/observe`,
    wired in `internal/agent/observe.go`): a site's real health can only be measured on
    the site. `cmd.health` travels over NATS, which is the link that breaks -- a box
    whose uplink is down is exactly the one you want to ask, and that is when it goes
    quiet. `observability.addr` empty serves neither endpoint; the checks still run and
    still log, and a bind failure is never fatal.

    - **ONE REGISTRY DECIDES WHAT IS WRONG, and both channels report it.** `cmd.health`
      carries the prober's `health.Report` in `checks`, and its three words are a pure
      function of `Report.State` (`fail` -> unhealthy, `warn` -> degraded, `ok` ->
      healthy). There is deliberately **no `edge` block** in that response: everything
      a gateway knows first-hand is a registered check, so it arrives without a second
      struct to define, fill and keep in step. Adding a fact to `cmd.health` means
      registering a check -- which also puts it on `/ready` and in
      `agent_check_state`. The old inline ladder is what let a gateway with every
      bucket down answer "healthy" over NATS while its own `/ready` disagreed.
    - The registry is assembled from two sources that never overlap: `registerAgentChecks`
      (`nats`, `jetstream`, `task_metrics`, `nebula`, `platform_sync`) answers for any
      agent; `Edge.RegisterChecks` adds `nats_local`, `hub_uplink` and `sync`, which need
      a leaf on the box to mean anything.
    - **`observe.Start` calls `prober.Start` synchronously, not in a goroutine**, and
      `agent.New` starts it *before* subscribing to commands. That ordering is what
      guarantees `cmd.health` always has a report to answer with. Every check reads
      state the process already holds, so the first round costs microseconds.
    - A check must never dial anything. A readiness probe that makes network calls
      turns the probe rate into a load generator and a slow dependency into an outage.

    - **An islanded edge WARNS, it does not fail.** `hub_uplink` is a warn and
      `nats_local` is a fail. Local NATS still works and devices keep running, and that
      autonomy is why a leaf node exists -- 503 would invert the design.
    - **Omit, never zero.** When the leaf's monitoring port is unreachable the
      server-derived series are left out rather than reported as 0: zero would claim an
      islanded site with no devices, which is a much louder statement than "not
      scraped". Same reason `skipped` ranks below `ok` in the check registry.
    - The server-derived rows come from the leaf's own loopback monitoring port, which
      is how the edge reads its own server **without ever holding a `$SYS` user
      credential**. It works the same whether the leaf is embedded or a separate process.

12. **`internal/health`, `internal/metrics` and `internal/natsd` are DUPLICATED from
    the platform repo, not extracted into a shared module.** That was the decision and
    it should stay one: two small copies that drift are cheaper to live with than a
    third repository to version, tag and keep both consumers pinned to -- and the two
    processes check genuinely different things (the Control Plane checks its operator
    trust and its own database; the agent checks a local leaf and an uplink). The same
    note is in the platform's CLAUDE.md. If they ever need to agree on something, write
    a test on each side rather than a library between them.

## Platform-Specific Files

Use build tags for platform-specific code:
- `//go:build windows` - Windows implementations
- `//go:build linux` - Linux implementations
- `//go:build freebsd` - FreeBSD implementations
- `//go:build !windows && !linux && !freebsd` - Stub implementations

**Key platform differences:**
- Windows: PowerShell execution, Windows Service SCM
- Linux/FreeBSD: Bash execution, systemd/rc.d
- Metrics: Builtin (gopsutil) by default; optional windows_exporter (port 9182) or node_exporter (port 9100)

## NATS Subjects

### Heartbeat (Core NATS, fire-and-forget)
- `{prefix}.{code}.heartbeat` - Liveness beacon, payload `{code, location, ts}` (agent version deliberately absent — the health command owns it)

### Telemetry (JetStream)
- `{prefix}.{code}.telemetry.system` - System metrics (CPU, memory, disk)
- `{prefix}.{code}.telemetry.service` - Service status
- `{prefix}.{code}.telemetry.inventory` - System inventory

All telemetry payloads carry `code`, `location`, and `ts` (RFC3339 UTC) so messages are self-describing for any direct subscriber.

### Commands (Core NATS Request/Reply)
- `{prefix}.{code}.cmd.ping` - Connectivity check
- `{prefix}.{code}.cmd.service` - Service control (start/stop/restart)
- `{prefix}.{code}.cmd.logs` - Log file retrieval
- `{prefix}.{code}.cmd.exec` - Custom command execution
- `{prefix}.{code}.cmd.health` - Agent health check (agent version, the `nebula` block when the overlay is enabled, the three command allowlists, and `checks`: the same readiness report `/ready` serves)
- `{prefix}.{code}.cmd.rotate_creds` - Re-mint this agent's NATS credential on the platform, then reconnect (platform auth only; answers with an error otherwise)
- `{prefix}.{code}.cmd.nebula` - Overlay actions: `sync` (pull and apply now) or `restart` (bounce Nebula on the running config). Enabled agents only; answers with an error otherwise

Command responses use `ts` (RFC3339 UTC) for their timestamp field.

**`cmd.nebula` is the one command that answers before it acts.** It replies
`accepted` and does the work asynchronously, because both actions can interrupt
the tunnel the request arrived through when NATS rides the overlay — a reply sent
afterwards would never land, and the caller would see a timeout on an operation
that succeeded. The outcome is read from `cmd.health`. Anything spawned this way
needs its own `recover()`: `handleWithRecovery` wraps the handler, not its
goroutines.

## Command line

`agent` takes flags, not subcommands -- there is no cobra here and adding one for
two one-shots would be a dependency for a `switch`.

```bash
agent -config /etc/agent/config.yaml   # run (the default)
agent -version                         # print the version and exit
agent -service install|start|stop|...  # register with the host service manager
agent -leaf-config                     # one-shot: fetch this thing's leaf config,
                                       # write nats-leaf.conf + creds, exit
```

**`-leaf-config` has to be separable from running.** The usual edge shape is a
separately supervised `nats-server`, and that server needs its config file to exist
before it starts -- which is before this agent has anything to connect to.
Bootstrapping and running cannot be the same invocation. It requires
`nats.auth.type: "platform"`, since a leaf config comes from the platform, and writes
the conf 0644 beside the creds at 0600.

## Configuration

Default config paths:
- Windows: `C:\ProgramData\Agent\config.yaml`
- Linux: `/etc/agent/config.yaml`
- FreeBSD: `/usr/local/etc/agent/config.yaml`

Key config sections:
```yaml
code: "unique-id"                # Required, alphanumeric/dash/underscore (legacy key: device_id)
location: "hq"                   # Optional, single NATS token, carried in telemetry payloads
subject_prefix: "agents"         # NATS subject prefix
platform:                        # The platform relationship. TOP-LEVEL: three
  url: "https://platform.example.com"      # subsystems read it (NATS creds, Nebula
  identity: "thing@example.com"            # source, leaf bootstrap), so it is not
  password_env: "AGENT_PLATFORM_PASSWORD"  # nested under any one of them.
  sync_interval: "24h"           # 1h-72h; credential refresh. Was tasks.creds_sync.interval
  session_file: "/path/to/platform-session.json"  # default: beside nats.auth.creds_file
  allow_insecure_url: false      # http:// platform URL, development only
nats:
  urls: ["nats://host:4222"]     # NATS server URLs
  auth:
    type: "creds"                # creds, token, userpass, platform, none
    creds_file: "/path/to/creds"
  server_config: ""              # Non-empty: host a nats-server from this file, in
                                 # this process. On a gateway that is the file
                                 # `agent -leaf-config` wrote, but it works with any
                                 # nats-server config. nats.urls must name the port
                                 # it listens on -- startup refuses a disagreement.
                                 # Empty where systemd or Docker supervises one, so
                                 # the bus survives an agent restart.
  tls:
    enabled: true
    ca_file: "/path/to/ca.pem"
sync:                            # KV buckets moved between this leaf's JetStream
  twin: false                    # domain and the hub. Off by default: it moves
                                 # data-plane traffic, so an upgrade must not start
                                 # doing it silently. Requires the platform block.
                                 # `twin: true` is the preset for the two digital-twin
                                 # buckets. (The old `twin.enabled` is rejected by name.)
  mirrors:                       # hub -> edge, server-maintained
    - bucket: "recipes"
      keys: "line-a.>"           # optional key pattern. CANNOT be changed later:
                                 # nats-server refuses to update a mirror block.
  relays:                        # edge -> hub, application relay
    - bucket: "events"
      keys: "site.S01.>"         # optional; a site cannot relay keys outside it
                                 # A bucket may appear in ONE list, never both.
observability:                   # /ready and /metrics on this box
  addr: "127.0.0.1:9100"         # empty serves neither; checks still run and log
  metrics_token: ""              # empty = open; Bearer or Basic when set
  interval: "15s"
nebula:                          # Embedded overlay host, off by default
  enabled: false
  source: "platform"             # "platform" (needs the platform block) or "file"
  config_file: "/path/to/nebula.yaml"        # Only for source: "file"
  cache_file: "/var/lib/agent/nebula-cache.yaml"  # Last config that reached the mesh
  sync_interval: "10m"           # 1m-1h. THIS IS THE REVOCATION LATENCY
  verify_timeout: "30s"          # 5s-5m, before restart then rollback
tasks:
  heartbeat:
    enabled: true
    interval: "1m"               # Minimum 10s
  system_metrics:
    enabled: true
    interval: "5m"               # Minimum 30s
    source: "builtin"            # "builtin" (default) or "exporter"
    exporter_url: "http://localhost:9182/metrics"  # Only for exporter mode
commands:
  scripts_directory: "/path/to/scripts"
  allowed_services: ["nginx"]
  allowed_commands: ["df -h"]
  timeout: "30s"                 # 5s-5m range
```

## Security Notes

- All commands/services must be whitelisted in config
- Log path access: the cleaned, absolute request must EQUAL a `filepath.Glob` match of
  an allowed pattern, and that is the whole check -- it already rules out traversal.
  Do not add a denylist in front of it: the last one ("sam", "system32", "..") refused
  `/var/log/samba/*` and protected nothing, and a `*`-only prefix check behind it broke
  every `?` and `[...]` pattern. Log path tests must use real files (glob reads the disk)
- Scripts must be in configured scripts_directory with .ps1/.sh extension, and are
  requested by **bare filename only**. **The caller's string never reaches a shell**:
  a script is started as a file (shebang / `powershell -File`), and an allowlisted
  command runs the operator's allowlist entry, not the request. The gate is one
  copy in `internal/tasks/exec.go`; the platform files only say how to start a
  process. It was once duplicated per platform, and both copies approved
  `$(anything)/deploy.sh` and then ran it through `bash -c` -- do not split it again
- No WMI or external command execution for inventory (uses native APIs)
- Command execution uses context with timeout
- Secrets on disk (.creds, platform session, Nebula config cache) are written 0600
  through a temp file + rename. A Nebula config embeds the host private key inline,
  because Nebula's PKI requires it there — treat `config_yaml` as key material
- `nebula.sync_interval` is a security setting: Nebula has no CRL, so a revoked
  certificate is refused only once each peer re-reads its own config
- Never log HTTP response bodies from the platform: the credential read returns an nkey seed.
  Non-2xx bodies are safe (error documents) and are folded into errors on purpose

## Testing

Tests use `_test.go` suffix with platform-specific variants:
- `*_test.go` - Cross-platform tests
- `*_test_windows.go` - Windows-specific tests
- `test_helpers.go` - Shared test utilities

Run single test:
```bash
go test -v -run TestName ./internal/tasks/...
```

## Dependencies

Key dependencies (from go.mod):
- `github.com/nats-io/nats.go` - NATS client
- `github.com/go-co-op/gocron/v2` - Task scheduling
- `github.com/kardianos/service` - Cross-platform service management
- `github.com/spf13/viper` - Configuration
- `go.uber.org/zap` - Structured logging
- `github.com/shirou/gopsutil/v3` - Cross-platform system metrics (CPU, memory, disk)
- `github.com/slackhq/nebula` - Embedded overlay host. Sets the module's Go floor
  (v1.11 needs Go 1.26) and roughly half the binary size; pinned to the same
  version `pb-nebula` uses, so the library generating the configs and the one
  reading them cannot disagree
- `github.com/prometheus/common/expfmt` - Prometheus metrics parsing (exporter mode)
- `golang.org/x/sys` - Windows syscalls (registry, service control)
- `gopkg.in/natefinch/lumberjack.v2` - Log rotation

## Common Tasks

### Adding a new scheduled task
1. Add config struct in `internal/config/config.go`
2. Add default values in `internal/config/config.go:setDefaults()`
3. Add validation in `internal/config/config.go:validate()`
4. Implement task in `internal/tasks/`
5. Schedule in `internal/scheduler/scheduler.go:scheduleTasks()`

### Adding a new command handler
1. Define request/response structs in `internal/nats/handlers.go`
2. Implement handler method on `CommandHandlers`
3. Subscribe in `SubscribeAll()` with panic recovery

### Adding platform support
1. Create `*_<platform>.go` files with build tags
2. Update `GetPlatformDefaults()` in `internal/config/defaults.go`
3. Update `GetMetricNames()` in `internal/tasks/metrics_names.go`
