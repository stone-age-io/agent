# Windows Installation Guide

Complete guide for installing and configuring the agent on Windows systems.

## Prerequisites

- **Operating System**: Windows Server 2016+, Windows 10, or Windows 11
- **Architecture**: x64 (64-bit)
- **Privileges**: Administrator access required
- **Network**: Access to NATS server (default: port 4222)
- **PowerShell**: Version 5.1 or later (included by default)

---

## Installation Steps

### 1. Install windows_exporter

The agent collects system metrics from Prometheus windows_exporter.

**Download and install:**

```powershell
# Run as Administrator

# Download latest release
$exporterVersion = "0.25.1"
$downloadUrl = "https://github.com/prometheus-community/windows_exporter/releases/download/v$exporterVersion/windows_exporter-$exporterVersion-amd64.msi"
$installerPath = "$env:TEMP\windows_exporter.msi"

Invoke-WebRequest -Uri $downloadUrl -OutFile $installerPath

# Install MSI (installs as Windows service)
Start-Process msiexec.exe -ArgumentList "/i `"$installerPath`" /quiet" -Wait

# Verify service is running
Get-Service windows_exporter

# Test metrics endpoint
Invoke-WebRequest -Uri "http://localhost:9182/metrics" -UseBasicParsing | Select-Object -ExpandProperty Content | Select-Object -First 20
```

**Alternative: Manual Installation**

If you prefer to install manually without MSI:

```powershell
# Download binary
$exporterVersion = "0.25.1"
$downloadUrl = "https://github.com/prometheus-community/windows_exporter/releases/download/v$exporterVersion/windows_exporter-$exporterVersion-amd64.exe"
$installPath = "C:\Program Files\windows_exporter"

# Create directory
New-Item -ItemType Directory -Force -Path $installPath

# Download
Invoke-WebRequest -Uri $downloadUrl -OutFile "$installPath\windows_exporter.exe"

# Install as service
& "$installPath\windows_exporter.exe" --service.install

# Start service
Start-Service windows_exporter
```

---

### 2. Install Agent

```powershell
# Run as Administrator

# Download agent (replace VERSION with latest release)
$agentVersion = "1.0.0"
$downloadUrl = "https://github.com/stone-age-io/agent/releases/download/v$agentVersion/agent-windows-amd64.exe"
$agentPath = "C:\Program Files\Agent"
$configPath = "C:\ProgramData\Agent"

# Create directories
New-Item -ItemType Directory -Force -Path $agentPath
New-Item -ItemType Directory -Force -Path $configPath
New-Item -ItemType Directory -Force -Path "$configPath\Scripts"

# Download agent binary
Invoke-WebRequest -Uri $downloadUrl -OutFile "$agentPath\agent.exe"

# Set permissions (Agent directory - read/execute)
$acl = Get-Acl $agentPath
$acl.SetAccessRuleProtection($true, $false)
$rule = New-Object System.Security.AccessControl.FileSystemAccessRule("SYSTEM","FullControl","ContainerInherit,ObjectInherit","None","Allow")
$acl.AddAccessRule($rule)
$rule = New-Object System.Security.AccessControl.FileSystemAccessRule("Administrators","FullControl","ContainerInherit,ObjectInherit","None","Allow")
$acl.AddAccessRule($rule)
Set-Acl $agentPath $acl
```

---

### 3. Configure Agent

Create configuration file:

```powershell
# Create configuration
$configContent = @'
# Agent Configuration for Windows

# Unique identifier for this agent
device_id: "windows-server-01"

# NATS subject prefix (optional)
subject_prefix: "agents"

# NATS Connection
nats:
  urls: 
    - "nats://nats.example.com:4222"
  
  # Authentication (choose one method)
  auth:
    type: "creds"
    creds_file: "C:\\ProgramData\\Agent\\device.creds"
  
  # TLS Configuration (optional)
  tls:
    enabled: false
    cert_file: "C:\\ProgramData\\Agent\\client-cert.pem"
    key_file: "C:\\ProgramData\\Agent\\client-key.pem"
    ca_file: "C:\\ProgramData\\Agent\\ca-cert.pem"
  
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
    exporter_url: "http://localhost:9182/metrics"
  
  service_check:
    enabled: true
    interval: "1m"
    services:
      - "W3SVC"         # IIS
      - "MSSQLSERVER"   # SQL Server
      - "WinRM"         # Windows Remote Management
  
  inventory:
    enabled: true
    interval: "24h"

