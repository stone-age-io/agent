# Agent

A lightweight, NATS-native management and observability agent for **Windows**, **Linux**, and **FreeBSD**.

---

## Overview

Agent is a purpose-built system management tool that provides remote management and observability for server infrastructure through a secure, lightweight agent.

**Key Principles:**
- **Lightweight**: <50MB RAM, <1% CPU usage
- **Secure**: TLS for NATS, allowlisted services/commands/log paths, and no inbound management API — nothing can tell this agent to do anything except over its own authenticated NATS connection
- **Simple**: Do one thing well
- **Extensible**: PowerShell/Bash scripts for custom functionality
- **NATS-Native**: management and telemetry are NATS, always dialed outbound

### What listens, and what dials out

Worth being exact, because it decides your firewall rules:

| | Direction | When |
|---|---|---|
| NATS (telemetry, commands, heartbeats) | **outbound** to your hub | always |
| Platform HTTPS (credentials, Nebula config, leaf bootstrap) | **outbound** to the Control Plane | when the `platform:` block is set |
| `/ready` + `/metrics` | **listens**, `127.0.0.1:9100` by default | unless `observability.addr` is empty |
| Nebula overlay (UDP) | **outbound** to a lighthouse | when `nebula.enabled` |
| Embedded `nats-server` | **listens**, per its own config | only when `nats.server_config` is set |

The first two never need an inbound rule. The readiness listener is on loopback
unless you move it. A site gateway is the exception by design: local devices have
to reach the leaf server it hosts.

---

## Platform Support

