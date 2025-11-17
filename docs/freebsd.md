# FreeBSD Installation Guide

Complete guide for installing and configuring the agent on FreeBSD systems.

## Prerequisites

- **Operating System**: FreeBSD 13.0 or later (tested on 14.0+)
- **Architecture**: amd64 (x86_64)
- **Privileges**: root access required
- **Network**: Access to NATS server (default: port 4222)

---

## Installation Steps

### 1. Install node_exporter

The agent collects system metrics from Prometheus node_exporter.

#### Option A: Install from Packages (Recommended)

```bash
# Install via pkg
sudo pkg install node_exporter

# Enable in rc.conf
sudo sysrc node_exporter_enable="YES"

# Start service
sudo service node_exporter start

# Verify it's running
service node_exporter status
fetch -qo - http://localhost:9100/metrics | head -20
```

#### Option B: Install from Ports

```bash
# Update ports tree
sudo portsnap fetch update

# Install node_exporter
cd /usr/ports/sysutils/node_exporter
sudo make install clean

# Enable and start
sudo sysrc node_exporter_enable="YES"
sudo service node_exporter start
```

#### Option C: Install Binary Manually

```bash
# Download latest release
cd /tmp
fetch https://github.com/prometheus/node_exporter/releases/download/v1.7.0/node_exporter-1.7.0.freebsd-amd64.tar.gz

# Extract
tar xvfz node_exporter-1.7.0.freebsd-amd64.tar.gz

# Install binary
sudo mv node_exporter-1.7.0.freebsd-amd64/node_exporter /usr/local/bin/
sudo chmod +x /usr/local/bin/node_exporter

# Create rc.d script
sudo tee /usr/local/etc/rc.d/node_exporter > /dev/null <<'EOF'
#!/bin/sh
#
# PROVIDE: node_exporter
# REQUIRE: NETWORKING
# KEYWORD: shutdown

. /etc/rc.subr

name="node_exporter"
rcvar="${name}_enable"
command="/usr/local/bin/node_exporter"
node_exporter_user="nobody"

load_rc_config $name
: ${node_exporter_enable:="NO"}

pidfile="/var/run/${name}.pid"
command_args="&"

run_rc_command "$1"
EOF

sudo chmod +x /usr/local/etc/rc.d/node_exporter

# Enable and start
sudo sysrc node_exporter_enable="YES"
sudo service node_exporter start
```

---

### 2. Install Agent

```bash
# Download agent binary (replace VERSION with latest release)
cd /tmp
fetch https://github.com/stone-age-io/agent/releases/download/v1.0.0/agent-freebsd-amd64

# Install binary
sudo mv agent-freebsd-amd64 /usr/local/bin/agent
sudo chmod +x /usr/local/bin/agent

# Create directories
sudo mkdir -p /usr/local/etc/agent
sudo mkdir -p /usr/local/etc/agent/scripts
sudo mkdir -p /var/log/agent

# Set permissions
sudo chmod 755 /usr/local/etc/agent
sudo chmod 755 /usr/local/etc/agent/scripts
sudo chmod 755 /var/log/agent
```

---

### 3. Configure Agent

Create configuration file:

```bash
sudo tee /usr/local/etc/agent/config.yaml > /dev/null <<'EOF'
# Agent Configuration for FreeBSD

# Unique identifier for this agent
device_id: "freebsd-server-01"

# NATS subject prefix (optional)
subject_prefix: "agents"

# NATS Connection
nats:
  urls: 
    - "nats://nats.example.com:4222"
  
  # Authentication (choose one method)
  auth:
    type: "creds"
    creds_file: "/usr/local/etc/agent/device.creds"
  
  # TLS Configuration (optional)
  tls:
    enabled: false
    cert_file: "/usr/local/etc/agent/client-cert.pem"
    key_file: "/usr/local/etc/agent/client-key.pem"
    ca_file: "/usr/local/etc/agent/ca-cert.pem"
  
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
    exporter_url: "http://localhost:9100/metrics"
  
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
  scripts_directory: "/usr/local/etc/agent/scripts"
  
  allowed_services:
    - "nginx"
    - "postgresql"
    - "redis"
  
  allowed_commands:
    - "df -h | grep -E '^/dev/'"
    - "uptime"
    - "top -b | head -20"
  
  allowed_log_paths:
    - "/var/log/nginx/*.log"
    - "/usr/local/www/app/*.log"
  
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
sudo ee /usr/local/etc/agent/config.yaml
# Or: sudo vi /usr/local/etc/agent/config.yaml
```

**Required changes:**
1. Set unique `device_id`
2. Update `nats.urls` with your NATS server
3. Configure authentication (credentials file, token, or userpass)
4. Adjust monitored services in `tasks.service_check.services`

**Copy NATS credentials (if using creds auth):**

```bash
sudo cp /path/to/device.creds /usr/local/etc/agent/device.creds
sudo chmod 600 /usr/local/etc/agent/device.creds
```

---

### 4. Install as rc.d Service