# Command Execution
commands:
  scripts_directory: "C:\\ProgramData\\Agent\\Scripts"
  
  allowed_services:
    - "W3SVC"
    - "MSSQLSERVER"
    - "WinRM"
  
  allowed_commands:
    - "Get-ComputerInfo | Select-Object WindowsVersion, OsHardwareAbstractionLayer"
    - "Get-Disk | Select-Object Number, FriendlyName, Size, HealthStatus"
  
  allowed_log_paths:
    - "C:\\inetpub\\logs\\LogFiles\\**\\*.log"
    - "C:\\Logs\\*.log"
  
  timeout: "30s"

# Logging
logging:
  level: "info"
  file: "C:\\ProgramData\\Agent\\agent.log"
  max_size_mb: 100
  max_backups: 3
'@

$configContent | Out-File -FilePath "$configPath\config.yaml" -Encoding UTF8
```

**Edit configuration:**

```powershell
notepad "$configPath\config.yaml"
```

**Required changes:**
1. Set unique `device_id`
2. Update `nats.urls` with your NATS server
3. Configure authentication (credentials file, token, or userpass)
4. Adjust monitored services in `tasks.service_check.services`

**Copy NATS credentials (if using creds auth):**

```powershell
Copy-Item "\\path\to\device.creds" -Destination "$configPath\device.creds"
```

---

### 4. Install as Windows Service

```powershell
# Install service
& "$agentPath\agent.exe" -service install

# Verify service was created
Get-Service agent

# Set service to start automatically
Set-Service agent -StartupType Automatic

# Start service
Start-Service agent

# Check status
Get-Service agent | Format-List *
```

---

### 5. Verify Installation

#### Check Service Status

```powershell
# View service status
Get-Service agent

# View service details
Get-Service agent | Format-List *

# Check if service is running
Get-Process | Where-Object {$_.ProcessName -eq "agent"}
```

#### Check Agent Logs

```powershell
# View recent log entries
Get-Content "C:\ProgramData\Agent\agent.log" -Tail 50

# Monitor logs in real-time
Get-Content "C:\ProgramData\Agent\agent.log" -Wait -Tail 20

# Search for errors
Select-String -Path "C:\ProgramData\Agent\agent.log" -Pattern "ERROR"
```

#### Test NATS Communication

From a machine with NATS CLI installed:

```bash
# Test ping
nats request "agents.windows-server-01.cmd.ping" '{}'

# Expected response:
# {"status":"pong","timestamp":"2025-..."}

# Check health
nats request "agents.windows-server-01.cmd.health" '{}'

# Subscribe to telemetry
nats sub "agents.windows-server-01.>"
```

---

## Configuration Options

### Monitored Services

Add or remove services to monitor:

```yaml
tasks:
  service_check:
    services:
      - "W3SVC"         # IIS
      - "MSSQLSERVER"   # SQL Server
      - "WinRM"
      - "Spooler"       # Print Spooler
```

**Find service names:**
```powershell
Get-Service | Format-Table Name, DisplayName, Status
```

### Allowed Commands

Whitelist PowerShell commands for remote execution:

```yaml
commands:
  allowed_commands:
    - "Get-ComputerInfo | Select-Object WindowsVersion"
    - "Get-Disk | Select-Object Number, Size, HealthStatus"
    - "Get-Process | Sort-Object CPU -Descending | Select-Object -First 10"
```

**Security note**: Only exact matches are allowed. Be specific!

### Log File Paths

Configure which log files can be retrieved:

```yaml
commands:
  allowed_log_paths:
    - "C:\\inetpub\\logs\\LogFiles\\**\\*.log"
    - "C:\\Logs\\*.log"
    - "C:\\Windows\\System32\\winevt\\Logs\\Application.evtx"
```

Supports glob patterns (`**` for recursive, `*` for wildcard).

---

## Example Scripts

Create custom PowerShell scripts in `C:\ProgramData\Agent\Scripts\`:

### System Information Script

```powershell
# Create script file
$scriptContent = @'
# Get comprehensive system information
$info = Get-ComputerInfo | Select-Object `
    WindowsProductName,
    WindowsVersion,
    OsUptime,
    CsTotalPhysicalMemory,
    OsFreePhysicalMemory,
    CsNumberOfProcessors,
    CsNumberOfLogicalProcessors

$info | ConvertTo-Json -Compress
'@

$scriptContent | Out-File -FilePath "C:\ProgramData\Agent\Scripts\Get-SystemInfo.ps1" -Encoding UTF8
```

