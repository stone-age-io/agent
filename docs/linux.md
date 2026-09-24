# Linux Installation Guide

Complete guide for installing and configuring the agent on Linux systems.

## Prerequisites

- **Operating System**: Ubuntu 22.04+, Debian 11+, or any systemd-based distribution
- **Architecture**: amd64 (x86_64) or arm64 (aarch64)
- **Privileges**: sudo/root access required
- **Network**: Access to NATS server (default: port 4222)

---

## Installation Steps

### 1. Install Agent

```bash
# Download and extract the release archive (set VERSION to the latest release)
cd /tmp
VERSION=0.1.0
wget https://github.com/stone-age-io/agent/releases/download/v${VERSION}/agent_${VERSION}_linux_amd64.tar.gz
tar xzf agent_${VERSION}_linux_amd64.tar.gz

# Install binary. The archive also carries LICENSE, README.md, the per-OS
# example configs under configs/, and these guides under docs/.
sudo mv agent /usr/local/bin/agent
sudo chmod +x /usr/local/bin/agent

# Create directories
sudo mkdir -p /etc/agent
sudo mkdir -p /opt/agent/scripts
sudo mkdir -p /var/log/agent

# Set permissions
sudo chmod 755 /etc/agent
sudo chmod 755 /opt/agent
sudo chmod 755 /opt/agent/scripts
sudo chmod 755 /var/log/agent
```

---

### 2. Configure Agent

Create configuration file:

```bash
sudo tee /etc/agent/config.yaml > /dev/null <<'EOF'
# Agent Configuration for Linux

# Agent identity: code is the NATS subject token (legacy key: device_id),
# location is optional and carried in heartbeat/telemetry payloads
code: "linux-server-01"
location: "hq"

# NATS subject prefix (optional)
subject_prefix: "agents"

# NATS Connection
nats:
  urls: 
    - "nats://nats.example.com:4222"
  
  # Authentication (choose one method)
  auth:
    type: "creds"
    creds_file: "/etc/agent/device.creds"
  
  # TLS Configuration (optional)
  tls:
    enabled: false
    cert_file: "/etc/agent/client-cert.pem"
    key_file: "/etc/agent/client-key.pem"
    ca_file: "/etc/agent/ca-cert.pem"
  
  max_reconnects: -1
  reconnect_wait: "2s"
  drain_timeout: "30s"

# Scheduled Tasks
tasks:
  heartbeat:
    enabled: true
    interval: "1m"
  
  system_metrics:
    enabled: true
    interval: "5m"
    # Built-in (gopsutil). You can still run node_exporter for Prometheus to
    # scrape directly; the agent does not read it. Its port, 9100, is also the
    # default observability.addr, so move one of them.
  
  service_check:
    enabled: true
    interval: "1m"
    services:
      - "nginx"
      - "postgresql"
      - "redis"
  
  inventory:
    enabled: true
    interval: "24h"

# Command Execution
commands:
  scripts_directory: "/opt/agent/scripts"
  
  allowed_services:
    - "nginx"
    - "postgresql"
    - "redis"
  
  allowed_commands:
    - "df -h | grep -E '^/dev/'"
    - "uptime"
    - "free -h"
  
  allowed_log_paths:
    - "/var/log/nginx/*.log"
    - "/var/log/app/*.log"
  
  timeout: "30s"

# Logging
logging:
  level: "info"
  file: "/var/log/agent/agent.log"
  max_size_mb: 100
  max_backups: 3
EOF
```

**Edit configuration:**

```bash
sudo nano /etc/agent/config.yaml
```

**Required changes:**
1. Set unique `code` (and optionally `location`)
2. Update `nats.urls` with your NATS server
3. Configure authentication (credentials file, stone-age.io platform, token, or userpass)
4. Adjust monitored services in `tasks.service_check.services`

**Copy NATS credentials (if using creds auth):**

```bash
sudo cp /path/to/device.creds /etc/agent/device.creds
sudo chmod 600 /etc/agent/device.creds
```

**Or let the stone-age.io platform manage credentials — the agent logs in as its Thing record, pulls creds from its nats_user relation, and keeps them current:**

```yaml
platform:
  url: "https://platform.example.com"
  identity: "thing@example.com"              # the thing's login email
  password_env: "AGENT_PLATFORM_PASSWORD"

nats:
  auth:
    type: "platform"
    creds_file: "/etc/agent/device.creds"
```

