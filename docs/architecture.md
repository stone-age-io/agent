# Architecture Overview

Understanding the design and components of the agent platform.

## System Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    Control Plane                         │
│                                                          │
│  ┌──────────────────────────────────────────────────┐  │
│  │              PocketBase                           │  │
│  │  - User management                                │  │
│  │  - Tenant management                             │  │
│  │  - Device registration                           │  │
│  │  - Configuration management                      │  │
│  │  - NATS JWT generation (pb-nats)                │  │
│  └──────────────────────────────────────────────────┘  │
└──────────────────┬──────────────────────────────────────┘
                   │
                   │ REST API / Web UI
                   │
┌──────────────────▼──────────────────────────────────────┐
│                    Data Plane                            │
│                                                          │
│  ┌──────────────────────────────────────────────────┐  │
│  │                   NATS                            │  │
│  │  ┌────────────────────────────────────────────┐ │  │
│  │  │  Account: Tenant A                         │ │  │
│  │  │  - devices.*                               │ │  │
│  │  │  - agents.*                                │ │  │
│  │  └────────────────────────────────────────────┘ │  │
│  │  ┌────────────────────────────────────────────┐ │  │
│  │  │  Account: Tenant B                         │ │  │
│  │  │  - devices.*                               │ │  │
│  │  │  - agents.*                                │ │  │
│  │  └────────────────────────────────────────────┘ │  │
│  │                                                   │  │
│  │  + JetStream (durable telemetry storage)        │  │
│  └──────────────────────────────────────────────────┘  │
└──────────────────┬──────────────────────────────────────┘
                   │
                   │ NATS Protocol (4222)
                   │