```bash
# Install service (kardianos/service handles rc.d setup)
sudo /usr/local/bin/agent -service install

# Verify rc.d script was created
ls -la /usr/local/etc/rc.d/agent

# Enable on boot
sudo sysrc agent_enable="YES"

# Start service
sudo service agent start

# Check status
sudo service agent status
```

---

### 5. Verify Installation

#### Check Service Status

```bash
# View service status
sudo service agent status

# View process
ps aux | grep agent

# Check if it's running
sockstat -l | grep 4222  # Should show NATS connection
```

#### Check Agent Logs

```bash
# View agent log file
sudo tail -f /var/log/agent/agent.log

# Check for errors
sudo grep ERROR /var/log/agent/agent.log

# View last 50 lines
sudo tail -50 /var/log/agent/agent.log
```

#### Test NATS Communication

From a machine with NATS CLI installed:

```bash
# Test ping
nats request "agents.freebsd-server-01.cmd.ping" '{}'

# Expected response:
# {"status":"pong","timestamp":"2025-..."}

# Check health
nats request "agents.freebsd-server-01.cmd.health" '{}'

# Subscribe to telemetry
nats sub "agents.freebsd-server-01.>"
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
      - "sshd"
```

**Find service names:**
```bash
service -e  # List enabled services
service -l  # List all services
```

### Allowed Commands

Whitelist commands for remote execution:

```yaml
commands:
  allowed_commands:
    - "df -h | grep -E '^/dev/'"
    - "uptime"
    - "ps aux | sort -rk %cpu | head -10"
    - "zpool status"  # ZFS pool status
```

**Security note**: Only exact matches are allowed. Be specific!

### Log File Paths

Configure which log files can be retrieved:

```yaml
commands:
  allowed_log_paths:
    - "/var/log/nginx/*.log"
    - "/usr/local/www/app/*.log"
    - "/var/log/messages"
```

Supports glob patterns for flexibility.

---

## Example Scripts

Create custom scripts in `/usr/local/etc/agent/scripts/`:

### System Information Script

```bash
sudo tee /usr/local/etc/agent/scripts/get-system-info.sh > /dev/null <<'EOF'
#!/bin/sh
# Get comprehensive system information

echo "{"
echo "  \"hostname\": \"$(hostname)\","
echo "  \"uptime\": \"$(uptime | awk '{print $3, $4, $5}')\","
echo "  \"kernel\": \"$(uname -r)\","
echo "  \"arch\": \"$(uname -m)\","
echo "  \"users\": $(who | wc -l | tr -d ' '),"
echo "  \"load_avg\": \"$(uptime | awk -F'load average:' '{print $2}')\","
echo "  \"memory_percent\": $(sysctl -n vm.stats.vm.v_page_count vm.stats.vm.v_free_count | awk 'NR==1{t=$1}NR==2{printf "%.1f", (1-$1/t)*100}')"
echo "}"
EOF

sudo chmod +x /usr/local/etc/agent/scripts/get-system-info.sh
```

### ZFS Pool Status Script

```bash
sudo tee /usr/local/etc/agent/scripts/get-zfs-status.sh > /dev/null <<'EOF'
#!/bin/sh
# Get ZFS pool status in JSON format

zpool list -H | awk 'BEGIN {print "["} 
NR>1 {print ","} 
{printf "{\"pool\":\"%s\",\"size\":\"%s\",\"alloc\":\"%s\",\"free\":\"%s\",\"frag\":\"%s\",\"cap\":\"%s\",\"health\":\"%s\"}", $1,$2,$3,$4,$6,$7,$10} 
END {print "]"}' | tr -d '\n' | sed 's/,\[/[/'
EOF

sudo chmod +x /usr/local/etc/agent/scripts/get-zfs-status.sh
```

### Network Statistics Script

```bash
sudo tee /usr/local/etc/agent/scripts/get-network-stats.sh > /dev/null <<'EOF'
#!/bin/sh
# Get network interface statistics

netstat -ibn | awk 'NR>1 && $1 !~ /lo/ {print $1, $7, $10}' | \
awk 'BEGIN {print "["} 
NR>1 {print ","} 
{printf "{\"interface\":\"%s\",\"in_bytes\":%s,\"out_bytes\":%s}", $1,$2,$3} 
END {print "]"}' | tr -d '\n' | sed 's/,\[/[/'
EOF

sudo chmod +x /usr/local/etc/agent/scripts/get-network-stats.sh
```

**Test scripts locally:**
```bash
/usr/local/etc/agent/scripts/get-system-info.sh
/usr/local/etc/agent/scripts/get-zfs-status.sh
/usr/local/etc/agent/scripts/get-network-stats.sh
```

---

## Service Management

### Start/Stop/Restart

```bash
sudo service agent start
sudo service agent stop
sudo service agent restart
```

### Enable/Disable on Boot

```bash
# Enable
sudo sysrc agent_enable="YES"

# Disable
sudo sysrc agent_enable="NO"

# Check status
sysrc agent_enable
```

### Check Service Status

```bash
# Status
sudo service agent status

# Process info
ps aux | grep agent

# Network connections
sockstat -4 | grep agent
```

---

## Troubleshooting

### Agent Won't Start

**Check service status:**
```bash
sudo service agent status
```

