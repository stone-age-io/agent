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
│  │ - Metrics   │  │ - Metrics   │  │ - Metrics   │    │
│  │ - Commands  │  │ - Commands  │  │ - Commands  │    │
│  │ - Services  │  │ - Services  │  │ - Services  │    │
│  └─────────────┘  └─────────────┘  └─────────────┘    │
│                                                          │
│  ┌─────────────┐  ┌─────────────┐                      │
│  │  windows_   │  │   node_     │                      │
│  │  exporter   │  │  exporter   │                      │
│  └─────────────┘  └─────────────┘                      │
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
- Collect system metrics from Prometheus exporters
- Execute whitelisted commands/scripts
- Control system services
- Report health and inventory
- Publish telemetry to NATS

**What it does NOT do:**
- Parse or analyze metrics (just forwards)
- Store historical data
- Make decisions (stateless)
- Expose HTTP endpoints

**Technology:**
- **Language**: Go 1.24+
- **Service Management**: kardianos/service (cross-platform)
- **Messaging**: NATS Core + JetStream
- **Metrics Parsing**: Prometheus expfmt
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
   Response: {"status":"pong","timestamp":"..."}
   ```
   - Synchronous
   - Ephemeral (no storage)
   - Fast (<10ms typical)

   **Telemetry** (JetStream Publish):
   ```
   Publish: agents.device-123.telemetry.system
   Payload: {"cpu_percent":15.2,"memory_free_gb":8.5,...}
   ```
   - Asynchronous
   - Durable (stored in JetStream)
   - Fire-and-forget

3. **Subject Structure**
   ```
   agents.<device-id>.cmd.<command>
   agents.<device-id>.telemetry.<type>
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

### 1. Metrics Collection (Telemetry)

```
┌─────────┐
│  Agent  │ Every 5 minutes
└────┬────┘
     │ 1. Scrape http://localhost:9182/metrics
     ▼
┌────────────────┐
│ windows_       │ Returns Prometheus metrics
│ exporter       │
└────┬───────────┘
     │ 2. Parse metrics
     ▼
┌─────────┐
│  Agent  │ 3. Publish to JetStream
└────┬────┘      agents.device-123.telemetry.system
     │            {"cpu_percent":15.2,...}
     ▼
┌─────────┐
│  NATS   │ 4. Store in JetStream stream
└────┬────┘
     │ 5. Consumers can subscribe
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
└────┬────┘    {"status":"success","output":"{...}","exit_code":0}
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

**Agent Level:**
- Whitelists for services, commands, log paths
- Exact match required (no wildcards in security checks)
- Path traversal protection

### 2. Data Flow Security

**In Transit:**
- TLS for NATS connections (optional but recommended)
- No HTTP endpoints exposed by agent
- All communication via encrypted NATS

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
    "version": "1.0.0",
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
  }
}
```

**Health Status:**
- `healthy`: All systems operational
- `degraded`: Some issues (>50% metrics failures, >10 reconnects)
- `unhealthy`: NATS disconnected

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

- **[Configuration Reference](configuration.md)** - All configuration options
- **[Script Development](script-development.md)** - Write custom scripts
- **[Security Best Practices](security.md)** - Hardening guide
- **[Troubleshooting](troubleshooting.md)** - Common issues

---

**Questions?** Open a discussion on [GitHub](https://github.com/stone-age-io/agent/discussions)
