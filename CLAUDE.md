# CLAUDE.MD - Agent Project Guide

## Project Overview

A lightweight, NATS-native system management and observability agent for Windows, Linux, and FreeBSD. The agent provides remote management and monitoring capabilities through secure NATS messaging.

**Key Design Principles:**
- Lightweight: <50MB RAM, <1% CPU target
- Secure: TLS support, whitelist-based execution, no exposed HTTP endpoints
- NATS-Native: All communication via NATS (JetStream for telemetry, Core NATS for commands)
- Cross-Platform: Windows, Linux, FreeBSD support with platform-specific implementations

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
├── cmd/agent/main.go          # Entry point, service management
├── internal/
│   ├── agent/agent.go         # Core agent orchestration
│   ├── config/                # Configuration loading & validation
│   │   ├── config.go          # Config structs and Load()
│   │   └── defaults.go        # Platform-specific defaults
│   ├── platform/              # stone-age.io platform credential lifecycle
│   │   ├── platform.go        # EnsureCredentials, Sync, Rotate
│   │   ├── nebula.go          # NebulaSource: this thing's nebula_host config
│   │   └── session.go         # Session file (auth token + both revisions)
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
├── docs/                      # Install guides, credentials, Nebula (+ nebula-design.md, a design record not a guide)
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
   - Supports auth types: creds, token, userpass, stone-age, none
   - Requires https for the platform URL unless `allow_insecure_url` is set
   - Platform-specific defaults for paths and exporter URLs

3. **Platform** (`internal/platform/`): credential lifecycle for auth type `stone-age`
   - The agent is a Thing: authenticates as itself against the `things` auth collection;
     its credential lives on the related nats_user record's `creds_file` field
   - `EnsureCredentials()` — first boot only: password auth with
     `expand=nats_user,location`, writes .creds (0600, atomic). Skips if the file exists;
     restores it via the stored session (no password) if the file is gone but a token remains
   - `Sync()` — startup + `tasks.creds_sync.interval`: refreshes the session token
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
   - `creds` and `stone-age` auth both connect with the .creds file; `UserCredentials`
     re-reads it on every reconnect, so `ForceReconnect()` adopts a replaced credential

6. **Scheduler** (`internal/scheduler/scheduler.go`):
   - Uses gocron/v2 for interval-based scheduling
   - Context-aware cancellation for clean shutdown
   - Panic recovery for all tasks
   - `creds_sync` task is scheduled only when a CredsSyncer is supplied (stone-age auth)
   - `nebula_sync` task is scheduled only when a NebulaSyncer is supplied (overlay enabled)

7. **Executor** (`internal/tasks/executor.go`):
   - Central task execution with stats tracking
   - Configurable metrics collection via MetricsCollector interface
   - Supports builtin (gopsutil) or exporter (Prometheus) sources
   - Command success/error recording

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
- `{prefix}.{code}.cmd.health` - Agent health check (includes agent version, and the `nebula` block when the overlay is enabled)
- `{prefix}.{code}.cmd.rotate_creds` - Re-mint this agent's NATS credential on the platform, then reconnect (stone-age auth only; answers with an error otherwise)
- `{prefix}.{code}.cmd.nebula` - Overlay actions: `sync` (pull and apply now) or `restart` (bounce Nebula on the running config). Enabled agents only; answers with an error otherwise

Command responses use `ts` (RFC3339 UTC) for their timestamp field.

**`cmd.nebula` is the one command that answers before it acts.** It replies
`accepted` and does the work asynchronously, because both actions can interrupt
the tunnel the request arrived through when NATS rides the overlay — a reply sent
afterwards would never land, and the caller would see a timeout on an operation
that succeeded. The outcome is read from `cmd.health`. Anything spawned this way
needs its own `recover()`: `handleWithRecovery` wraps the handler, not its
goroutines.

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
nats:
  urls: ["nats://host:4222"]     # NATS server URLs
  auth:
    type: "creds"                # creds, token, userpass, stone-age, none
    creds_file: "/path/to/creds"
    stone-age:                   # Only for stone-age auth type (platform credentials)
      url: "https://platform.example.com"
      identity: "thing@example.com"     # the thing's login email
      password_env: "AGENT_PLATFORM_PASSWORD"  # optional after first boot
      session_file: "/path/to/platform-session.json"  # default: beside creds_file
      allow_insecure_url: false  # http:// platform URL, development only
  tls:
    enabled: true
    ca_file: "/path/to/ca.pem"
nebula:                          # Embedded overlay host, off by default
  enabled: false
  source: "platform"             # "platform" (needs stone-age auth) or "file"
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
  creds_sync:                    # Only runs with stone-age auth
    enabled: true
    interval: "24h"              # 1h-72h range (under the platform's 7d token TTL)
commands:
  scripts_directory: "/path/to/scripts"
  allowed_services: ["nginx"]
  allowed_commands: ["df -h"]
  timeout: "30s"                 # 5s-5m range
```

## Security Notes

- All commands/services must be whitelisted in config
- Log path access restricted to allowed patterns with path traversal protection
- Scripts must be in configured scripts_directory with .ps1/.sh extension
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
