# Nebula Overlay Guide

Running the agent as a host on your organization's [Nebula](https://github.com/slackhq/nebula) mesh.

## Overview

The agent can run a Nebula overlay host inside itself. The config comes from the
stone-age.io platform — the `nebula_host` record linked to this agent's thing —
and the agent re-reads it on an interval.

That re-reading is the point of the feature, not a detail of it. **Nebula has no
certificate revocation list.** Refusing a certificate the CA already signed means
listing its fingerprint in `pki.blocklist` in every *other* host's config, so a
revoked device stays on the mesh until each peer has re-read its own config.
Certificate renewal and CA rotation work the same way. An agent doing this on a
schedule is what makes those operations actually take effect across a fleet.

The feature is **off by default**. An agent that leaves the `nebula` block alone
behaves exactly as it did before the feature existed.

For the design reasoning — why the agent embeds Nebula rather than supervising it,
and what was rejected — see [nebula-design.md](nebula-design.md).

## Prerequisites

- **A `nebula_host` record on the platform, linked to this agent's thing.** An
  owner or admin creates it (UI → *Nebula* → *Hosts*) and assigns it to the thing
  through the thing's `nebula_host` field.
- **A top-level `platform:` block**, for `source: "platform"`. The agent reads its
  Nebula config with the same platform identity it uses for its NATS credential,
  so the same block serves both. Use `source: "file"` if you do not have one.
- **Privileges to create a network device.** The agent already runs as root or
  SYSTEM as a service, so this is usually already true.
- **Nebula 1.10 or newer on every other host in the mesh.** `pb-nebula` issues v2
  certificates, and older builds cannot validate them. They do not fail loudly —
  they simply never complete a handshake.
