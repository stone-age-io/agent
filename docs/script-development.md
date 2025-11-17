# Script Development Guide

Learn how to write custom scripts for the agent.

## Overview

The agent's extensibility comes from **scripts**, not built-in features. This keeps the agent lightweight while allowing unlimited customization.

**Design Philosophy:**
- Agent = Dumb executor
- Scripts = Business logic
- Simple over clever

---

## Script Types

### PowerShell Scripts (Windows)

**Extension:** `.ps1`
**Location:** `C:\ProgramData\Agent\Scripts\`
**Executor:** PowerShell 5.1+

### Bash/Shell Scripts (Linux/FreeBSD)

**Extension:** `.sh`
**Location:** `/opt/agent/scripts/` (Linux), `/usr/local/etc/agent/scripts/` (FreeBSD)
**Executor:** `/bin/bash`

---

## Best Practices

### 1. Always Return JSON

Scripts should return JSON for consistent parsing:

**PowerShell:**
```powershell
$result = @{
    status = "success"
    data = @{
        cpu_count = (Get-WmiObject Win32_ComputerSystem).NumberOfLogicalProcessors
        memory_gb = [math]::Round((Get-WmiObject Win32_ComputerSystem).TotalPhysicalMemory/1GB, 2)
    }
}

$result | ConvertTo-Json -Compress
```

**Bash:**
```bash
#!/bin/bash

echo "{
  \"status\": \"success\",
  \"data\": {
    \"cpu_count\": $(nproc),
    \"memory_gb\": $(free -g | awk '/Mem:/{print $2}')
  }
}"
```

### 2. Handle Errors Gracefully

**PowerShell:**
```powershell
try {
    $result = Get-SomeData
    @{
        status = "success"
        data = $result
    } | ConvertTo-Json -Compress
}
catch {
    @{
        status = "error"
        error = $_.Exception.Message
    } | ConvertTo-Json -Compress
    exit 1
}
```

**Bash:**
```bash
#!/bin/bash

if result=$(some_command 2>&1); then
    echo "{\"status\":\"success\",\"data\":\"$result\"}"
    exit 0
else
    echo "{\"status\":\"error\",\"error\":\"$result\"}"
    exit 1
fi
```

### 3. Use Timeouts

Agent has a default 30-second timeout. Keep scripts fast:

**Avoid:**
```powershell
# BAD: Long-running operations
Start-Sleep -Seconds 60
```

**Do:**
```powershell
# GOOD: Quick data collection
Get-Process | Select-Object -First 10
```

### 4. No External Dependencies

Scripts should work with standard tools:

**PowerShell:**
- Use built-in cmdlets
- Avoid third-party modules

**Bash:**
- Use standard utilities (awk, sed, grep)
- Avoid requiring extra packages

### 5. Secure Data Handling

**Don't:**
```powershell
# BAD: Exposing sensitive data
$password = "secret123"
```

**Do:**
```powershell
# GOOD: Reference secure storage
$cred = Get-Credential -UserName "app" -Message "Enter password"
```

---

## Example Scripts

### Windows (PowerShell)

#### 1. Get Windows Updates Status

```powershell
# Get-WindowsUpdates.ps1
# Returns pending Windows updates

try {
    $updateSession = New-Object -ComObject Microsoft.Update.Session
    $updateSearcher = $updateSession.CreateUpdateSearcher()
    
    # Search for updates not installed
    $searchResult = $updateSearcher.Search("IsInstalled=0 and IsHidden=0")
    
    $updates = @($searchResult.Updates | ForEach-Object {
        @{
            title = $_.Title
            description = $_.Description
            isDownloaded = $_.IsDownloaded
            severity = $_.MsrcSeverity
            rebootRequired = $_.RebootRequired
        }
    })
    
    $result = @{
        status = "success"
        update_count = $searchResult.Updates.Count
        updates = $updates
        last_check = (Get-ItemProperty "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update" -ErrorAction SilentlyContinue).AULastDetectionTime
    }
    
    $result | ConvertTo-Json -Compress
}
catch {
    @{
        status = "error"
        error = $_.Exception.Message
    } | ConvertTo-Json -Compress
    exit 1
}
```

#### 2. Check IIS Application Pools

```powershell
# Get-IISAppPools.ps1
# Returns IIS application pool status

Import-Module WebAdministration -ErrorAction Stop

try {
    $pools = Get-ChildItem IIS:\AppPools | Select-Object name, state, @{
        Name="ProcessModel"
        Expression={$_.processModel.userName}
    }
    
    $result = @{
        status = "success"
        pools = @($pools | ForEach-Object {
            @{
                name = $_.name
                state = $_.state.ToString()
                identity = $_.ProcessModel
            }
        })
    }
    
    $result | ConvertTo-Json -Compress
}
catch {
    @{
        status = "error"
        error = $_.Exception.Message
    } | ConvertTo-Json -Compress
    exit 1
}
```

#### 3. SQL Server Database Sizes

```powershell
# Get-SQLDatabaseSizes.ps1
# Returns database sizes for SQL Server

