# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) — with the pre-1.0
caveat that a minor version may break something. Pin what you deploy.

History before `0.1.0` is not reconstructed here; `git log` is the record for
that period, and this file starts where the versioned releases do.

## [0.2.1] - 2026-09-19

### Fixed

- **A declared mirror whose hub bucket does not exist no longer reports itself
  healthy.** `ensureMirror` never asked the hub anything, and JetStream cannot
  validate a cross-domain mirror source when the stream is created — so a mirror
  naming a bucket the hub does not have was accepted, reported
  `agent_edge_sync_up = 1`, and received nothing for ever. It is now checked on
  every start and reported as down, with the bucket named.

- **`sync.twin: true` works on a fresh organization again.** The same omission
  dropped hub-side creation for the preset's `twin_desired`: 0.2.0 created the
  hub bucket on the relay path only, so a deployment whose console had not
  already created `twin_desired` mirrored a stream that was not there. This is
  the more likely of the two to bite, because `sync.twin: true` is the documented
  path.

  Both were one omission — only the relay path asked the hub anything. Both
  directions now go through a single `hubBucket()`, which is also the only place
  that decides whether the agent may create a bucket at the hub: the two preset
  names yes, a user-declared one never.

## [0.2.0] - 2026-09-19

> Building from source now needs **Go 1.26+**: the Nebula library sets the floor.
> CI and the release pipeline read the version from `go.mod`, so they follow on
> their own.

### Added

- **The agent can now be a site's NATS leaf node.** This absorbs `leaf-sync`,
  which used to be a separate binary in the platform repository. Four capabilities
  arrived together, each independently switchable:

  - `agent -leaf-config` — a one-shot that authenticates as this agent's Thing,
    calls `GET /api/me/leaf-config`, and writes `nats-leaf.conf` (0644) beside its
    creds (0600). It has to be separable from running: the usual edge shape is a
    separately supervised `nats-server`, and that server needs its config file
    before it starts, which is before the agent has anything to connect to.
  - `nats.server_config` — host a `nats-server` in this process from that file.
    It points at *any* nats-server config, not only a generated one, so it is
    equally how you run a plain embedded broker on a box with no platform at all.
    `nats.urls` must name the port it listens on; startup refuses a disagreement.
  - `sync:` — keep KV buckets in step with the hub, so the site keeps deciding
    locally through a WAN outage. Two directions, two mechanisms, a list each:
    `sync.mirrors` are maintained by the server (hub → edge), `sync.relays` are
    pumped by the agent (edge → hub), and `sync.twin: true` is the preset for the
    two digital-twin buckets. Each entry takes an optional `keys:` **key
    pattern** — `line-a.>`, never `$KV.recipes.line-a.>` — which the agent turns
    into a mirror's subject filter or a relay's filtered watcher.

    A bucket may appear in one list or the other, **never both**: two writers on
    one bucket do not converge, they oscillate. That used to be structural, with
    two built-in buckets and no way to say anything else; it is now a check that
    refuses to start and names the bucket.

    A `keys:` on a **mirror** cannot be changed afterwards — nats-server rejects
    any change to a mirror block on an existing stream
    (`JSStreamMirrorNotUpdatableErr`), so narrowing one later means deleting and
    recreating the bucket at every site.

    The agent creates the local side of a declared bucket and never the hub side;
    the two preset twin buckets, whose shape the platform knows, are the
    exception. Design record:
    **[docs/edge-sync-design.md](docs/edge-sync-design.md)**.
  - `observability.addr` — serve `/ready` and `/metrics` on the box. `cmd.health`
    travels over NATS, which is the link that breaks; the box you most need to ask
    is the one whose uplink is down, and that is when it goes quiet.

  **There is no `edge.enabled` key and no gateway flag.** "Gateway" is not a mode
  the config declares — it is the sum of the capabilities it turns on, and any of
  them means the edge goroutine has a reason to exist. A single flag naming the
  role would be a second control that can disagree with the first:
  `edge.enabled: false` beside `sync.twin: true` has no correct behaviour.

  On the platform side a gateway is just a **Thing**; the `leaf_nodes` collection
  is gone. `GET /api/me/leaf-config` is bound to `things`, takes no record id, and
  gates on nothing — everything it serves is either public trust material (the
  operator, account and `$SYS` account JWTs, which every server validates anyway)
  or the caller's own credential, which it must already hold to connect.

  `nats-server` links into the binary whether or not you configure one, so a
  scanner flagging a `nats-server` CVE against this build is reporting code that
  does not run unless `nats.server_config` is set. New docs:
  **[docs/leaf-node.md](docs/leaf-node.md)**.

- **An embedded Nebula overlay host**, off by default (`nebula.enabled`). The agent
  fetches its Nebula config from the stone-age.io platform — the `nebula_host`
  related to its own thing — runs Nebula in-process, and re-reads the config on an
  interval so revocation, certificate renewal and CA rotation actually reach the
  device. Nebula has no CRL, so a mesh only converges if its members re-read their
  configs; `nebula.sync_interval` is therefore the revocation latency for the
  device, not a tuning knob. `nebula.source: "file"` reads a config from disk
  instead, for deployments without the platform.
- A newly applied Nebula config that cannot reach a lighthouse is restarted and
  then rolled back to the last config known to have worked, so a bad config
  cannot take a fleet off the network. The last good config is cached locally, so
  a device that reboots while the platform is unreachable still comes up on the
  overlay.
- **`cmd.nebula`** with `sync` and `restart`. It answers `accepted` and then acts,
  because both actions can interrupt the tunnel the request arrived through; the
  outcome is reported through `cmd.health`. There is deliberately no `stop`.
