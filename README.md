# Windows Agent

A lightweight Windows management and observability agent written in Go. The agent runs as a native Windows Service and communicates exclusively over NATS for remote device management.

## Features

- **Native Windows Service**: Runs as a system service with automatic startup
- **NATS Integration**: All communication via NATS (Core Request/Reply for commands, JetStream for telemetry)
- **TLS Support**: Secure encrypted connections with optional mutual TLS authentication
- **System Monitoring**: CPU, memory, disk metrics via windows_exporter
- **Service Management**: Start/stop/restart Windows services
- **Log Retrieval**: Fetch log file contents remotely
- **Command Execution**: Execute whitelisted PowerShell commands
- **System Inventory**: Hardware and software inventory collection
- **Secure by Default**: Whitelist-based security, no exposed HTTP endpoints
- **Graceful Shutdown**: Proper NATS drain and cleanup

## Architecture

The agent follows a simple, purpose-built design:

```
NATS (Data Plane)
    ↕ (TLS encrypted)
win-agent.exe (Windows Service)
    ├─ Scheduled Tasks (JetStream)
    │   ├─ Heartbeat (every 1m)
    │   ├─ System Metrics (every 5m)
    │   ├─ Service Status (every 1m)
    │   └─ Inventory (every 24h)
    │
    └─ Command Handlers (Core Request/Reply)
        ├─ Ping/Pong
        ├─ Service Control (start/stop/restart)
        ├─ Log Fetch
        └─ Custom Exec (PowerShell)
```

## Prerequisites