- **Windows only: `wintun.dll`.** See [Windows](#windows) below.

## Configuration

```yaml
nebula:
  enabled: true
  source: "platform"
  cache_file: "/var/lib/agent/nebula-cache.yaml"
  sync_interval: "10m"
  verify_timeout: "30s"
```

| Key | Default | Notes |
|---|---|---|
| `enabled` | `false` | Off unless set. |
| `source` | `"platform"` | `"platform"` or `"file"`. |
| `config_file` | — | Required for `source: "file"`. |
| `cache_file` | per-OS | Last config known to have reached the mesh. Written `0600`. |
| `sync_interval` | `"10m"` | 1m–1h. **This is the revocation latency.** |
| `verify_timeout` | `"30s"` | 5s–5m. How long a new config has to reach a lighthouse. |

### Choosing `sync_interval`

Treat it as a security setting rather than a performance one. It is how long this
device will keep honouring a certificate that has already been revoked on the
platform, because that is how long it may go before re-reading the blocklist.
Ten minutes is the default. An hour is the maximum the agent will accept.

The interval is not the only path — `cmd.nebula` with `sync` pulls immediately —
but it is the one that works without the command channel, which matters when the
command channel is itself riding on the mesh.

### `source: "file"`

```yaml
nebula:
  enabled: true
  source: "file"
  config_file: "/etc/agent/nebula.yaml"
  verify_timeout: "30s"
```

Reads a Nebula config from disk and runs it. It does **not** poll, cache, or roll
back, and `sync_interval` does not apply. It exists so the feature is usable
without the platform, and so a device can join the mesh before it has a thing
record. A file-sourced agent does not converge on its own: revocation, renewal and
CA rotation all become your problem again.

## Behavior

### Startup

1. Fetch the config from the source.
2. Start Nebula on it.
3. If that fails, fall back to the cached config from the last successful run.
4. If there is no usable config, log the error and carry on without the overlay.

An unreachable platform at startup is a warning, not a failure. The agent keeps
running — quite possibly on a NATS connection that does not need the mesh — and
the scheduled sync retries.

### Adopting a new config

Every `sync_interval`, the agent asks the platform whether its `nebula_host`
record has changed, sending only a revision probe. The config itself is fetched
only when that revision has moved, because it contains the host's private key
inline.

When a new config arrives:

1. Reload Nebula in place.
2. If the overlay does not come back, restart Nebula on the new config.
3. If it still does not come back, roll back to the last config that worked.

Step 3 is why a bad config cannot take a fleet off the network. A rolled-back
agent reports `rolled_back: true` in its health response and keeps trying at each
sync.

"Does not come back" means no lighthouse handshake within `verify_timeout`, and
is only checked when the config names a lighthouse to reach. A lighthouse has
nobody to hand shake with, and rolling back a good config because peers happened
to be offline would cause the outage the rollback exists to prevent.

### Reporting

`cmd.health` carries a `nebula` block:

```json
{
  "nebula": {
    "enabled": true,
    "running": true,
    "source": "platform",
    "overlay_ip": "10.128.0.100",
    "tunnels": 3,
    "lighthouse_up": true,
    "cert_expires_at": "2027-09-12T00:00:00Z",
    "config_revision": "2026-09-12 14:03:11.482Z",
    "last_sync": "2026-09-12T14:10:00Z"
  }
}
```

`config_revision` is the `updated` value of the `nebula_host` record the agent is
running. Comparing it against what the platform currently holds answers *"has
this revocation actually reached this device?"* — across a whole fleet, from the
same channel as everything else.

An agent whose overlay is enabled but not carrying traffic reports `degraded`.
It never reports `unhealthy` for a mesh problem: telemetry and commands are
unaffected.

## Commands

`{prefix}.{code}.cmd.nebula`:

```json
{"action": "sync"}
```

| Action | Effect |
|---|---|
| `sync` | Pull from the platform now and apply if the revision moved. |
| `restart` | Bounce Nebula on the config already running. Does not re-fetch. |

Both reply `{"status": "accepted"}` **before** doing the work, and the reply does
not tell you whether it succeeded. That is deliberate: when NATS rides the
overlay, both actions interrupt the tunnel the request arrived through, so a
reply sent afterwards would never arrive and you would see a timeout on an
operation that worked. **Read the result from `cmd.health`.**

There is no `stop`. On an agent whose NATS rides the overlay it would sever the
only channel that could deliver a `start`. To turn the overlay off, set
`nebula.enabled: false` and restart the agent.

## Gateways and unsafe routes

If the `nebula_host` record carries `unsafe_networks`, this agent can act as a
gateway into the networks it is attached to. The agent does nothing special for
this — Nebula tunnels the packet, its own firewall authorizes it, and the kernel
forwards it — but **three things are your responsibility**:

- **Enable IP forwarding.** Nebula does not set `net.ipv4.ip_forward`, and neither
  does the agent. The agent deliberately does not modify system settings.
- **Provide a return path.** A peer at `10.128.0.5` reaches `192.168.1.50`; that
  host replies to `10.128.0.5` and has no route for it. Either the LAN gateway
  needs a route for the overlay CIDR, or the agent's host needs to masquerade.
  This is the most common reason unsafe routes appear not to work.
- **Use Linux or FreeBSD.** Windows forwarding is `IPEnableRouter` in the registry
  plus the RemoteAccess service, not a sysctl. A Windows gateway is possible but
  unsupported.

`cmd.health` reports the `unsafe_networks` in the live certificate, so you can
confirm the platform issued the gateway grant it was meant to.

## Windows

Nebula uses [Wintun](https://www.wintun.net/) for its network device, and loads
`wintun.dll` from a fixed path relative to the agent executable:

```
<agent directory>\dist\windows\wintun\bin\amd64\wintun.dll
```

**The agent does not ship this DLL.** Download the Wintun release, and place the
`bin\amd64\wintun.dll` from it at that path. If it is missing, the agent logs a
start failure naming the exact path it looked in.

## Troubleshooting

### "thing record has no nebula_host assigned"

The thing exists on the platform but nothing is linked to its `nebula_host`
field. An owner or admin sets it; members cannot.

### "nebula host has no config"

The `nebula_host` record exists but `config_yaml` is empty. Usually the host is
inactive, or its network has no CA. Check the host in the platform UI.

### "no lighthouse reachable within 30s"

Nebula started but could not hand shake. The agent will have rolled back if it
had a previous config. Common causes: the CA changed and this host still holds a
certificate from the old one, the lighthouse is down or unreachable at its
`public_host_port`, or UDP is blocked between here and there.

### "start nebula: ... If this is a Wintun error"

See [Windows](#windows).

### Nebula starts but `tunnels` stays 0

The overlay is up and this host is alone on it. Check that peers are running and
that none of them are running Nebula older than 1.10 — a pre-1.10 host cannot
validate the v2 certificates `pb-nebula` issues, and fails silently.

### `rolled_back: true` in the health response

The agent is running an older config because the current one from the platform
could not reach the mesh. It re-tries every sync, so this clears on its own once
the platform holds a config that works. Fix the config on the platform; the agent
will not adopt a broken one.
