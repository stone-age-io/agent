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
# Download agent binary (replace VERSION with latest release)
cd /tmp
wget https://github.com/stone-age-io/agent/releases/download/v1.0.0/agent-linux-amd64

# Install binary
sudo mv agent-linux-amd64 /usr/local/bin/agent
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

# Unique identifier for this agent
device_id: "linux-server-01"

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
    source: "builtin"  # "builtin" (default) or "exporter"
    # exporter_url: "http://localhost:9100/metrics"  # Only for exporter mode
  
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
1. Set unique `device_id`
2. Update `nats.urls` with your NATS server
3. Configure authentication (credentials file, token, or userpass)
4. Adjust monitored services in `tasks.service_check.services`

**Copy NATS credentials (if using creds auth):**

```bash
sudo cp /path/to/device.creds /etc/agent/device.creds
sudo chmod 600 /etc/agent/device.creds
```

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
# {"status":"pong","timestamp":"2025-..."}

# Check health
nats request "agents.linux-server-01.cmd.health" '{}'

# Subscribe to telemetry
nats sub "agents.linux-server-01.>"
```

---

## Optional: Install node_exporter

By default, the agent uses built-in metrics collection. If you prefer to use Prometheus node_exporter for additional metrics, follow these steps:

### Download and Install Binary

```bash
# Download latest release
cd /tmp
wget https://github.com/prometheus/node_exporter/releases/download/v1.7.0/node_exporter-1.7.0.linux-amd64.tar.gz

# Extract
tar xvfz node_exporter-1.7.0.linux-amd64.tar.gz

# Install binary
sudo mv node_exporter-1.7.0.linux-amd64/node_exporter /usr/local/bin/
sudo chmod +x /usr/local/bin/node_exporter

# Create systemd service
sudo tee /etc/systemd/system/node_exporter.service > /dev/null <<'EOF'
[Unit]
Description=Prometheus Node Exporter
Documentation=https://github.com/prometheus/node_exporter
After=network-online.target

[Service]
Type=simple
User=node_exporter
Group=node_exporter
ExecStart=/usr/local/bin/node_exporter \
    --collector.filesystem.mount-points-exclude='^/(dev|proc|sys|var/lib/docker/.+|var/lib/kubelet/.+)($|/)' \
    --collector.netclass.ignored-devices='^(veth.*)$'
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# Create user
sudo useradd --no-create-home --shell /bin/false node_exporter

# Enable and start service
sudo systemctl daemon-reload
sudo systemctl enable node_exporter
sudo systemctl start node_exporter

# Verify it's running
systemctl status node_exporter
curl -s http://localhost:9100/metrics | head -20
```

### Configure Agent for Exporter Mode

Update your config.yaml:

```yaml
tasks:
  system_metrics:
    enabled: true
    interval: "5m"
    source: "exporter"
    exporter_url: "http://localhost:9100/metrics"
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

Supports glob patterns for flexibility.

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

**If using exporter mode, verify node_exporter:**
```bash
# Verify node_exporter is running
systemctl status node_exporter

# Test metrics endpoint
curl http://localhost:9100/metrics | head -20

# Verify exporter URL in config
grep exporter_url /etc/agent/config.yaml
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

### Upgrade node_exporter (If Using Exporter Mode)

```bash
# Stop service
sudo systemctl stop node_exporter

# Download new version
cd /tmp
wget https://github.com/prometheus/node_exporter/releases/download/v1.8.0/node_exporter-1.8.0.linux-amd64.tar.gz
tar xvfz node_exporter-1.8.0.linux-amd64.tar.gz

# Replace binary
sudo mv node_exporter-1.8.0.linux-amd64/node_exporter /usr/local/bin/

# Start service
sudo systemctl start node_exporter
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

### Remove node_exporter (Optional)

```bash
# Stop and disable
sudo systemctl stop node_exporter
sudo systemctl disable node_exporter

# Remove files
sudo rm /usr/local/bin/node_exporter
sudo rm /etc/systemd/system/node_exporter.service

# Reload systemd
sudo systemctl daemon-reload

# Remove user (if created)
sudo userdel node_exporter
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
   # Agent only needs outbound NATS connection
   # No inbound ports required
   ```

4. **Log Rotation**: Ensure logs don't fill disk
   ```yaml
   logging:
     max_size_mb: 100
     max_backups: 3
   ```

5. **Regular Updates**: Keep agent and node_exporter updated

---

## Next Steps

- **[Architecture Overview](architecture.md)** - Understand the system design
- **[Script Development Guide](script-development.md)** - Write custom scripts
- **[Configuration Reference](configuration.md)** - All config options
- **[Troubleshooting](troubleshooting.md)** - Common issues

---

**Need help?** Open an issue on [GitHub](https://github.com/stone-age-io/agent/issues)