- Windows Server 2016+ or Windows 10+
- Go 1.23+ (for building)
- [windows_exporter](https://github.com/prometheus-community/windows_exporter) installed and running
- Access to a NATS server with JetStream enabled

## Installation

### 1. Install windows_exporter

Download and install windows_exporter with the required collectors:

```powershell
# Download from https://github.com/prometheus-community/windows_exporter/releases
.\windows_exporter.exe install --collectors.enabled "cpu,memory,logical_disk,os"
Start-Service windows_exporter

# Verify it's running
Invoke-WebRequest http://localhost:9182/metrics
```

### 2. Build the Agent

From a machine with Go installed:

```bash
make build-release VERSION=1.0.0
```

This creates `win-agent.exe` with version 1.0.0 embedded.

### 3. Prepare Configuration

Create the configuration directory and copy files:

```powershell
# Create directories
New-Item -Path "C:\Program Files\WinAgent" -ItemType Directory
New-Item -Path "C:\ProgramData\WinAgent" -ItemType Directory

# Copy binary
Copy-Item win-agent.exe "C:\Program Files\WinAgent\"

# Copy and edit configuration
Copy-Item config.yaml.example "C:\ProgramData\WinAgent\config.yaml"
notepad "C:\ProgramData\WinAgent\config.yaml"
```

Edit `config.yaml` to set:
- `device_id`: Unique identifier for this agent
- `nats.urls`: Your NATS server URL(s)
- `nats.auth`: Authentication credentials
- `nats.tls`: TLS configuration (if using encrypted connections)
- `tasks.service_check.services`: Services to monitor
- `commands.allowed_services`: Services that can be controlled
- `commands.allowed_commands`: PowerShell commands that can be executed
- `commands.allowed_log_paths`: Log files that can be retrieved

### 4. Install and Start Service

```powershell
cd "C:\Program Files\WinAgent"

# Install as Windows service
.\win-agent.exe -service install

# Start the service
Start-Service win-agent

# Check status
Get-Service win-agent

# View logs
Get-Content "C:\ProgramData\WinAgent\agent.log" -Tail 50 -Wait
```

## Configuration

### NATS Authentication

The agent supports three authentication methods:

#### Credentials File (Recommended)

```yaml
nats:
  auth:
    type: "creds"
    creds_file: "C:\\ProgramData\\WinAgent\\device.creds"
```

#### Token

```yaml
nats:
  auth:
    type: "token"
    token: "your-secret-token"
```

#### Username/Password

```yaml
nats:
  auth:
    type: "userpass"
    username: "agent-user"
    password: "secret-password"
```

### TLS Configuration

The agent supports TLS encryption for secure communication with NATS servers. TLS is **strongly recommended for production deployments**.

#### Basic TLS (Server Verification)

Use this when connecting to a server with a certificate signed by a trusted CA:

```yaml
nats:
  urls:
    - "tls://connect.your-service.com:4222"
  tls:
    enabled: true
```

#### TLS with Custom CA

Use this when your NATS server uses a self-signed certificate or an internal CA:

```yaml
nats:
  urls:
    - "tls://connect.your-service.com:4222"
  tls:
    enabled: true
    ca_file: "C:\\ProgramData\\WinAgent\\ca-cert.pem"
```

#### Mutual TLS (Client Certificate Authentication)

Use this when the NATS server requires client certificates for authentication:

```yaml
nats:
  urls:
    - "tls://connect.your-service.com:4222"
  auth:
    type: "none"  # Auth via client certificate
  tls:
    enabled: true
    cert_file: "C:\\ProgramData\\WinAgent\\client-cert.pem"
    key_file: "C:\\ProgramData\\WinAgent\\client-key.pem"
    ca_file: "C:\\ProgramData\\WinAgent\\ca-cert.pem"
```

#### Recommended Production Configuration

Combine TLS encryption with NATS credentials for maximum security:

```yaml
nats:
  urls:
    - "tls://connect.your-service.com:4222"
  auth:
    type: "creds"
    creds_file: "C:\\ProgramData\\WinAgent\\device.creds"
  tls:
    enabled: true
    ca_file: "C:\\ProgramData\\WinAgent\\ca-cert.pem"
```

**⚠️ WARNING**: Never use `insecure_skip_verify: true` in production. It disables certificate verification and makes the connection vulnerable to man-in-the-middle attacks.

### Task Configuration

Each task can be individually enabled/disabled and has a configurable interval:

```yaml
tasks:
  heartbeat:
    enabled: true
    interval: "1m"
  
  system_metrics:
    enabled: true
    interval: "5m"
    exporter_url: "http://localhost:9182/metrics"
  
  service_check:
    enabled: true
    interval: "1m"
    services:
      - "MyService"
  
  inventory:
    enabled: true
    interval: "24h"
```

### Security Configuration

All command execution is whitelist-based:

```yaml
commands:
  # Only these services can be controlled
  allowed_services:
    - "MyAppService"
  
  # Only these exact commands can be executed
  allowed_commands:
    - "Get-Process | Sort-Object CPU -Descending | Select-Object -First 5"
  
  # Only logs matching these patterns can be read
  allowed_log_paths:
    - "C:\\Logs\\*.log"
  
  # Command execution timeout
  timeout: "30s"
```

## NATS Subjects

### Telemetry (Published by Agent)

The agent publishes telemetry data to JetStream:

- `agents.<device_id>.heartbeat` - Heartbeat every 60s
- `agents.<device_id>.telemetry.system` - System metrics every 5min
- `agents.<device_id>.telemetry.service` - Service status every 60s
- `agents.<device_id>.telemetry.inventory` - Inventory on startup and daily

### Commands (Sent to Agent)

Commands use Core NATS Request/Reply:

- `agents.<device_id>.cmd.ping` - Ping/pong liveness check
- `agents.<device_id>.cmd.service` - Service control (start/stop/restart)
- `agents.<device_id>.cmd.logs` - Fetch log file contents
- `agents.<device_id>.cmd.exec` - Execute PowerShell command
- `agents.<device_id>.cmd.health` - Agent health and performance metrics

## Usage Examples

### Send a Ping Command

```bash
nats request "agents.device-12345.cmd.ping" '{}'
```

Response:
```json
{
  "status": "pong",
  "timestamp": "2025-11-14T12:00:00Z"
}
```

### Restart a Service

```bash
nats request "agents.device-12345.cmd.service" '{
  "action": "restart",
  "service_name": "MyService"
}'
```

Response:
```json
{
  "status": "success",
  "service_name": "MyService",
  "action": "restart",
  "result": "Service MyService restarted successfully",
  "timestamp": "2025-11-14T12:00:00Z"
}
```

### Fetch Log File

```bash
nats request "agents.device-12345.cmd.logs" '{
  "log_path": "C:\\Logs\\app.log",
  "lines": 100
}'
```

Response:
```json
{
  "status": "success",
  "log_path": "C:\\Logs\\app.log",
  "lines": ["line1", "line2", "..."],
  "total_lines": 100,
  "timestamp": "2025-11-14T12:00:00Z"
}
```

### Execute PowerShell Command

```bash
nats request "agents.device-12345.cmd.exec" '{
  "command": "Get-Process | Sort-Object CPU -Descending | Select-Object -First 5"
}'
```

Response:
```json
{
  "status": "success",
  "command": "Get-Process...",
  "output": "...",
  "exit_code": 0,
  "timestamp": "2025-11-14T12:00:00Z"
}
```

### Check Agent Health

```bash
nats request "agents.device-12345.cmd.health" '{}'
```

Response:
```json
{
  "status": "healthy",
  "agent_metrics": {
    "memory_usage_mb": 45.2,
    "goroutines": 12,
    "uptime_seconds": 86400,
    "commands_processed": 1543,
    "commands_errored": 2
  },
  "timestamp": "2025-11-14T12:00:00Z"
}
```

### Subscribe to Telemetry

```bash
# All telemetry from a device
nats sub "agents.device-12345.>"

# Just heartbeats
nats sub "agents.device-12345.heartbeat"

# Just system metrics
nats sub "agents.device-12345.telemetry.system"
```

## Maintenance

### Viewing Logs

```powershell
# Tail logs in real-time
Get-Content "C:\ProgramData\WinAgent\agent.log" -Tail 50 -Wait

# View last 100 lines
Get-Content "C:\ProgramData\WinAgent\agent.log" -Tail 100
```

### Restarting the Service

```powershell
Restart-Service win-agent
```

### Updating Configuration

```powershell
# Edit config
notepad "C:\ProgramData\WinAgent\config.yaml"

# Restart service to apply changes
Restart-Service win-agent
```

### Upgrading the Agent

```powershell
# Stop service
Stop-Service win-agent

# Replace binary
Copy-Item win-agent.exe "C:\Program Files\WinAgent\" -Force

# Start service
Start-Service win-agent
```

### Uninstalling

```powershell
cd "C:\Program Files\WinAgent"

# Stop and uninstall service
.\win-agent.exe -service stop
.\win-agent.exe -service uninstall

# Remove files
Remove-Item "C:\Program Files\WinAgent" -Recurse -Force
Remove-Item "C:\ProgramData\WinAgent" -Recurse -Force
```

## Troubleshooting

### Agent Won't Start

1. Check the configuration file is valid:
   ```powershell
   Get-Content "C:\ProgramData\WinAgent\config.yaml"
   ```

2. Verify NATS connectivity:
   ```powershell
   Test-NetConnection connect.your-service.com -Port 4222
   ```

3. Check Windows Event Log:
   ```powershell
   Get-EventLog -LogName Application -Source win-agent -Newest 20
   ```

### TLS Connection Issues

1. Verify TLS is properly configured in config.yaml

2. Test certificate files exist and are readable:
   ```powershell
   Test-Path "C:\ProgramData\WinAgent\ca-cert.pem"
   Test-Path "C:\ProgramData\WinAgent\client-cert.pem"
   ```

3. Check agent logs for TLS errors:
   ```powershell
   Get-Content "C:\ProgramData\WinAgent\agent.log" | Select-String "TLS"
   ```

4. For development, temporarily enable `insecure_skip_verify` to rule out certificate issues

### Metrics Not Publishing

1. Verify windows_exporter is running:
   ```powershell
   Get-Service windows_exporter
   Invoke-WebRequest http://localhost:9182/metrics
   ```

2. Check agent logs for scraping errors:
   ```powershell
   Get-Content "C:\ProgramData\WinAgent\agent.log" | Select-String "metrics"
   ```

### Commands Not Working

1. Verify command is in whitelist in config.yaml
2. Check agent logs for permission errors
3. Ensure service is running as appropriate user (LocalService)

## Performance

- **CPU Usage**: < 1% average
- **Memory Usage**: < 50MB
- **Binary Size**: ~20MB (includes all dependencies)
- **Startup Time**: < 2 seconds
- **Command Response Time**: < 500ms for simple commands

## Security

- Service runs as LocalService account (least privilege)
- All commands and services are whitelist-controlled
- Log file access restricted to configured paths
- No HTTP endpoints exposed
- NATS authentication required
- TLS encryption available for secure communications
- Tenant isolation via NATS accounts
- Client certificate authentication supported

### Security Best Practices

1. **Always use TLS in production** - Enable `nats.tls.enabled: true`
2. **Use credentials file authentication** - More secure than username/password
3. **Never use `insecure_skip_verify`** in production
4. **Keep whitelists minimal** - Only allow necessary services, commands, and log paths
5. **Rotate certificates regularly** - Follow your organization's PKI policies
6. **Monitor agent logs** - Watch for authentication failures and unauthorized access attempts
7. **Run as LocalService** - Ensure the service has minimal privileges

## License

MIT License - See LICENSE file for details

## Support

For issues and questions, please open an issue on the GitHub repository.
