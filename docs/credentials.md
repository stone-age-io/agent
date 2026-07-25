# Platform Credentials Guide

Fetch, renew, and rotate NATS credentials from the stone-age.io platform.

---

## Overview

On the stone-age.io platform, an agent is a **Thing**. With `auth.type: "stone-age"` the agent manages its own NATS `.creds` file against the platform, so credentials never have to be distributed to devices by hand.

Three operations, in the order a device meets them:

| | When | What it does |
|---|---|---|
| **Bootstrap** | First start, `.creds` missing | Logs in with the thing's password, writes `.creds` |
| **Sync** | Every start, then every `creds_sync.interval` | Renews the platform session token, adopts a credential the platform re-minted |
| **Rotate** | On the `cmd.rotate_creds` command | Asks the platform to re-mint the credential, adopts it, reconnects |

**Security model:** each device authenticates as its own thing — there is no shared service account. The platform's access rules guarantee an authenticated thing sees only its own record and only its assigned NATS identity, so a compromised device cannot read another device's credentials.

---

## Prerequisites

- A running stone-age.io platform instance, reachable over HTTPS
- A `things` record for the device with:
  - `code` matching the agent config's `code`
  - a login email and password (`things` is a password-auth collection)
  - a `nats_user` relation assigned, with credentials generated in its `creds_file` field
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
    type: "stone-age"
    creds_file: "/etc/agent/device.creds"
    stone-age:
      url: "https://platform.example.com"
      identity: "server-prod-01@things.example.com"
      password_env: "AGENT_PLATFORM_PASSWORD"

tasks:
  creds_sync:
    enabled: true
    interval: "24h"
```

### Configuration Reference

| Field | Required | Description |
|-------|----------|-------------|
| `url` | Yes | Platform base URL. Must be `https://` unless `allow_insecure_url` is set |
| `identity` | Yes | The thing's login email |
| `password_env` | Until bootstrapped | Name of the env var holding the thing's password — see [After the first boot](#after-the-first-boot) |
| `session_file` | No | Where the platform session is stored. Defaults to `platform-session.json` beside `creds_file` |
| `allow_insecure_url` | No | Permits a plain `http://` platform URL. Development only |

| Task field | Default | Description |
|------------|---------|-------------|
| `creds_sync.enabled` | `true` | Ignored unless `auth.type` is `stone-age` |
| `creds_sync.interval` | `24h` | Range 1h–72h. Must stay well inside the platform's session token TTL (7 days) |

The collection (`things`), the credential source (`nats_user` relation → `creds_file`), and the identity checks are fixed — the agent is opinionated about the platform schema.

---

## Setting the Environment Variable

The thing's password is read from an environment variable, never stored in the config file.

### Linux (systemd)

```bash
sudo systemctl edit agent
```

Add:
```ini
[Service]
Environment="AGENT_PLATFORM_PASSWORD=your-password-here"
```

Then reload and restart:
```bash
sudo systemctl daemon-reload
```

```bash
sudo systemctl restart agent
```

### Windows

```powershell
[Environment]::SetEnvironmentVariable("AGENT_PLATFORM_PASSWORD", "your-password-here", "Machine")
```

```powershell
Restart-Service agent
```

### FreeBSD

```bash
sudo sysrc agent_env="AGENT_PLATFORM_PASSWORD=your-password-here"
```

```bash
sudo service agent restart
```

---

## Behavior

### First Start
1. Agent detects `auth.type: "stone-age"` and finds no `.creds` file
2. Reads the password from the env var named by `password_env`
3. Authenticates as the thing (`POST /api/collections/things/auth-with-password?expand=nats_user,location`)
4. Verifies the thing record's `code` equals the agent config's `code` — **fails** on mismatch
5. Warns if the expanded location's `code` differs from the config's `location`
6. Writes the credential from `expand.nats_user.creds_file` to `creds_file` with `0600` permissions
7. Stores the session token and the credential's revision in `session_file` (`0600`)
8. Connects to NATS

### Subsequent Starts
1. The `.creds` file exists, so bootstrap is skipped
2. One credential sync runs before connecting — best-effort, so an unreachable platform never stops a working agent from starting
3. Connects to NATS

If the `.creds` file is missing but the session file is intact, the agent restores the credential using its stored session and does **not** need the password — so deleting `.creds` on its own is a safe way to force a re-fetch.