### Disk Space Script

```powershell
$scriptContent = @'
# Get disk space information
$disks = Get-Volume | Where-Object {$_.DriveLetter -ne $null} | Select-Object `
    DriveLetter,
    @{Name="SizeGB";Expression={[math]::Round($_.Size/1GB,2)}},
    @{Name="FreeSpaceGB";Expression={[math]::Round($_.SizeRemaining/1GB,2)}},
    @{Name="PercentFree";Expression={[math]::Round(($_.SizeRemaining/$_.Size)*100,2)}},
    HealthStatus

$disks | ConvertTo-Json -Compress
'@

$scriptContent | Out-File -FilePath "C:\ProgramData\Agent\Scripts\Get-DiskSpace.ps1" -Encoding UTF8
```

### Windows Update Status Script

```powershell
$scriptContent = @'
# Get Windows Update status
$updateSession = New-Object -ComObject Microsoft.Update.Session
$updateSearcher = $updateSession.CreateUpdateSearcher()
$searchResult = $updateSearcher.Search("IsInstalled=0 and IsHidden=0")

$result = @{
    UpdateCount = $searchResult.Updates.Count
    Updates = @($searchResult.Updates | Select-Object Title, IsDownloaded)
    LastCheck = (Get-ItemProperty "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update" -Name AULastDetectionTime).AULastDetectionTime
}

$result | ConvertTo-Json -Compress
'@

$scriptContent | Out-File -FilePath "C:\ProgramData\Agent\Scripts\Get-WindowsUpdates.ps1" -Encoding UTF8
```

**Test scripts locally:**
```powershell
powershell.exe -ExecutionPolicy Bypass -File "C:\ProgramData\Agent\Scripts\Get-SystemInfo.ps1"
powershell.exe -ExecutionPolicy Bypass -File "C:\ProgramData\Agent\Scripts\Get-DiskSpace.ps1"
```

---

## Service Management

### Start/Stop/Restart

```powershell
# Start service
Start-Service agent

# Stop service
Stop-Service agent

# Restart service
Restart-Service agent
```

### View Service Status

```powershell
# Basic status
Get-Service agent

# Detailed status
Get-Service agent | Format-List *

# Check process
Get-Process | Where-Object {$_.ProcessName -eq "agent"}
```

### View Logs

```powershell
# View recent entries
Get-Content "C:\ProgramData\Agent\agent.log" -Tail 50

# Real-time monitoring
Get-Content "C:\ProgramData\Agent\agent.log" -Wait -Tail 20

# Search for patterns
Select-String -Path "C:\ProgramData\Agent\agent.log" -Pattern "ERROR|WARN"

# View by date range
Get-Content "C:\ProgramData\Agent\agent.log" | Where-Object {$_ -match "2025-11-17"}
```

---

## Troubleshooting

### Agent Won't Start

**Check service status:**
```powershell
Get-Service agent | Format-List *
```

**Check Windows Event Log:**
```powershell
Get-EventLog -LogName Application -Source agent -Newest 50
```

**Common issues:**

1. **Config file errors**
   ```powershell
   # Test config manually
   & "C:\Program Files\Agent\agent.exe" -config "C:\ProgramData\Agent\config.yaml"
   ```

2. **Permission errors**
   ```powershell
   # Check file permissions
   Get-Acl "C:\ProgramData\Agent\config.yaml" | Format-List
   
   # Ensure SYSTEM can read config
   $acl = Get-Acl "C:\ProgramData\Agent"
   $rule = New-Object System.Security.AccessControl.FileSystemAccessRule("SYSTEM","Read","ContainerInherit,ObjectInherit","None","Allow")
   $acl.AddAccessRule($rule)
   Set-Acl "C:\ProgramData\Agent" $acl
   ```

3. **NATS connection failed**
   ```powershell
   # Test NATS connectivity
   Test-NetConnection -ComputerName nats.example.com -Port 4222
   
   # Check credentials file exists
   Test-Path "C:\ProgramData\Agent\device.creds"
   ```

### No Metrics Being Published

**Check windows_exporter:**
```powershell
# Verify service is running
Get-Service windows_exporter