**Check logs:**
```bash
sudo tail -50 /var/log/agent/agent.log
```

**Common issues:**

1. **Config file errors**
   ```bash
   # Test config manually
   /usr/local/bin/agent -config /usr/local/etc/agent/config.yaml
   ```

2. **Permission errors**
   ```bash
   # Check file permissions
   ls -la /usr/local/etc/agent/config.yaml
   ls -la /var/log/agent/
   
   # Fix permissions
   sudo chmod 644 /usr/local/etc/agent/config.yaml
   sudo chmod 755 /var/log/agent
   ```

3. **NATS connection failed**
   ```bash
   # Test NATS connectivity
   nc -zv nats.example.com 4222
   
   # Check credentials file
   ls -la /usr/local/etc/agent/device.creds
   ```

### No Metrics Being Published

**Check node_exporter:**
```bash
# Verify node_exporter is running
service node_exporter status

# Test metrics endpoint
fetch -qo - http://localhost:9100/metrics | head -20
```

**Check agent logs:**
```bash
sudo grep metrics /var/log/agent/agent.log
```

**Verify exporter URL in config:**
```bash
grep exporter_url /usr/local/etc/agent/config.yaml
```

### Service Control Not Working

**Check allowed services:**
```bash
grep -A 5 "allowed_services:" /usr/local/etc/agent/config.yaml
```

**Verify service exists:**
```bash
service nginx status
```

**Check agent logs:**
```bash
sudo grep service /var/log/agent/agent.log
```

---

## Upgrading

### Upgrade Agent Binary

```bash
# Stop service
sudo service agent stop

# Backup current binary
sudo cp /usr/local/bin/agent /usr/local/bin/agent.backup

# Download new version
cd /tmp
fetch https://github.com/stone-age-io/agent/releases/download/v1.1.0/agent-freebsd-amd64

# Install new binary
sudo mv agent-freebsd-amd64 /usr/local/bin/agent
sudo chmod +x /usr/local/bin/agent

# Start service
sudo service agent start

# Verify version (check logs)
sudo tail -20 /var/log/agent/agent.log | grep version
```

### Upgrade node_exporter

```bash
# Using pkg
sudo pkg upgrade node_exporter

# Or manually
cd /tmp
fetch https://github.com/prometheus/node_exporter/releases/download/v1.8.0/node_exporter-1.8.0.freebsd-amd64.tar.gz
tar xvfz node_exporter-1.8.0.freebsd-amd64.tar.gz
sudo service node_exporter stop
sudo mv node_exporter-1.8.0.freebsd-amd64/node_exporter /usr/local/bin/
sudo service node_exporter start
```

---

## Uninstallation

### Remove Agent

```bash
# Stop service
sudo service agent stop

# Disable service
sudo sysrc agent_enable="NO"

# Uninstall service
sudo /usr/local/bin/agent -service uninstall

# Remove files
sudo rm /usr/local/bin/agent
sudo rm -rf /usr/local/etc/agent
sudo rm -rf /var/log/agent
sudo rm /usr/local/etc/rc.d/agent
```

### Remove node_exporter (Optional)

```bash
# Stop and disable
sudo service node_exporter stop
sudo sysrc node_exporter_enable="NO"

# Remove via pkg
sudo pkg delete node_exporter

# Or remove manually
sudo rm /usr/local/bin/node_exporter
sudo rm /usr/local/etc/rc.d/node_exporter
```

---

## FreeBSD-Specific Features

### ZFS Integration

Monitor ZFS pools with custom scripts:

```bash
# Add to allowed commands
commands:
  allowed_commands:
    - "zpool status"
    - "zpool list"
    - "zfs list"
```

### Jail Management

Monitor jails if using FreeBSD jails:

```bash
# Add to allowed commands
commands:
  allowed_commands:
    - "jls"
    - "jexec <jailname> ps aux"
```

### Package Management

Check installed packages:

```bash
# Add to allowed commands
commands:
  allowed_commands:
    - "pkg info"
    - "pkg version"
```

---

## Security Best Practices

1. **Credentials**: Store NATS credentials with restrictive permissions
   ```bash
   sudo chmod 600 /usr/local/etc/agent/device.creds
   ```

2. **Scripts**: Only allow trusted scripts
   ```bash
   sudo chmod 755 /usr/local/etc/agent/scripts
   sudo chmod 700 /usr/local/etc/agent/scripts/*.sh
   ```

3. **Firewall**: Use ipfw or pf to restrict connections
   ```bash
   # Agent only needs outbound NATS connection
   # No inbound ports required
   ```

4. **Updates**: Keep FreeBSD, agent, and node_exporter updated
   ```bash
   sudo freebsd-update fetch install
   sudo pkg upgrade
   ```

---

## Next Steps

- **[Architecture Overview](architecture.md)** - Understand the system design
- **[Script Development Guide](script-development.md)** - Write custom scripts
- **[Configuration Reference](configuration.md)** - All config options
- **[Troubleshooting](troubleshooting.md)** - Common issues

---

**Need help?** Open an issue on [GitHub](https://github.com/stone-age-io/agent/issues)