### Credential Sync
Runs once at startup and then on `creds_sync.interval`:

1. `POST /api/collections/things/auth-refresh` with the stored token → a fresh token, renewing its TTL
2. `GET /api/collections/nats_users/records/{id}?fields=updated` → the credential's revision
3. If the revision is unchanged, stop here
4. Otherwise read the full record, write the new `.creds`, and force a NATS reconnect to pick it up

**Why two calls:** a `.creds` file embeds the nkey seed — a private key. Asking for `expand=nats_user` would return the whole credential on every sync, and PocketBase does not apply `?fields=` to auth responses, so the expand cannot be trimmed. Refreshing without an expand and probing only `updated` keeps key material off the network except when it has actually changed.

If the stored token has lapsed, the agent falls back to password authentication for that run.

### After the first boot

Once the agent holds a session token, it no longer needs the thing's password. Removing `AGENT_PLATFORM_PASSWORD` from the service environment is supported and reduces what a compromised device gives up: a 0600 token file is narrower than a machine-level environment variable, which on Windows any local process can read.

The tradeoff: the platform's session token for a thing lives **7 days**, renewed on each sync. A device that is powered off or offline for longer than that comes back with a dead token, and without a password it cannot recover on its own — re-provision it by setting the env var once, or delete `.creds` and let it bootstrap again.

Devices that are frequently offline should keep the password configured.

---

## Rotation

### On demand (from the platform side)

Press **Regenerate** on the thing's NATS identity in the platform UI. Each device adopts the new credential on its next sync — within `creds_sync.interval`.

### On demand (from the device side)

```
{prefix}.{code}.cmd.rotate_creds
```

Empty request body. The agent asks the platform to re-mint its credential (`POST /api/me/nats-creds/rotate`), writes the result, replies, and then reconnects:

```json
{"status": "success", "changed": true, "ts": "2026-07-25T12:00:00Z"}
```

`changed: false` means the platform handed back the credential the agent already had. An agent that is not platform-managed answers with `status: "error"` rather than timing out.

**Rotation is not revocation.** The previous credential stays valid until it expires or an operator revokes it on the platform (the `revoke` field on the NATS identity). Rotating after a suspected compromise does not lock the old credential out.

### Recovering a revoked device

No manual step is needed. The sync path never touches NATS, so it keeps working while the NATS connection does not:

1. NATS rejects the revoked credential and the agent exits
2. The service manager restarts it (`Restart=always` on systemd, recovery actions on Windows)
3. The startup sync adopts the current credential, and the agent connects

---

## Troubleshooting

### "environment variable AGENT_PLATFORM_PASSWORD is not set or empty"
The env var named by `password_env` is not set in the **service** environment (setting it in your shell is not enough).

### "no password_env configured and no usable platform session token"
The password was removed from the environment and the stored session token has lapsed or was rejected. Set the env var again, or delete `.creds` and `platform-session.json` to re-bootstrap.

### "stone-age.url must be https://"
The platform URL is plain HTTP. Bootstrap sends the thing's password and receives a private key, so this is refused unless `allow_insecure_url: true` is set for development.

### "authentication failed: platform returned 400"
The thing's login email (`identity`) or password is wrong, or the thing record does not exist / has no password set.

### "code mismatch: config has '...' but the platform thing record has '...'"
The agent authenticated as a thing whose `code` doesn't match its config. Either the config's `code` is wrong or the device was given another thing's login. This check prevents a device from publishing telemetry under the wrong identity — it is enforced on every authentication, not just the first.

### "thing record has no NATS credentials" / "NATS identity has no credentials"
No `nats_user` is assigned to the thing, or the assigned identity has no generated `creds_file`. Assign one on the platform and generate its credentials.

### "thing record has no nats_user assigned"
The thing exists but its `nats_user` relation is empty. Only an owner or admin can set it.

### "platform did not confirm the rotation"
The rotation route answered without `rotated: true`. Check the platform logs; the identity may be revoked (setting `regenerate` on a revoked identity re-enables it, which is deliberately an owner/admin action).

### Credential sync warnings in the log
Sync failures are warnings, not fatal errors: the credential on disk is usually still valid and the next run retries. Persistent failures mean the platform is unreachable or the session has lapsed with no password available.
