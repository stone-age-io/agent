# Platform Bootstrap Guide

Auto-provision NATS credentials from the stone-age.io platform on first agent start.

---

## Overview

On the stone-age.io platform, an agent is a **Thing**. The bootstrap feature lets the agent fetch its own NATS `.creds` file from the platform on first startup, eliminating the need to manually distribute credential files to each device.

**How it works:**
1. Agent starts with `auth.type: "pocketbase"`
2. If `.creds` file already exists, bootstrap is skipped
3. Agent authenticates **as its thing** against the platform's `things` auth collection (single `auth-with-password` call with `expand=nats_user,location`)
4. Agent verifies the thing record's `code` matches the agent config's `code` (fail-fast on mis-provisioned devices)
5. Agent reads the NATS credentials from the expanded `nats_user` relation's `creds_file` field
6. Agent writes the `.creds` file to disk with restrictive permissions
7. Agent switches to `creds` auth and connects to NATS normally

After the initial bootstrap, the platform is no longer needed at runtime.

**Security model:** each device authenticates with its own thing credential — there is no shared service account. The platform's access rules guarantee an authenticated thing can see only its own record and only its assigned NATS user, so a compromised device cannot read any other device's credentials.

---

## Prerequisites

- A running stone-age.io platform instance (accessible over HTTPS in production)
- A `things` record for the device with:
  - `code` matching the agent config's `code`
  - a login email and password (things is a password-auth collection)
  - a `nats_user` relation assigned, with generated credentials in its `creds_file` field
  - optionally a `location` relation (the agent warns if it differs from the config's `location`)

---

## Agent Configuration

### Minimal Configuration

```yaml
code: "server-prod-01"
location: "hq"

nats:
  urls: ["nats://nats.example.com:4222"]
  auth:
    type: "pocketbase"
    creds_file: "/etc/agent/device.creds"
    pocketbase:
      url: "https://platform.example.com"
      identity: "server-prod-01@things.example.com"
      password_env: "AGENT_PB_PASSWORD"
```

### Configuration Reference

| Field | Required | Description |
|-------|----------|-------------|
| `url` | Yes | Platform (PocketBase) base URL |
| `identity` | Yes | The thing's login email |
| `password_env` | Yes | Name of environment variable containing the thing's password |

The collection (`things`), the credentials source (`nats_user` relation → `creds_file`), and the identity checks are fixed — the agent is opinionated about the platform schema.

---

## Setting the Environment Variable

The thing's password is read from an environment variable (never stored in config files).

### Linux (systemd)

```bash
# Create a systemd override
sudo systemctl edit agent
```

Add the following:
```ini
[Service]
Environment="AGENT_PB_PASSWORD=your-password-here"
```

Then reload and restart:
```bash
sudo systemctl daemon-reload
sudo systemctl restart agent
```

### Windows

```powershell
# Set machine-level environment variable
[Environment]::SetEnvironmentVariable("AGENT_PB_PASSWORD", "your-password-here", "Machine")

# Restart agent service to pick up the variable
Restart-Service agent
```

### FreeBSD

```bash
# Add to rc.conf environment
sudo sysrc agent_env="AGENT_PB_PASSWORD=your-password-here"

# Restart service
sudo service agent restart
```

---

## Behavior

### First Start
1. Agent detects `auth.type: "pocketbase"`
2. Checks if `creds_file` path already exists - if it does, skips bootstrap
3. Reads password from the environment variable specified by `password_env`
4. Authenticates as the thing (POST `/api/collections/things/auth-with-password?expand=nats_user,location`)
5. Verifies the thing record's `code` equals the agent config's `code` - **fails** on mismatch
6. Warns if the expanded location's `code` differs from the agent config's `location`
7. Extracts the `.creds` content from the expanded `nats_user.creds_file` and writes it to `creds_file` with `0600` permissions
8. Switches auth type to `"creds"` internally and proceeds to connect to NATS

### Subsequent Starts
- The `.creds` file already exists, so bootstrap is skipped entirely
- The platform is not contacted
- The agent connects to NATS using the stored `.creds` file

### Credential Rotation
If NATS credentials need to be rotated:
1. Regenerate the nats_user credentials on the platform
2. Delete the existing `.creds` file on the agent
3. Restart the agent - it will re-bootstrap and fetch the new credentials

---

## Troubleshooting

### "environment variable AGENT_PB_PASSWORD is not set or empty"
The environment variable specified in `password_env` is not set or is empty. Ensure it is set in the service environment (not just in your shell session).

### "authentication failed: auth returned 400"
The thing's login email (`identity`) or password is wrong, or the thing record does not exist / has no password set on the platform.

### "code mismatch: config has '...' but the platform thing record has '...'"
The agent authenticated successfully but as a thing whose `code` doesn't match the agent config. Either the config's `code` is wrong, or the device was given another thing's login. Fix one of them before starting - this check prevents a device from publishing telemetry under the wrong identity.

### "thing record has no NATS credentials"
The thing has no `nats_user` relation assigned, or the assigned nats_user has no generated `creds_file` content. Assign a NATS user to the thing on the platform and ensure its credentials are generated.

### Bootstrap runs but NATS connection fails
The `.creds` content stored on the platform may be invalid or stale. Verify the nats_user's `creds_file` is a valid NATS credentials file, regenerate if needed, then delete the local `.creds` file and restart the agent.
