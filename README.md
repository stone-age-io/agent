# Agent

A lightweight, NATS-native management and observability agent for **Windows**, **Linux**, and **FreeBSD**.

---

## Overview

Agent is a purpose-built system management tool that provides remote management and observability for server infrastructure through a secure, lightweight agent.

**Key Principles:**
- **Lightweight**: <50MB RAM, <1% CPU usage
- **Secure**: TLS support, whitelist-based execution, no exposed endpoints
- **Simple**: Do one thing well
- **Extensible**: PowerShell/Bash scripts for custom functionality
- **NATS-Native**: All communication via NATS (no HTTP endpoints)

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
- **Telemetry Publishing**: JetStream for durable metrics
- **Command Handling**: Core NATS request/reply
- **Multi-Tenant**: NATS account isolation
- **TLS Support**: Encrypted communication

---

## Quick Start

Choose your platform:

<details>
<summary><b>Windows</b></summary>

```powershell
# 1. Download agent
# Get latest release from: https://github.com/stone-age-io/agent/releases

# 2. Install
New-Item -ItemType Directory -Force -Path "C:\Program Files\Agent"
Copy-Item agent.exe "C:\Program Files\Agent\"
Copy-Item config.yaml "C:\ProgramData\Agent\"

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
wget https://github.com/stone-age-io/agent/releases/download/v1.0.0/agent-linux-amd64
sudo mv agent-linux-amd64 /usr/local/bin/agent
sudo chmod +x /usr/local/bin/agent

# 2. Configure
sudo mkdir -p /etc/agent
sudo cp config.yaml /etc/agent/
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
fetch https://github.com/stone-age-io/agent/releases/download/v1.0.0/agent-freebsd-amd64
sudo mv agent-freebsd-amd64 /usr/local/bin/agent
sudo chmod +x /usr/local/bin/agent

# 2. Configure
sudo mkdir -p /usr/local/etc/agent
sudo cp config.yaml /usr/local/etc/agent/
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
device_id: "server-prod-01"

# NATS Connection
nats:
  urls: ["nats://nats.example.com:4222"]
  auth:
    type: "creds"
    creds_file: "/path/to/device.creds"

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
- **[Script Development](docs/script-development.md)** - Write custom scripts

---

## Building from Source

### Prerequisites
- Go 1.24+
- Make (optional, for convenience)

### Build for Current Platform
```bash
git clone https://github.com/stone-age-io/agent.git
cd agent
make build
```

### Build for All Platforms
```bash
make build-all VERSION=1.0.0
```

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
- **Contributing**: See [CONTRIBUTING.md](CONTRIBUTING.md)

---

## License

MIT License - see [LICENSE](LICENSE) for details.
