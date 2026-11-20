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

### 1. Install Agent

```powershell
# Run as Administrator

# Download agent (set the version to the latest release)
$agentVersion = "0.10.0"
$downloadUrl = "https://github.com/stone-age-io/agent/releases/download/v$agentVersion/agent_${agentVersion}_windows_amd64.zip"
$agentPath = "C:\Program Files\Agent"
$configPath = "C:\ProgramData\Agent"

# Create directories
New-Item -ItemType Directory -Force -Path $agentPath
New-Item -ItemType Directory -Force -Path $configPath

# Download and extract the release archive. It also carries LICENSE,
# README.md, the per-OS example configs under configs\, and these guides
# under docs\.
Invoke-WebRequest -Uri $downloadUrl -OutFile "$env:TEMP\agent.zip"
Expand-Archive -Path "$env:TEMP\agent.zip" -DestinationPath "$env:TEMP\agent" -Force
Move-Item -Path "$env:TEMP\agent\agent.exe" -Destination "$agentPath\agent.exe" -Force

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

### 2. Configure Agent

Start from the example config in the release archive. Only the identity and
the NATS connection are live in it; every other setting is commented out at
its default, and nothing is allowed until you list it.

```powershell
Copy-Item "$env:TEMP\agent\configs\windows\config.yaml.example" "$configPath\config.yaml"
```

**Edit configuration:**

```powershell
notepad "$configPath\config.yaml"
```

**Required changes:**
1. Set unique `code` (and optionally `location`)
2. Update `nats.urls` with your NATS server
3. Configure authentication (credentials file, stone-age.io platform, token, or userpass)

Everything else is optional: uncomment a block to turn it on. The sections below
cover services, commands, log paths and scripts.

**Copy NATS credentials (if using creds auth):**

```powershell
Copy-Item "\\path\to\device.creds" -Destination "$configPath\device.creds"
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
    creds_file: "C:\\ProgramData\\Agent\\device.creds"
```

The `platform:` block is top level because three subsystems read it: this
credential lifecycle, the [Nebula config source](nebula.md), and the
[leaf bootstrap](leaf-node.md).

Set the environment variable before starting the agent:
```powershell
[Environment]::SetEnvironmentVariable("AGENT_PLATFORM_PASSWORD", "your-password", "Machine")
```

See **[Platform Credentials Guide](credentials.md)** for full setup details, including removing the password after the first boot.

---

### 3. Install as Windows Service

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

### 4. Verify Installation

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
# {"status":"pong","ts":"2026-..."}

# Check health
nats request "agents.windows-server-01.cmd.health" '{}'

# Find it by service discovery: every agent in the account answers
# (needs $SRV.> on the agent's NATS role; see docs/architecture.md)
nats micro ls stone-agent

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

**Security note**: A request must match an entry exactly, apart from
whitespace, and the entry as you wrote it is what runs. Be specific!

### Log File Paths

Configure which log files can be retrieved:

```yaml
commands:
  allowed_log_paths:
    - "C:\\inetpub\\logs\\LogFiles\\*\\*.log"
    - "C:\\Logs\\*.log"
```

Patterns use Go's `filepath.Glob`: `*` and `?` match within one path element
and `[...]` matches a character class. There is no recursive `**`: each `*`
matches exactly one directory level, so `LogFiles\*\*.log` reads the IIS
site folders one level down. The allowlist is the only check. If a path matches
a pattern, it can be read.

Event logs (`.evtx`) are binary and can't be tailed by line. Read them with
`Get-WinEvent` in a script instead.

---

## Example Scripts

Scripts are off until `commands.scripts_directory` names a directory that
exists. Set it to `C:\ProgramData\Agent\Scripts` and create it:

```powershell
New-Item -ItemType Directory -Force -Path "$configPath\Scripts"
```

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

**Check agent logs:**
```powershell
Select-String -Path "C:\ProgramData\Agent\agent.log" -Pattern "metrics"
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

The agent starts PowerShell with `-ExecutionPolicy Bypass`, so the machine's
local execution policy does not apply to its commands and scripts. Only a
policy set by Group Policy overrides that:

**Check execution policy:**
```powershell
# MachinePolicy or UserPolicy set here wins over -ExecutionPolicy Bypass
Get-ExecutionPolicy -List
```

---

## Upgrading

### Upgrade Agent Binary

```powershell
# Stop service
Stop-Service agent

# Backup current binary
Copy-Item "C:\Program Files\Agent\agent.exe" -Destination "C:\Program Files\Agent\agent.exe.backup"

# Download and extract the new release archive
$agentVersion = "0.10.0"
$downloadUrl = "https://github.com/stone-age-io/agent/releases/download/v$agentVersion/agent_${agentVersion}_windows_amd64.zip"
Invoke-WebRequest -Uri $downloadUrl -OutFile "$env:TEMP\agent.zip"
Expand-Archive -Path "$env:TEMP\agent.zip" -DestinationPath "$env:TEMP\agent" -Force
Move-Item -Path "$env:TEMP\agent\agent.exe" -Destination "C:\Program Files\Agent\agent.exe" -Force

# Start service
Start-Service agent

# Verify version (check logs)
Get-Content "C:\ProgramData\Agent\agent.log" -Tail 20 | Select-String "version"
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

3. **Firewall**: Outbound NATS, plus outbound HTTPS if the platform block is set
   ```powershell
   # Inbound: none by default -- /ready and /metrics bind 127.0.0.1:10063, which
   # is not reachable off the box. A SITE GATEWAY is the exception: local
   # devices connect in to the nats-server it hosts (4222 by default).

   # Verify outbound is allowed:
   Test-NetConnection -ComputerName nats.example.com -Port 4222
   # And, when the platform block is configured:
   Test-NetConnection -ComputerName platform.example.com -Port 443
   ```

4. **Windows Updates**: Keep Windows and agent updated

---

## Next Steps

- **[Architecture Overview](architecture.md)** - Understand the system design
- **[Script Development Guide](script-development.md)** - Write custom scripts

---

**Need help?** Open an issue on [GitHub](https://github.com/stone-age-io/agent/issues)