The `platform:` block is top level because three subsystems read it: this
credential lifecycle, the [Nebula config source](nebula.md), and the
[leaf bootstrap](leaf-node.md).

Set the environment variable before starting the agent:
```bash
sudo systemctl edit agent
# Add: Environment="AGENT_PLATFORM_PASSWORD=your-password"
```

See **[Platform Credentials Guide](credentials.md)** for full setup details, including removing the password after the first boot.

---

### 3. Install as systemd Service

```bash
# Install service (kardianos/service handles systemd setup)
sudo /usr/local/bin/agent -service install

# Verify service file was created
cat /etc/systemd/system/agent.service

# Reload systemd
sudo systemctl daemon-reload

# Enable on boot
sudo systemctl enable agent

# Start service
sudo systemctl start agent

# Check status
sudo systemctl status agent
```

---

### 4. Verify Installation

#### Check Service Status

```bash
# View service status
sudo systemctl status agent

# View logs (real-time)
sudo journalctl -u agent -f

# View last 50 log entries
sudo journalctl -u agent -n 50 --no-pager
```

#### Check Agent Logs

```bash
# View agent log file
sudo tail -f /var/log/agent/agent.log

# Check for errors
sudo grep ERROR /var/log/agent/agent.log
```

#### Test NATS Communication

From a machine with NATS CLI installed:

```bash
# Test ping
nats request "agents.linux-server-01.cmd.ping" '{}'

# Expected response:
# {"status":"pong","ts":"2026-..."}

# Check health
nats request "agents.linux-server-01.cmd.health" '{}'

# Subscribe to telemetry
nats sub "agents.linux-server-01.>"
```

---

## Configuration Options

### Monitored Services

Add or remove services to monitor:

```yaml
tasks:
  service_check:
    services:
      - "nginx"
      - "postgresql"
      - "redis"
      - "docker"
```

**Find service names:**
```bash
systemctl list-units --type=service --state=running
```

### Allowed Commands

Whitelist commands for remote execution:

```yaml
commands:
  allowed_commands:
    - "df -h | grep -E '^/dev/'"
    - "uptime"
    - "ps aux | sort -rk 3 | head -10"
```

**Security note**: Only exact matches are allowed. Be specific!

### Log File Paths

Configure which log files can be retrieved:

```yaml
commands:
  allowed_log_paths:
    - "/var/log/nginx/*.log"
    - "/var/log/app/*.log"
    - "/var/log/syslog"
```

Patterns use Go's `filepath.Glob`: `*` and `?` match within one path element
and `[...]` matches a character class. There is no recursive `**`. The
allowlist is the only check. If a path matches a pattern, it can be read.

---

## Example Scripts

Create custom scripts in `/opt/agent/scripts/`:

### System Information Script

```bash
sudo tee /opt/agent/scripts/get-system-info.sh > /dev/null <<'EOF'
#!/bin/bash
# Get comprehensive system information

echo "{"
echo "  \"hostname\": \"$(hostname)\","
echo "  \"uptime\": \"$(uptime -p)\","
echo "  \"kernel\": \"$(uname -r)\","
echo "  \"users\": $(who | wc -l),"
echo "  \"load_avg\": \"$(uptime | awk -F'load average:' '{print $2}')\","
echo "  \"memory_percent\": $(free | grep Mem | awk '{printf "%.1f", $3/$2 * 100.0}')"
echo "}"
EOF

sudo chmod +x /opt/agent/scripts/get-system-info.sh
```

### Disk Usage Script

```bash
sudo tee /opt/agent/scripts/get-disk-usage.sh > /dev/null <<'EOF'
#!/bin/bash
# Get disk usage in JSON format

df -h | grep '^/dev/' | awk 'BEGIN {print "["} 
NR>1 {print ","} 
{printf "{\"device\":\"%s\",\"size\":\"%s\",\"used\":\"%s\",\"avail\":\"%s\",\"use_percent\":\"%s\",\"mount\":\"%s\"}", $1,$2,$3,$4,$5,$6} 
END {print "]"}' | tr -d '\n' | sed 's/,\[/[/'
EOF

sudo chmod +x /opt/agent/scripts/get-disk-usage.sh
```