- **`cmd.health` gained a `nebula` block** — tunnel count, lighthouse reachability,
  certificate expiry, the unsafe networks in the live certificate, and the config
  revision currently running. Comparing that revision with the platform answers
  whether a revocation has landed on a device. Absent when the overlay is off.

### Changed

- **BREAKING: the platform relationship moved to a top-level `platform:` block,**
  and `auth.type: "stone-age"` became `auth.type: "platform"`.

  ```yaml
  # before                          # after
  nats:                             platform:
    auth:                             url: "https://platform.example.com"
      type: "stone-age"               identity: "thing@example.com"
      stone-age:                      password_env: "AGENT_PLATFORM_PASSWORD"
        url: ...                      sync_interval: "24h"
        identity: ...
        password_env: ...           nats:
  tasks:                              auth:
    creds_sync:                         type: "platform"
      enabled: true                     creds_file: "/etc/agent/device.creds"
      interval: "24h"
  ```

  Three subsystems read the platform relationship — the NATS credential
  lifecycle, the Nebula config source, and the new leaf bootstrap — and with it
  buried under `nats.auth` the other two had to reach across sections to ask
  whether the platform was configured at all. "Is the block present" is a better
  question than "is some other section's type field set to a particular string".
  The rename follows: `nebula.source` already said `"platform"` for the same
  idea, so the two spellings were one idea with two names.

  `tasks.creds_sync` is gone, replaced by `platform.sync_interval` (same 1h–72h
  range, same default). It had an `enabled` flag that is not a state worth being
  able to express: an agent told to fetch its credentials from the platform but
  not to keep them current is a device that stops working in seven days.

  Update `config.yaml` before upgrading — the old keys are not read.

- **The agent now opens a listening socket, which 0.1.0 did not.**
  `observability.addr` defaults to `127.0.0.1:9100`, so upgrading starts serving
  `/ready` and `/metrics` on loopback without anything being configured. Set
  `observability.addr: ""` to keep the old behaviour; the checks still run and
  still log either way.

  Worth calling out on its own because 0.1.0 was described — here and in every
  install guide — as having **no listening ports**, and firewall rules may have
  been written against that. Nothing about it is reachable off the box by
  default, and nothing can *instruct* the agent through it; the endpoints are
  read-only. Two caveats:

  - **9100 is `node_exporter`'s default port** on Linux and FreeBSD. On a box
    running both, move one — a bind failure is logged, not fatal, so the symptom
    is an endpoint that quietly never came up. `windows_exporter` uses 9182.
  - **Loopback is not an authorization boundary** on a box with other users. Set
    `observability.metrics_token` (Bearer, or Basic with any username) before
    moving `addr` anywhere else.

- **The agent no longer refuses to start when NATS is unreachable.** `nats.Connect`
  now retries in the background instead of failing construction, and the JetStream
  check moved from a one-shot fatal probe at startup to a check that runs on every
  connect and reports through `cmd.health`. A misconfigured agent still fails fast
  — an unknown auth type or unreadable TLS material is a configuration error, and
  unreachability is not.
- **`cmd.health` gained `nats.jetstream`**, and reports `degraded` when the agent
  is connected but JetStream is unusable. This replaces the guarantee the old
  startup probe gave: telemetry failing silently is still refused, it is just
  reported rather than fatal.
- A NATS connection that `nats.go` abandons for good — two consecutive
  authorization failures, which is what a revoked credential looks like — now
  exits the agent instead of leaving it running against a dead connection. This
  restores the restart-and-re-sync recovery path that the retry change would
  otherwise have removed.

## [0.1.0] - 2026-08-22

First tagged release, with prebuilt binaries for all four supported targets. The
agent has been running on real hosts for months; this tag marks the point at
which it is packaged for other people to install.

### Added

Summarising the state at first tag rather than the path to it:

- **A single outbound-only daemon** for Linux, Windows and FreeBSD, installing
  itself as a systemd unit, a Windows service or an rc.d script
  (`agent -service install`). No listening ports.
- **Platform-managed credentials** (`auth.type: "stone-age"`). The agent
  authenticates to the Control Plane as its own Thing, writes the `.creds` file
  from the linked `nats_users` record at `0600`, and thereafter renews its
  session and adopts a re-minted credential on its own — so a device never has a
  credential hand-delivered to it. Manual `.creds`, token and user/password auth
  remain for pre-distributed and development setups.
- **Telemetry.** CPU, memory, disk usage and I/O from built-in collection
  (gopsutil) or from `node_exporter` / `windows_exporter` when
  `source: "exporter"` is set. Published over JetStream for durability, with
  liveness heartbeats on core NATS, where last-write-wins is the point and replay
  is not.
- **Service control and monitoring** — start, stop, restart and status through
  the host's own service manager, with a timeout so a hung manager cannot block
  the command handler.
- **Whitelisted command and script execution**, output capped at 10 MB, plus
  on-demand log retrieval and hardware/OS inventory.
- **Wire format aligned with the platform's conventions**: `code` and `location`
  as identity, `ts` timestamps, and subjects that a rule-router rule or a
  console dashboard can consume without translation.

### Fixed

- The compiled-in version defaulted to `1.0.0`, so an unstamped build reported a
  release number it was not. It now defaults to `dev`, and releases stamp the
  real version — which matters because this string travels in every heartbeat and
  command reply, not just on the command line.

### Notes

- `-version` prints the version and exits, so a host can be asked what it is
  running without starting the agent.
- Releases are cut by goreleaser from a pushed `v*` tag. The makefile remains for
  local and development builds.

[Unreleased]: https://github.com/stone-age-io/agent/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/stone-age-io/agent/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/stone-age-io/agent/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/stone-age-io/agent/releases/tag/v0.1.0