# Test metrics endpoint
Invoke-WebRequest -Uri "http://localhost:9182/metrics" -UseBasicParsing
```

**Check agent logs:**
```powershell
Select-String -Path "C:\ProgramData\Agent\agent.log" -Pattern "metrics"
```

**Verify exporter URL in config:**
```powershell
Select-String -Path "C:\ProgramData\Agent\config.yaml" -Pattern "exporter_url"
```

### Service Control Not Working

**Check allowed services:**
```powershell
Select-String -Path "C:\ProgramData\Agent\config.yaml" -Pattern "allowed_services" -Context 0,5
```

**Verify service exists:**
```powershell
Get-Service W3SVC  # Example
```

**Check agent logs:**
```powershell
Select-String -Path "C:\ProgramData\Agent\agent.log" -Pattern "service"
```

### PowerShell Execution Errors

**Check execution policy:**
```powershell
Get-ExecutionPolicy

# If restricted, set to RemoteSigned
Set-ExecutionPolicy RemoteSigned -Scope LocalMachine
```

---

## Upgrading

### Upgrade Agent Binary

```powershell
# Stop service
Stop-Service agent

# Backup current binary
Copy-Item "C:\Program Files\Agent\agent.exe" -Destination "C:\Program Files\Agent\agent.exe.backup"

# Download new version
$agentVersion = "1.1.0"
$downloadUrl = "https://github.com/stone-age-io/agent/releases/download/v$agentVersion/agent-windows-amd64.exe"
Invoke-WebRequest -Uri $downloadUrl -OutFile "C:\Program Files\Agent\agent.exe"

# Start service
Start-Service agent

# Verify version (check logs)
Get-Content "C:\ProgramData\Agent\agent.log" -Tail 20 | Select-String "version"
```

### Upgrade windows_exporter

```powershell
# Stop service
Stop-Service windows_exporter

# Download new MSI
$exporterVersion = "0.26.0"
$downloadUrl = "https://github.com/prometheus-community/windows_exporter/releases/download/v$exporterVersion/windows_exporter-$exporterVersion-amd64.msi"
$installerPath = "$env:TEMP\windows_exporter.msi"
Invoke-WebRequest -Uri $downloadUrl -OutFile $installerPath

# Install (will upgrade existing)
Start-Process msiexec.exe -ArgumentList "/i `"$installerPath`" /quiet" -Wait

# Start service
Start-Service windows_exporter
```

---

## Uninstallation

### Remove Agent

```powershell
# Stop service
Stop-Service agent

# Uninstall service
& "C:\Program Files\Agent\agent.exe" -service uninstall

# Remove files
Remove-Item "C:\Program Files\Agent" -Recurse -Force
Remove-Item "C:\ProgramData\Agent" -Recurse -Force
```

### Remove windows_exporter (Optional)

```powershell
# Stop service
Stop-Service windows_exporter

# Uninstall via Programs and Features
# Or using MSI uninstall:
Start-Process msiexec.exe -ArgumentList "/x windows_exporter /quiet" -Wait
```

---

## Security Best Practices

1. **Credentials**: Store NATS credentials securely
   ```powershell
   $acl = Get-Acl "C:\ProgramData\Agent\device.creds"
   $acl.SetAccessRuleProtection($true, $false)
   $rule = New-Object System.Security.AccessControl.FileSystemAccessRule("SYSTEM","Read","Allow")
   $acl.AddAccessRule($rule)
   Set-Acl "C:\ProgramData\Agent\device.creds" $acl
   ```

2. **Scripts**: Only allow trusted scripts
   ```powershell
   # Restrict Scripts directory to Administrators
   $acl = Get-Acl "C:\ProgramData\Agent\Scripts"
   $acl.SetAccessRuleProtection($true, $false)
   $rule = New-Object System.Security.AccessControl.FileSystemAccessRule("Administrators","FullControl","ContainerInherit,ObjectInherit","None","Allow")
   $acl.AddAccessRule($rule)
   Set-Acl "C:\ProgramData\Agent\Scripts" $acl
   ```

3. **Firewall**: Agent only needs outbound NATS connection
   ```powershell
   # No inbound ports required
   # Verify outbound is allowed:
   Test-NetConnection -ComputerName nats.example.com -Port 4222
   ```

4. **Windows Updates**: Keep Windows and agent updated

---

## Next Steps

- **[Architecture Overview](architecture.md)** - Understand the system design
- **[Script Development Guide](script-development.md)** - Write custom scripts
- **[Configuration Reference](configuration.md)** - All config options
- **[Troubleshooting](troubleshooting.md)** - Common issues

---

**Need help?** Open an issue on [GitHub](https://github.com/stone-age-io/agent/issues)