┌──────────────────▼──────────────────────────────────────┐
│                      Edge                                │
│                                                          │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐    │
│  │   Agent     │  │   Agent     │  │   Agent     │    │
│  │  (Windows)  │  │   (Linux)   │  │  (FreeBSD)  │    │
│  │             │  │             │  │             │    │
│  │ - Metrics*  │  │ - Metrics*  │  │ - Metrics*  │    │
│  │ - Commands  │  │ - Commands  │  │ - Commands  │    │
│  │ - Services  │  │ - Services  │  │ - Services  │    │
│  └─────────────┘  └─────────────┘  └─────────────┘    │
│                                                          │
│  * Metrics: Built-in (gopsutil)                          │
└──────────────────────────────────────────────────────────┘
```

---

## Design Philosophy

### "Grug Brained Developer" Principles

1. **Simple over Clever**
   - Explicit code over abstractions
   - Boring solutions over novel ones
   - Clear over terse

2. **Separation of Concerns**
   - Agent = dumb executor
   - Business logic = scripts
   - Orchestration = control plane

3. **Do One Thing Well**
   - Agent collects metrics and executes commands
   - Doesn't parse, analyze, or store data
   - Minimal dependencies

---

## Component Deep Dive

### Agent (Edge)

**Purpose**: Lightweight executor on target systems

**Responsibilities:**
- Collect system metrics (built-in gopsutil)
- Execute whitelisted commands/scripts
- Control system services
- Report health and inventory
- Publish telemetry to NATS

**Optionally, on a site that is also a gateway** (see [Leaf Nodes](./leaf-node.md)):
- Bootstrap and host the site's NATS **leaf node**
- Relay declared KV buckets up to the hub, mirror declared buckets down
- Serve `/ready` and `/metrics` **locally**, on the box
- Run a Nebula overlay host in-process

None of those is a mode. There is no `edge.enabled` key and no gateway flag on
the platform either — a gateway is a Thing whose `thing_type` says so and whose
config turns more of these on. A single flag naming the role would be a second
control that can disagree with the first.

**What it does NOT do:**
- Parse or analyze metrics (just forwards)
- Store historical data
- Make decisions (stateless)
- Expose HTTP endpoints, **except** `/ready` and `/metrics` when
  `observability.addr` is set, and that defaults to loopback. It is opt-in for
  exactly this reason: opening a port on an appliance should be a decision. The
  justification for having it at all is that `cmd.health` travels over NATS,
  which is the link that breaks — the box you most need to ask is the one whose
  uplink is down, and that is when it goes quiet

**Technology:**
- **Language**: Go 1.26+ (Nebula sets the floor)
- **Service Management**: kardianos/service (cross-platform)
- **Messaging**: NATS Core + JetStream
- **Metrics Collection**: gopsutil
- **Exposition**: prometheus/client_golang (already linked by Nebula, so free)
- **Embedded server**: nats-server, linked in whether or not it is configured —
  so a scanner flagging a nats-server CVE here is reporting code that does not
  run unless `nats.server_config` is set
- **Logging**: zap (structured logging)

---

### NATS (Data Plane)

**Purpose**: Message bus with tenant isolation

**Key Features:**

1. **Multi-Tenancy via Accounts**
   ```
   Account: tenant-abc
     └─ Subject Namespace: agents.*, devices.*
     
   Account: tenant-xyz  
     └─ Subject Namespace: agents.*, devices.*
   ```
   
   Tenants cannot see each other's messages (cryptographically isolated).

2. **Communication Patterns**
   
   **Commands** (Core NATS Request/Reply):
   ```
   Request:  agents.device-123.cmd.ping
   Response: {"status":"pong","ts":"..."}
   ```
   - Synchronous
   - Ephemeral (no storage)
   - Fast (<10ms typical)

   **Telemetry** (JetStream Publish):
   ```
   Publish: agents.device-123.telemetry.system
   Payload: {"code":"device-123","location":"hq","cpu_usage_percent":15.2,"memory_free_gb":8.5,"memory_total_gb":16.0,"memory_used_percent":46.88,...,"ts":"..."}
   ```
   - Asynchronous
   - Durable (stored in JetStream)
   - Fire-and-forget
   - Self-describing: every payload carries `code`, `location`, and `ts`

   **Heartbeat** (Core NATS Publish):
   ```
   Publish: agents.device-123.heartbeat
   Payload: {"code":"device-123","location":"hq","ts":"..."}
   ```
   - Last-write-wins liveness beacon
   - Deliberately outside JetStream: a missed beat is the signal, so
     durability/replay of stale beats would be harmful
   - The JetStream stream must bind `agents.*.telemetry.>` (not `agents.>`)
     so heartbeats stay out of the stream by subject construction

3. **Subject Structure**
   ```
   agents.<code>.cmd.<command>
   agents.<code>.telemetry.<type>
   agents.<code>.heartbeat
   ```

**Technology:**
- **NATS Server**: Core + JetStream
- **Authentication**: JWT (issued by pb-nats)
- **Transport**: TCP with TLS support

---

### PocketBase (Control Plane)

**Purpose**: Configuration and orchestration

**Responsibilities:**
- User authentication and authorization
- Tenant/organization management
- Device registration and credential issuance
- Configuration storage and distribution
- Rule-based message routing (via rule-router)

**Integration Points:**
- **pb-nats**: Dynamic NATS JWT generation tied to PocketBase users/tenants
- **pb-tenancy**: Multi-tenant organization hierarchy
- **rule-router**: Routes NATS messages based on rules stored in PocketBase

**Technology:**
- **Framework**: PocketBase (Go)
- **Database**: SQLite (embedded)
- **API**: REST + Realtime subscriptions

---

## Message Flow Examples

### 0. Credential Lifecycle (auth type: "platform")

The agent is a Thing on the stone-age.io platform. It authenticates as itself;
the auth response (with `expand`) carries everything bootstrap needs in one call.

```
┌─────────┐
│  Agent  │ First startup
└────┬────┘
     │ 1. Check if .creds file exists → skip to sync if yes
     │
     │ 2. Read the thing's password from env var (AGENT_PLATFORM_PASSWORD)
     ▼
┌────────────┐
│  Platform  │ 3. POST /api/collections/things/auth-with-password
│            │         ?expand=nats_user,location
└────┬───────┘    → Returns the thing record with expanded relations
     │
     ▼
┌─────────┐
│  Agent  │ 4. Verify record.code == config code (fail on mismatch)
└────┬────┘ 5. Read creds from record.expand.nats_user.creds_file
     │      6. Write .creds file (permissions: 0600)
     │      7. Store session token + credential revision (0600)
     │      8. Connect to NATS using .creds
     ▼
┌─────────┐
│  NATS   │ Normal operation begins
└─────────┘
```

Access rules on the platform scope everything to the authenticated thing: it can
see only its own record and only its assigned NATS user, so no device can read
another device's credentials.

**Upkeep** runs once at startup and then on `platform.sync_interval` (24h by
default). It deliberately keeps key material off the wire unless it changed:

```
┌─────────┐
│  Agent  │ Every 24 hours
└────┬────┘
     │ 1. POST /api/collections/things/auth-refresh    (NO expand)
     │    → fresh session token, no credential in the response
     │
     │ 2. GET /api/collections/nats_users/records/{id}?fields=updated
     │    → just a timestamp
     ▼
  revision unchanged? ──yes──► done. Nothing else is requested.
     │
     no
     ▼
     │ 3. GET the full record → new creds_file
     │ 4. Write .creds (atomic replace), force NATS reconnect
     ▼