param(
    [string]$ServerInstance = "localhost"
)

try {
    $query = @"
SELECT 
    name AS database_name,
    (size * 8 / 1024) AS size_mb,
    (CAST(FILEPROPERTY(name, 'SpaceUsed') AS int) * 8 / 1024) AS used_mb
FROM sys.master_files
WHERE type = 0
ORDER BY size DESC
"@
    
    $databases = Invoke-Sqlcmd -ServerInstance $ServerInstance -Query $query -ErrorAction Stop
    
    $result = @{
        status = "success"
        server = $ServerInstance
        databases = @($databases | ForEach-Object {
            @{
                name = $_.database_name
                size_mb = $_.size_mb
                used_mb = $_.used_mb
                free_mb = $_.size_mb - $_.used_mb
            }
        })
    }
    
    $result | ConvertTo-Json -Compress
}
catch {
    @{
        status = "error"
        error = $_.Exception.Message
    } | ConvertTo-Json -Compress
    exit 1
}
```

---

### Linux (Bash)

#### 1. Check Docker Containers

```bash
#!/bin/bash
# get-docker-status.sh
# Returns Docker container status

if ! command -v docker &> /dev/null; then
    echo '{"status":"error","error":"Docker not installed"}'
    exit 1
fi

# Get container list
containers=$(docker ps --format '{"name":"{{.Names}}","status":"{{.Status}}","image":"{{.Image}}"}' 2>&1)

if [ $? -eq 0 ]; then
    # Wrap in array
    echo "{\"status\":\"success\",\"containers\":[${containers//$'\n'/,}]}"
else
    echo "{\"status\":\"error\",\"error\":\"$containers\"}"
    exit 1
fi
```

#### 2. Check Certificate Expiration

```bash
#!/bin/bash
# check-ssl-certificates.sh
# Check SSL certificate expiration dates

cert_dir="/etc/ssl/certs"
results="["