| Platform | Service Manager | Metrics Source | Status |
|----------|----------------|----------------|--------|
| **Windows Server 2016+** | Windows Service | Built-in (default) or [windows_exporter](https://github.com/prometheus-community/windows_exporter) | ✅ Stable |
| **Windows 10/11** | Windows Service | Built-in (default) or [windows_exporter](https://github.com/prometheus-community/windows_exporter) | ✅ Stable |
| **Ubuntu 22.04+** | systemd | Built-in (default) or [node_exporter](https://github.com/prometheus/node_exporter) | ✅ Stable |
| **Debian 11+** | systemd | Built-in (default) or [node_exporter](https://github.com/prometheus/node_exporter) | ✅ Stable |
| **FreeBSD 13+** | rc.d | Built-in (default) or [node_exporter](https://github.com/prometheus/node_exporter) | ✅ Stable |

---

## Features

### Core Capabilities
- **System Metrics**: CPU, memory, disk usage and I/O
- **Service Management**: Start, stop, restart system services
- **Service Monitoring**: Track service status and health
- **Command Execution**: Run whitelisted scripts securely
- **Log Retrieval**: Fetch log files on-demand
- **System Inventory**: Hardware and OS information
- **Health Monitoring**: Agent self-diagnostics

### Communication
- **Telemetry Publishing**: JetStream for durable metrics, service status, and inventory
- **Heartbeats**: Core NATS liveness beacons (last-write-wins, no replay)
- **Command Handling**: Core NATS request/reply
- **Multi-Tenant**: NATS account isolation
- **TLS Support**: Encrypted communication

### Provisioning
- **Platform Credentials**: Auto-fetch NATS credentials on first start, then renew and rotate them without redistribution
- **Manual Credentials**: Pre-distribute `.creds` files
- **Token / UserPass**: Simple auth for development

### Overlay networking (optional)
- **Nebula mesh host**, run in-process rather than as a separate service. The config is re-read on an interval, which is what makes revocation, renewal and CA rotation actually reach the device -- Nebula has no CRL, so `nebula.sync_interval` **is** this device's revocation latency. A newly applied config that cannot reach a lighthouse is rolled back to the last one known to work.

### Local health (on by default)
- **`/ready` and `/metrics`** on `127.0.0.1:9100`. This is not a gateway feature: any agent can say whether it is healthy, and `cmd.health` travels over NATS, which is the link that breaks. The box you most need to ask is the one whose uplink is down, and that is exactly when it goes quiet over the bus.

### Site gateway (all optional)
- **NATS leaf node**: bootstrap a site's `nats-leaf.conf` from the platform (`agent -leaf-config`) and, if you want, host that server in this process
- **KV sync**: relay declared buckets up to the hub and mirror declared buckets down, so the site keeps working through a WAN outage

None of these is a mode you switch on. There is no `edge.enabled` key and no
gateway flag on the platform either -- a "gateway" is just an agent with more of
these keys set, and a Thing whose type says so. See
**[Leaf Nodes](docs/leaf-node.md)**.

---

## Quick Start

Choose your platform:

<details>
<summary><b>Windows</b></summary>

```powershell
# 1. Download and extract the release archive
$version = "0.1.0"
Invoke-WebRequest -Uri "https://github.com/stone-age-io/agent/releases/download/v$version/agent_${version}_windows_amd64.zip" -OutFile "$env:TEMP\agent.zip"
Expand-Archive -Path "$env:TEMP\agent.zip" -DestinationPath "$env:TEMP\agent" -Force

# 2. Install (the archive carries the per-OS example configs under configs\)
New-Item -ItemType Directory -Force -Path "C:\Program Files\Agent"
New-Item -ItemType Directory -Force -Path "C:\ProgramData\Agent"
Copy-Item "$env:TEMP\agent\agent.exe" "C:\Program Files\Agent\"
Copy-Item "$env:TEMP\agent\configs\windows\config.yaml.example" "C:\ProgramData\Agent\config.yaml"

# 3. Configure
notepad "C:\ProgramData\Agent\config.yaml"

# 4. Install as service
cd "C:\Program Files\Agent"
.\agent.exe -service install

# 5. Start service
Start-Service agent
```

> **Note**: By default, the agent uses built-in metrics collection. Optionally install [windows_exporter](https://github.com/prometheus-community/windows_exporter) for additional metrics and set `source: "exporter"` in config.

**[Detailed Windows Guide →](docs/windows.md)**

</details>

<details>
<summary><b>Linux</b></summary>

```bash
# 1. Install agent
VERSION=0.1.0
wget https://github.com/stone-age-io/agent/releases/download/v${VERSION}/agent_${VERSION}_linux_amd64.tar.gz
tar xzf agent_${VERSION}_linux_amd64.tar.gz
sudo mv agent /usr/local/bin/agent
sudo chmod +x /usr/local/bin/agent

# 2. Configure (the example config ships in the archive)
sudo mkdir -p /etc/agent
sudo cp configs/linux/config.yaml.example /etc/agent/config.yaml
sudo nano /etc/agent/config.yaml

# 3. Install as service
sudo /usr/local/bin/agent -service install

# 4. Start service
sudo systemctl start agent
```

> **Note**: By default, the agent uses built-in metrics collection. Optionally install [node_exporter](https://github.com/prometheus/node_exporter) for additional metrics and set `source: "exporter"` in config.

**[Detailed Linux Guide →](docs/linux.md)**

</details>

<details>
<summary><b>FreeBSD</b></summary>

```bash
# 1. Install agent
VERSION=0.1.0
fetch https://github.com/stone-age-io/agent/releases/download/v${VERSION}/agent_${VERSION}_freebsd_amd64.tar.gz
tar xzf agent_${VERSION}_freebsd_amd64.tar.gz
sudo mv agent /usr/local/bin/agent
sudo chmod +x /usr/local/bin/agent

# 2. Configure (the example config ships in the archive)
sudo mkdir -p /usr/local/etc/agent
sudo cp configs/freebsd/config.yaml.example /usr/local/etc/agent/config.yaml
sudo ee /usr/local/etc/agent/config.yaml

# 3. Install as service
sudo /usr/local/bin/agent -service install

# 4. Start service
sudo service agent start
```

> **Note**: By default, the agent uses built-in metrics collection. Optionally install [node_exporter](https://github.com/prometheus/node_exporter) (`pkg install node_exporter`) for additional metrics and set `source: "exporter"` in config.

**[Detailed FreeBSD Guide →](docs/freebsd.md)**

</details>

---

## Architecture

```
┌──────────────────┐
│   PocketBase     │  Control Plane (users, tenants, devices, config)
└────────┬─────────┘
         │
┌────────▼─────────┐
│      NATS        │  Data Plane (messaging, telemetry)
│   + JetStream    │  - Tenant isolation via accounts
└────────┬─────────┘  - Durable telemetry storage
         │
    ┌────▼─────┐
    │  Agent   │      Edge (Windows/Linux/FreeBSD)
    └──────────┘      - Built-in metrics (gopsutil) or exporter
                      - Command execution
                      - Service control
```

**Design Philosophy:**
- **Control Plane** (PocketBase): Manages configuration and orchestration
- **Data Plane** (NATS): All agent communication, tenant-isolated
- **Edge** (Agent): Lightweight executor on target systems

**[Architecture Details →](docs/architecture.md)**

---

## Configuration Example

```yaml
# Agent Identity
code: "server-prod-01"    # Identity token used in NATS subjects (legacy key: device_id)
location: "hq"            # Optional deployment location, carried in telemetry payloads

# stone-age.io platform (optional). One home for the platform relationship:
# the NATS credential lifecycle, the Nebula config source and the leaf
# bootstrap all read it. Leave the block out and none of them are available.
# platform:
#   url: "https://platform.example.com"
#   identity: "thing@example.com"              # the thing's login email
#   password_env: "AGENT_PLATFORM_PASSWORD"
#   sync_interval: "24h"                       # credential refresh, 1h-72h

# NATS Connection
nats:
  urls: ["nats://nats.example.com:4222"]

  # Host a nats-server in this process (optional). On a gateway that is the file
  # `agent -leaf-config` wrote, but it loads any nats-server config, so this is
  # equally how you run a plain embedded broker on a box with no platform.
  # nats.urls must name the port it listens on -- startup refuses a disagreement.
  # Leave it unset where systemd or Docker already supervises one: the bus then
  # survives an agent restart, which is what you want on a live site.
  # server_config: "/etc/agent/nats-leaf.conf"

  auth:
    # Option 1: Credentials file (pre-distributed)
    type: "creds"
    creds_file: "/path/to/device.creds"

    # Option 2: stone-age.io platform (fetches and maintains .creds).
    # The agent is a Thing on the platform: it logs in as itself and its
    # credential lives on its nats_user relation. Configure the platform
    # itself in the top-level `platform:` block above.
    # type: "platform"
    # creds_file: "/etc/agent/device.creds"

# Scheduled Tasks
tasks:
  heartbeat:
    enabled: true
    interval: "1m"

  system_metrics:
    enabled: true
    interval: "5m"
    source: "builtin"  # "builtin" (default) or "exporter"
    # exporter_url: "http://localhost:9182/metrics"  # Only for exporter mode

  service_check:
    enabled: true
    services:
      - "nginx"
      - "postgresql"

# KV bucket sync between this leaf's JetStream domain and the hub.
# Optional, off by default: it moves data-plane traffic. Requires platform auth.
# sync:
#   twin: true                 # preset: the two digital-twin buckets
#   mirrors:                   # hub -> edge, maintained by the server
#     - bucket: "recipes"
#       keys: "line-a.>"       # optional; CANNOT be changed after creation
#   relays:                    # edge -> hub, pumped by the agent
#     - bucket: "events"
#       keys: "site.S01.>"     # optional. A bucket belongs to ONE list.

# /ready and /metrics on this box. ON by default, on loopback -- set addr to ""
# to serve neither. The checks still run and still log either way.
#
# NOTE on Linux and FreeBSD: 9100 is also node_exporter's default port. If you
# run the exporter on this box, move one of them.
observability:
  addr: "127.0.0.1:9100"

# Nebula overlay (optional, off by default). sync_interval is a security number,
# not a tuning knob: Nebula has no CRL, so it is this device's revocation
# latency. See docs/nebula.md.
# nebula:
#   enabled: true
#   source: "platform"        # or "file", with config_file, for no-platform use
#   sync_interval: "10m"

# Command Execution
commands:
  scripts_directory: "/opt/agent/scripts"
  allowed_services:
    - "nginx"
  allowed_commands:
    - "df -h"
```


## Use Cases

### Managed Service Providers (MSPs)
- Manage 100s of customer servers from a unified platform
- Multi-tenant isolation via NATS accounts
- Self-hosted alternative to expensive RMM tools

### Enterprise IT
- Monitor and manage internal infrastructure
- Meet compliance requirements (data never leaves premises)
- Integrate with existing observability stack

### VARs & System Integrators
- Build custom management platforms for vertical markets
- White-label and embed in your solutions
- Extensible via scripts for industry-specific needs

---

## Documentation

### Getting Started
- **[Linux Installation](docs/linux.md)** - Ubuntu, Debian, systemd-based distros
- **[FreeBSD Installation](docs/freebsd.md)** - FreeBSD 13+, rc.d setup
- **[Windows Installation](docs/windows.md)** - Windows Server, Windows 10/11

### Advanced Topics
- **[Architecture Overview](docs/architecture.md)** - System design and components
- **[Platform Credentials](docs/credentials.md)** - Provisioning, renewing, and rotating credentials from the stone-age.io platform
- **[Leaf Nodes](docs/leaf-node.md)** - Run a site's NATS leaf node, sync KV buckets with the hub, serve local health
- **[Nebula Overlay](docs/nebula.md)** - Run the agent as a host on your organization's Nebula mesh
- **[Script Development](docs/script-development.md)** - Write custom scripts

---

## Building from Source

### Prerequisites
- Go 1.26+
- Make (optional, for convenience)

### Build for Current Platform
```bash
git clone https://github.com/stone-age-io/agent.git
cd agent
make build
```

### Build for All Platforms
```bash
make build-all VERSION=0.1.0
```

The makefile is for local and development builds. Releases are cut by
goreleaser from a pushed `v*` tag (`.goreleaser.yaml`,
`.github/workflows/release.yml`), which stamps the version and publishes the
archives the install steps above download.

Generates binaries:
- `build/agent-linux-amd64`
- `build/agent-linux-arm64`
- `build/agent-freebsd-amd64`
- `build/agent-windows-amd64.exe`

### Run Tests
```bash
make test
```

---

## Community & Support

- **Issues**: [GitHub Issues](https://github.com/stone-age-io/agent/issues)
- **Discussions**: [GitHub Discussions](https://github.com/stone-age-io/agent/discussions)

---

## License

MIT License - see [LICENSE](LICENSE) for details.