┌─────────┐
│  NATS   │ Reconnects with the new credential, subscriptions intact
└─────────┘
```

A `.creds` file embeds the nkey seed, so the split matters: `?fields=` is not
applied to PocketBase auth responses, which means any auth call with
`expand=nats_user` returns the whole credential whether or not it is needed.

Because this path never touches NATS, it is also the recovery path for a device
whose credential was revoked — the agent exits, the service manager restarts it,
and the startup sync heals it.

**Rotation** (`cmd.rotate_creds`, or the platform's Regenerate button) posts to
`/api/me/nats-creds/rotate`, then reads and installs the re-minted credential.
pb-nats re-mints inside the record-update model hook, before the save commits, so
the new credential is readable immediately — no polling. Rotation is not
revocation: the old credential stays valid until it expires or is revoked.

After the first boot the password is optional: the stored session token is
renewed by every sync. See **[Platform Credentials](credentials.md)**.

---

### 1. Metrics Collection (Telemetry)

```
┌─────────┐
│  Agent  │ Every 5 minutes
└────┬────┘
     │ 1. Collect metrics via:
     │    - Built-in (gopsutil)
     ▼
┌─────────┐
│  Agent  │ 2. Publish to JetStream
└────┬────┘      agents.device-123.telemetry.system
     │            {"code":"device-123","location":"hq","cpu_usage_percent":15.2,...,"ts":"..."}
     ▼
┌─────────┐
│  NATS   │ 3. Store in JetStream stream
└────┬────┘
     │ 4. Consumers can subscribe
     ▼
┌──────────────┐
│ Dashboard /  │ Real-time display
│ Rule Router  │ Alert on thresholds
└──────────────┘
```

### 2. Service Control (Command)

```
┌──────────┐
│Dashboard │ User clicks "Restart nginx"
└────┬─────┘
     │ 1. POST /api/devices/123/command
     ▼
┌────────────┐
│ PocketBase │ 2. Validate permissions
└────┬───────┘
     │ 3. NATS request
     │    agents.device-123.cmd.service
     │    {"action":"restart","service_name":"nginx"}
     ▼
┌─────────┐
│  NATS   │ 4. Route to device
└────┬────┘
     │ 5. Deliver message
     ▼
┌─────────┐
│  Agent  │ 6. Validate whitelist
└────┬────┘    7. Execute: systemctl restart nginx
     │ 8. Return response
     ▼         {"status":"success","output":"..."}
┌─────────┐
│  NATS   │ 9. Reply back
└────┬────┘
     │
     ▼
┌──────────┐
│Dashboard │ 10. Display result to user
└──────────┘
```

### 3. Script Execution (Command)

```
┌──────────┐
│Dashboard │ User runs custom script
└────┬─────┘
     │ 1. NATS request
     │    agents.device-123.cmd.exec
     │    {"command":"Get-DiskSpace.ps1"}
     ▼
┌─────────┐
│  Agent  │ 2. Validate script exists
└────┬────┘    3. Check whitelist/scripts_directory
     │ 4. Execute PowerShell script
     │    C:\ProgramData\Agent\Scripts\Get-DiskSpace.ps1
     ▼
┌────────────┐
│PowerShell/ │ Script returns JSON
│   Bash     │
└────┬───────┘
     │ 5. Capture output
     ▼
┌─────────┐
│  Agent  │ 6. Return via NATS
└────┬────┘    {"status":"success","output":{...},"exit_code":0}
     │
     ▼