**Test scripts locally:**
```bash
/opt/agent/scripts/get-system-info.sh
/opt/agent/scripts/get-disk-usage.sh
```

---

## Service Management

### Start/Stop/Restart

```bash
sudo systemctl start agent
sudo systemctl stop agent
sudo systemctl restart agent
```

### View Logs

```bash
# Real-time logs
sudo journalctl -u agent -f

# Last 100 entries
sudo journalctl -u agent -n 100

# Logs since boot
sudo journalctl -u agent -b

# Logs with errors only
sudo journalctl -u agent -p err
```

### Check Configuration

```bash
# Test config without starting service
/usr/local/bin/agent -config /etc/agent/config.yaml
```

---

## Troubleshooting

### Agent Won't Start

**Check service status:**
```bash
sudo systemctl status agent
```

**Check logs:**
```bash
sudo journalctl -u agent -n 50 --no-pager
```

**Common issues:**

1. **Config file errors**
   ```bash
   # Validate YAML syntax
   python3 -c "import yaml; yaml.safe_load(open('/etc/agent/config.yaml'))"
   ```

2. **Permission errors**
   ```bash
   # Check file permissions
   ls -la /etc/agent/config.yaml
   ls -la /var/log/agent/
   
   # Fix permissions
   sudo chmod 644 /etc/agent/config.yaml
   sudo chmod 755 /var/log/agent
   ```

3. **NATS connection failed**
   ```bash
   # Test NATS connectivity
   nc -zv nats.example.com 4222
   
   # Check credentials file exists
   ls -la /etc/agent/device.creds
   ```

### No Metrics Being Published

**Check agent logs:**
```bash
sudo journalctl -u agent | grep metrics
```

### Service Control Not Working

**Check allowed services:**
```bash
grep -A 5 "allowed_services:" /etc/agent/config.yaml
```

**Verify service exists:**
```bash
systemctl status nginx
```

**Check agent logs:**
```bash
sudo journalctl -u agent | grep service
```

---

## Upgrading

### Upgrade Agent Binary

```bash
# Stop service
sudo systemctl stop agent

# Backup current binary
sudo cp /usr/local/bin/agent /usr/local/bin/agent.backup

# Download new version
cd /tmp
wget https://github.com/stone-age-io/agent/releases/download/v1.1.0/agent-linux-amd64

# Install new binary
sudo mv agent-linux-amd64 /usr/local/bin/agent
sudo chmod +x /usr/local/bin/agent

# Start service
sudo systemctl start agent

# Verify version (check logs)
sudo journalctl -u agent | grep "Starting agent version"
```

---

## Uninstallation

### Remove Agent

```bash
# Stop service
sudo systemctl stop agent

# Disable service
sudo systemctl disable agent

# Uninstall service
sudo /usr/local/bin/agent -service uninstall

# Remove files
sudo rm /usr/local/bin/agent
sudo rm -rf /etc/agent
sudo rm -rf /opt/agent
sudo rm -rf /var/log/agent
```

---

## Security Best Practices

1. **Credentials**: Store NATS credentials with restrictive permissions
   ```bash
   sudo chmod 600 /etc/agent/device.creds
   ```

2. **Scripts**: Only allow trusted scripts, verify before deploying
   ```bash
   sudo chmod 755 /opt/agent/scripts
   sudo chmod 700 /opt/agent/scripts/*.sh  # Owner only
   ```

3. **Firewall**: Block incoming connections if not needed
   ```bash
   # Telemetry and commands: outbound NATS only.
   # Credentials, Nebula config and leaf bootstrap: outbound HTTPS (443) to the
   # Control Plane, when the `platform:` block is set.
   #
   # Inbound: none by default -- /ready and /metrics bind 127.0.0.1:9100, which
   # is not reachable off the box. A SITE GATEWAY is the exception: local
   # devices connect in to the nats-server it hosts (4222 by default).
   ```

4. **Log Rotation**: Ensure logs don't fill disk
   ```yaml
   logging:
     max_size_mb: 100
     max_backups: 3
   ```

5. **Regular Updates**: Keep the agent updated

---

## Next Steps

- **[Architecture Overview](architecture.md)** - Understand the system design
- **[Script Development Guide](script-development.md)** - Write custom scripts

---

**Need help?** Open an issue on [GitHub](https://github.com/stone-age-io/agent/issues)
