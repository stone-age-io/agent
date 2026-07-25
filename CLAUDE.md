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
make build-all VERSION=1.0.0

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
│   │   └── session.go         # Session file (auth token + credential revision)
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
├── docs/                      # Platform installation guides
├── Makefile                   # Build automation
└── go.mod                     # Go 1.24+ required
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
   - Session file (token + credential revision, 0600) makes `password_env` optional after
     first boot. The platform's thing token TTL is 7 days, renewed by each sync
   - Never logs response bodies — one of them is a private key

4. **NATS Client** (`internal/nats/client.go`):
   - JetStream validation on connect (fail-fast)
   - TLS 1.2+ support with optional mTLS
   - Async publishing with automatic retries
   - `creds` and `stone-age` auth both connect with the .creds file; `UserCredentials`
     re-reads it on every reconnect, so `ForceReconnect()` adopts a replaced credential

5. **Scheduler** (`internal/scheduler/scheduler.go`):
   - Uses gocron/v2 for interval-based scheduling
   - Context-aware cancellation for clean shutdown
   - Panic recovery for all tasks
   - `creds_sync` task is scheduled only when a CredsSyncer is supplied (stone-age auth)

6. **Executor** (`internal/tasks/executor.go`):
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
- `{prefix}.{code}.cmd.health` - Agent health check (includes agent version)
- `{prefix}.{code}.cmd.rotate_creds` - Re-mint this agent's NATS credential on the platform, then reconnect (stone-age auth only; answers with an error otherwise)

Command responses use `ts` (RFC3339 UTC) for their timestamp field.

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
- Secrets on disk (.creds, platform session) are written 0600 through a temp file + rename
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