┌──────────┐
│Dashboard │ 7. Parse JSON and display
└──────────┘
```

---

## Security Model

### 1. Authentication & Authorization

**NATS Level:**
- JWT-based authentication (issued by pb-nats)
- Account isolation (tenant cannot access another tenant's subjects)
- Subject-based permissions

**Platform Credentials (platform auth):**
- Credentials fetched over HTTPS; a plain http:// platform URL is refused unless explicitly allowed for development
- Password read from an environment variable (never in config files), and optional once the agent holds a session token
- `.creds` and the session file written with owner-only permissions (0600), replaced atomically
- Routine syncs transfer no key material: the token refresh omits `expand` and the drift check asks only for `updated`
- Response bodies are never logged — on the credential read, one of them is a private key

**Agent Level:**
- Whitelists for services, commands, log paths
- Exact match required (no wildcards in security checks)
- Path traversal protection

### 2. Data Flow Security

**In Transit:**
- TLS for NATS connections (optional but recommended)
- No inbound management API: the agent can only be instructed over its own
  authenticated NATS connection, which it dials outbound
- Outbound HTTPS to the Control Plane when the `platform:` block is set
  (credentials, Nebula config, leaf bootstrap)
- `/ready` and `/metrics` are served over plain HTTP on `observability.addr`,
  `127.0.0.1:9100` by default. Loopback is not a permission boundary if other
  users share the box -- set `observability.metrics_token`, or set `addr` empty,
  before moving it off loopback
- A gateway's embedded `nats-server` listens per its own config, which is the
  one case where devices connect *in*

**At Rest:**
- Credentials stored with restricted permissions
- Configuration files readable only by agent user
- Logs rotated and size-limited

### 3. Principle of Least Privilege

**Agent runs as:**
- Windows: LocalService (no interactive logon)
- Linux: root (required for service control) *
- FreeBSD: root (required for service control) *

\* Future: Consider running as non-privileged user with sudo whitelist

---

## Scalability

### Horizontal Scaling

**NATS Cluster:**
```
┌─────────┐   ┌─────────┐   ┌─────────┐
│ NATS-1  │◄──┤ NATS-2  ├──►│ NATS-3  │
└─────────┘   └─────────┘   └─────────┘
     ▲             ▲             ▲
     │             │             │
   Agents       Agents        Agents
```

- 3-5 node NATS cluster for high availability
- Agents connect to any node (automatic failover)
- JetStream replication for durability

**Agent Distribution:**
- Each agent is independent (no coordination)
- 1 agent per managed system
- Tested: 10,000+ agents per NATS cluster

### Vertical Scaling

**Agent Resource Usage:**
- CPU: <1% typical, <5% during metrics scrape
- Memory: 30-50MB typical
- Network: ~1KB/minute telemetry (compressed)
- Disk: Minimal (logs only, with rotation)

**NATS Server:**
- CPU: Scales with message rate
- Memory: ~1-2MB per 10,000 subscriptions
- Tested: 100K+ messages/second per node

---

## Monitoring the Agent

### Self-Diagnostics

The agent exposes its own health via the `health` command:

```bash
nats request "agents.device-123.cmd.health" '{}'
```

**Response includes:**
```json
{
  "status": "healthy",
  "agent": {
    "version": "0.1.0",
    "uptime_seconds": 86400,
    "goroutines": 15,
    "memory_mb": 45.2
  },
  "nats": {
    "connected": true,
    "url": "nats://nats.example.com:4222",
    "reconnects": 2,
    "in_msgs": 150,
    "out_msgs": 720
  },
  "tasks": {
    "last_heartbeat": "2025-11-17T12:00:00Z",
    "last_metrics": "2025-11-17T11:55:00Z",
    "heartbeat_count": 1440,
    "metrics_count": 288,
    "metrics_failures": 0
  },
  "commands": {
    "processed": 42,
    "errored": 1
  },
  "os": {
    "platform": "linux",
    "name": "Ubuntu 24.04",
    "version": "24.04"
  },
  "config": {
    "code": "device-123",
    "location": "hq",
    "subject_prefix": "agents",
    "version": "0.3.1",
    "enabled_tasks": ["heartbeat", "system_metrics", "creds_sync"],
    "allowed_commands": ["df -h"],
    "allowed_services": ["nginx"],
    "allowed_log_paths": ["/var/log/*.log"]
  },
  "checks": {
    "ready": true,
    "state": "warn",
    "checked": "2025-11-17T12:00:03Z",
    "checks": [
      {"name": "hub_uplink", "state": "warn", "detail": "no outbound leaf connection to the hub",
       "fix": "This site is islanded: local NATS still works and devices keep running..."},
      {"name": "jetstream", "state": "ok", "detail": "available"},
      {"name": "nats", "state": "ok", "detail": "connected"},
      {"name": "nats_local", "state": "ok", "detail": "connected"},
      {"name": "nebula", "state": "skipped", "detail": "the overlay is not enabled"},
      {"name": "platform_sync", "state": "ok", "detail": "last sync 4m12s ago"},
      {"name": "sync", "state": "ok", "detail": "2 bucket(s) syncing"},
      {"name": "task_metrics", "state": "ok", "detail": "288 collected, 0 failed"}
    ]
  }
}
```

`checks` is the readiness report — the same one `/ready` serves, from the same
registry on the same probe schedule, so the two channels cannot disagree about
what is wrong with this agent. It can be up to one probe interval stale;
`checked` carries the timestamp.

**There is deliberately no `edge` block.** Everything a gateway knows
first-hand — is the local leaf up, is the hub uplink attached, is each declared
bucket syncing and why not — is a registered check and arrives here already.
Adding a fact to this response means registering a check, which also puts it on
`/ready` and in `agent_check_state`, rather than writing it into three places
and watching them drift.

Checks marked `skipped` do not apply to this agent — no overlay configured, no
leaf on the box, metrics disabled. Skipped ranks *below* `ok`: it means nothing
was examined, not that everything was fine.

The three allowlists are the three gates — `cmd.exec`, `cmd.service` and
`cmd.logs` each refuse anything not named in one of them. They are reported
here because "not allowed" is otherwise the same answer whether an entry is
missing or merely spelled differently, and checking meant shell access to the
box. They are configuration, not secrets: they list what an authenticated
caller was already permitted to do.

**Health Status** is a pure function of the worst check in `checks`:
- `healthy`: every check reported `ok`
- `degraded`: some check reported `warn` — it works and someone should look at
  it (an islanded site, a bucket that will not sync, a rolled-back overlay,
  JetStream unusable, a majority of metrics scrapes failing)
- `unhealthy`: some check reported `fail` — currently NATS being disconnected,
  and on a gateway the local leaf being unreachable

A warning does **not** make the agent unready: `/ready` still answers 200. The
two endpoints answer different questions — "may I route traffic here" and "is
anything wrong" — and collapsing them is how a green tick comes to mean
nothing.

---

## Deployment Patterns

### 1. Single Tenant (Simple)

```
PocketBase + NATS (single instance)
└─ Single NATS account
   └─ All agents in one namespace