first=true
for cert in "$cert_dir"/*.crt; do
    [ -f "$cert" ] || continue
    
    # Get expiration date
    exp_date=$(openssl x509 -in "$cert" -noout -enddate 2>/dev/null | cut -d= -f2)
    
    if [ -n "$exp_date" ]; then
        exp_epoch=$(date -d "$exp_date" +%s 2>/dev/null)
        now_epoch=$(date +%s)
        days_left=$(( ($exp_epoch - $now_epoch) / 86400 ))
        
        [ "$first" = false ] && results+=","
        first=false
        
        results+="{\"file\":\"$(basename "$cert")\",\"expires\":\"$exp_date\",\"days_left\":$days_left}"
    fi
done

results+="]"

echo "{\"status\":\"success\",\"certificates\":$results}"
```

#### 3. PostgreSQL Database Statistics

```bash
#!/bin/bash
# get-postgres-stats.sh
# Get PostgreSQL database statistics

DB_NAME="${1:-postgres}"
DB_USER="${2:-postgres}"

query="SELECT 
    datname as database,
    pg_size_pretty(pg_database_size(datname)) as size,
    numbackends as connections
FROM pg_stat_database 
WHERE datname NOT IN ('template0', 'template1');"

if result=$(psql -U "$DB_USER" -d "$DB_NAME" -t -A -F'|' -c "$query" 2>&1); then
    # Convert psql output to JSON
    echo -n '{"status":"success","databases":['
    echo "$result" | awk -F'|' 'NR>1{printf "%s{\"name\":\"%s\",\"size\":\"%s\",\"connections\":%d}", (NR>2?",":""), $1, $2, $3}'
    echo ']}'
else
    echo "{\"status\":\"error\",\"error\":\"$result\"}"
    exit 1
fi
```

---

### FreeBSD (Shell)

#### 1. ZFS Pool Health

```sh
#!/bin/sh
# get-zfs-health.sh
# Check ZFS pool health

pools=$(zpool list -H -o name 2>&1)

if [ $? -ne 0 ]; then
    echo "{\"status\":\"error\",\"error\":\"$pools\"}"
    exit 1
fi

echo -n '{"status":"success","pools":['

first=true
for pool in $pools; do
    health=$(zpool list -H -o health "$pool")
    size=$(zpool list -H -o size "$pool")
    used=$(zpool list -H -o allocated "$pool")
    free=$(zpool list -H -o free "$pool")
    cap=$(zpool list -H -o capacity "$pool")
    
    [ "$first" = "false" ] && echo -n ","
    first=false
    
    echo -n "{\"name\":\"$pool\",\"health\":\"$health\",\"size\":\"$size\",\"used\":\"$used\",\"free\":\"$free\",\"capacity\":\"$cap\"}"
done

echo ']}'
```

#### 2. Jail Status

```sh
#!/bin/sh
# get-jail-status.sh
# Get FreeBSD jail status

if ! command -v jls >/dev/null 2>&1; then
    echo '{"status":"error","error":"jls command not found"}'
    exit 1
fi

jails=$(jls -h jid name host.hostname ip4.addr 2>&1)

if [ $? -eq 0 ]; then
    echo -n '{"status":"success","jails":['
    
    echo "$jails" | tail -n +2 | awk '{
        printf "%s{\"jid\":%d,\"name\":\"%s\",\"hostname\":\"%s\",\"ip\":\"%s\"}", 
            (NR>1?",":""), $1, $2, $3, $4
    }'
    
    echo ']}'
else
    echo "{\"status\":\"error\",\"error\":\"$jails\"}"
    exit 1
fi
```

---

## Testing Scripts

### Local Testing

**Windows:**
```powershell
# Test script directly
powershell.exe -ExecutionPolicy Bypass -File "C:\ProgramData\Agent\Scripts\Get-WindowsUpdates.ps1"

# Parse JSON
$result = & "C:\ProgramData\Agent\Scripts\Get-WindowsUpdates.ps1" | ConvertFrom-Json
$result.status
```

**Linux/FreeBSD:**
```bash
# Test script directly
/opt/agent/scripts/get-docker-status.sh

# Parse JSON
/opt/agent/scripts/get-docker-status.sh | jq .
```

### Remote Testing via NATS

```bash
# Execute script remotely
nats request "agents.device-123.cmd.exec" '{
  "command": "Get-WindowsUpdates.ps1"
}'

# Expected response:
# {
#   "status": "success",
#   "output": "{\"status\":\"success\",\"update_count\":5,...}",
#   "exit_code": 0
# }
```

---

## Security Considerations

### 1. Input Validation

**Never trust input:**
```powershell
# BAD: Unsafe
param([string]$Input)
Invoke-Expression $Input  # DANGEROUS!

# GOOD: Safe
param([string]$ServiceName)
if ($ServiceName -match '^[a-zA-Z0-9_-]+$') {
    Get-Service $ServiceName
}
```

### 2. Script Signing (Optional)

**Windows:**
```powershell
# Sign script with certificate
$cert = Get-ChildItem Cert:\CurrentUser\My -CodeSigningCert
Set-AuthenticodeSignature -FilePath "Get-Updates.ps1" -Certificate $cert
```

### 3. Least Privilege

Run scripts with minimum required permissions:

**Linux:**
```bash
# If possible, use sudo for specific commands only
sudo systemctl status nginx
# Instead of running entire script as root
```

---

## Common Patterns

### 1. Timestamp

**PowerShell:**
```powershell
$timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:ssZ"
```

**Bash:**
```bash
timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
```

### 2. Error Handling

**PowerShell:**
```powershell
$ErrorActionPreference = "Stop"
try {
    # Your code
}
catch {
    @{status="error"; error=$_.Exception.Message} | ConvertTo-Json -Compress
    exit 1
}
```

**Bash:**
```bash
set -e  # Exit on error
trap 'echo "{\"status\":\"error\",\"error\":\"Script failed at line $LINENO\"}"' ERR
```

### 3. Logging

**PowerShell:**
```powershell
# Log to file
$logFile = "C:\ProgramData\Agent\Scripts\script.log"
"$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') - Script started" | Out-File -Append $logFile
```

**Bash:**
```bash
# Log to syslog
logger -t agent-script "Script execution started"
```

---

## Script Library Structure

Organize scripts by category:

```
scripts/
├── monitoring/
│   ├── check-services.ps1
│   ├── check-ports.sh
│   └── check-certificates.sh
│
├── maintenance/
│   ├── cleanup-logs.ps1
│   ├── restart-services.ps1
│   └── update-packages.sh
│
├── inventory/
│   ├── get-installed-software.ps1
│   ├── get-hardware-info.sh
│   └── scan-network.ps1
│
├── backup/
│   ├── verify-backups.ps1
│   └── test-restore.sh
│
└── security/
    ├── audit-users.ps1
    ├── check-firewall.sh
    └── scan-vulnerabilities.sh
```

---

## Debugging

### Enable Verbose Output

**PowerShell:**
```powershell
$VerbosePreference = "Continue"
Write-Verbose "Checking service status..."
```

**Bash:**
```bash
set -x  # Enable debug mode
echo "Checking service status..." >&2  # Stderr for debugging
```

### Check Agent Logs

**Windows:**
```powershell
Select-String -Path "C:\ProgramData\Agent\agent.log" -Pattern "exec"
```

**Linux/FreeBSD:**
```bash
grep exec /var/log/agent/agent.log
```

---

## Performance Tips

1. **Use native tools**: Avoid spawning extra processes
2. **Filter early**: Don't retrieve all data then filter
3. **Limit output**: Return only what's needed
4. **Cache results**: If data doesn't change often
5. **Async operations**: Use background jobs carefully

---

## Next Steps

- **[Architecture](architecture.md)** - Understand how scripts fit in
- **[Configuration](configuration.md)** - Configure script execution
- **[Security](security.md)** - Harden your scripts
- **[Troubleshooting](troubleshooting.md)** - Debug script issues

---

**Share your scripts!** Contribute to the community script library on [GitHub](https://github.com/stone-age-io/agent/discussions)