```

**Use Case:** Small deployments, single organization

### 2. Multi-Tenant (MSP)

```
PocketBase + NATS Cluster
├─ Account: Customer A
│  └─ agents.* (isolated)
├─ Account: Customer B
│  └─ agents.* (isolated)
└─ Account: Customer C
   └─ agents.* (isolated)
```

**Use Case:** MSPs managing multiple customer environments

### 3. Hierarchical (Enterprise)

```
PocketBase (regional)
├─ NATS Cluster US-East
│  ├─ Account: Prod
│  └─ Account: Staging
├─ NATS Cluster US-West
│  ├─ Account: Prod
│  └─ Account: Staging
└─ NATS Cluster EU
   ├─ Account: Prod
   └─ Account: Staging
```

**Use Case:** Global enterprises with regional compliance requirements

---

## Extension Points

### 1. Custom Scripts

Extend agent functionality without modifying code:

```
scripts/
├── monitoring/
│   ├── check-database.ps1
│   └── check-api-health.sh
├── maintenance/
│   ├── cleanup-temp.ps1
│   └── rotate-logs.sh
└── inventory/
    ├── scan-software.ps1
    └── check-licenses.sh
```

### 2. NATS Consumers

Process telemetry without agent changes:

```go
// Custom consumer example
js, _ := nc.JetStream()
sub, _ := js.Subscribe("agents.*.telemetry.system", func(msg *nats.Msg) {
    // Parse metrics
    // Store in database
    // Check thresholds
    // Send alerts
})
```

### 3. Rule Router Integration

Route messages based on content:

```yaml
# PocketBase rule
- match: "cpu_percent > 90"
  action: 
    - publish: "alerts.high-cpu"
    - webhook: "https://alerts.example.com/cpu"
```

---

## Future Enhancements

**Planned Features:**
1. **Interactive Sessions**: PowerShell/Bash REPL over NATS
2. **File Transfer**: Upload/download files securely
3. **Plugin System**: WebAssembly or Lua for safe extensibility
4. **Mobile Apps**: NATS clients for iOS/Android

**Not Planned:**
- Built-in metric analysis (use external tools)
- Persistent local storage (stateless by design)
- HTTP endpoints (NATS-only philosophy)
- Rich UI in agent (separation of concerns)

---

## Related Documentation

- **[Script Development](script-development.md)** - Write custom scripts

---

**Questions?** Open a discussion on [GitHub](https://github.com/stone-age-io/agent/discussions)
