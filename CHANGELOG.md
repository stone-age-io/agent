# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) — with the pre-1.0
caveat that a minor version may break something. Pin what you deploy.

History before `0.1.0` is not reconstructed here; `git log` is the record for
that period, and this file starts where the versioned releases do.

## [Unreleased]

> **Security fix. Upgrade any agent with a `commands.scripts_directory`.**
> Anyone able to publish to `cmd.exec` could run arbitrary commands on the
> device, whatever the allowlist said, as long as one script existed in the
> scripts directory.
>
> Script requests must now be a bare filename (`deploy.sh`, not
> `/opt/agent/scripts/deploy.sh`). A request that used the full path is now
> refused, where it used to work.

### Security

- **`cmd.exec` ran the caller's string instead of the script it approved.**
  The script check reduced the request to its last path element and confirmed
  a script by that name existed, then handed the *unreduced* request to
  `bash -c` (or `powershell -Command`). So `{"command": "$(anything)/deploy.sh"}`
  passed the check and ran `anything`. Both platforms were affected.

  Script requests must now be a bare filename, and the agent builds the path
  itself and starts that file directly, with no shell involved. Allowlisted
  commands now run the operator's allowlist entry rather than the request.
  They used to compare equal after whitespace normalization, which let a
  newline in the request split one allowed line into two commands.

### Fixed

- **A failed `cmd.exec` returns its output and exit code.** A command that ran
  and exited non-zero replied with only `"error": "command exited with code
  1"`, and the stderr explaining why never left the box. The reply keeps
  `status: "error"`, so nothing checking status changes, and now carries
  `output` and `exit_code` as well. A timed-out command returns what it had
  printed before it was killed.

- **`commands.timeout` is a real bound.** When the timeout killed a shell, a
  child still holding the output pipe (a `sleep`, say) kept the reply waiting
  until that child finished. A script that exited while leaving a background
  process behind held the reply for as long as that process lived. Output is
  now collected for at most one second after the command exits or is killed.

- **`cmd.logs` reads what `allowed_log_paths` allows.** A substring denylist
  in front of the allowlist refused any path containing `sam`, `system32`,
  `.exe`, `.dll`, `.sys` or `..`, whatever the operator had allowed. That
  meant `/var/log/samba/*`, any home directory containing "sam", and
  `app..log` could never be read. A check behind the allowlist only
  understood `*`, so every pattern using `?` or `[...]` matched nothing. Both
  are gone. Neither refused anything the allowlist would have let through:
  the request must still exactly equal a file an allowed pattern names.

  If you allowlisted a broad pattern and relied on the denylist to carve
  pieces out of it, narrow the pattern.

### Removed

- **Exporter metrics mode.** `tasks.system_metrics.source: "exporter"` and
  `exporter_url` read CPU, memory and disk figures by scraping node_exporter
  or windows_exporter instead of using the builtin gopsutil collector. Nothing
  used it, and it was a second implementation of the same figures, with
  per-platform metric-name tables to keep correct. The builtin collector is
  now the only one. A config that still carries either key loads unchanged
  (they are ignored) and gets builtin metrics, in the same payload shape. If
  you want node_exporter's series, run it and have Prometheus scrape it
  directly.

### Changed

- **`cmd.exec`'s `exit_code` is present exactly when the command ran**, 0
  included. It used to be dropped on success and never sent on failure.
  Absent now means the command never started (refused, not found, or timed
  out).

- **Windows scripts run with `-File` instead of `-Command`.** A script's own
  `exit N` is now the exit code `cmd.exec` reports. Under `-Command` it
  collapsed to 0 or 1.

## [0.3.0] - 2026-09-20

> **`cmd.health` reports `degraded` in situations where it used to report
> `healthy`.** Its status word is now derived from the readiness checks rather
> than from a separate list of conditions, so an islanded gateway, a bucket
> that will not sync, a rolled-back overlay or a platform sync that has stopped
> working all reach it. None of these are new faults — they were happening and
> going unreported over NATS. Anything alerting on `status != "healthy"` will
> get louder, and should. `/ready` is unaffected: warnings still answer 200.
>
> The other upgrade-visible change is that an agent with no leaf node no longer
> opens a second NATS connection. If you count connections per device at the
> server, expect that number to drop.

### Fixed

- **A plain device no longer starts the edge subsystem.** `observability.addr`
  defaults to `127.0.0.1:9100`, and it was one of the three disjuncts in
  `edgeEnabled()` — so every agent ever deployed ran the edge, whether or not
  it had a leaf node. That meant a second NATS connection built from nothing
  but a `.creds` file, ignoring `token`/`userpass` auth and the entire
  `nats.tls` block, plus three readiness checks about a leaf the box did not
  have.

  On a creds-authenticated device the effect was a duplicate idle connection
  per agent and a `nats_local` check describing the hub as "the local leaf". On
  a token-authenticated or private-CA one the dial failed outright and `/ready`
  returned 503 for ever, while the agent's actual connection was fine.

  Serving `/ready` everywhere was the right intent and still happens — the
  agent owns the endpoint now, with checks it can answer for itself, and the
  edge contributes its three leaf checks only when there is a leaf.
  `edgeEnabled()` is now `nats.server_config != "" || sync.Any()`.

- **Sync wiring is retried instead of attempted once.** A bucket that could not
  be brought up at startup stayed down until someone restarted the agent, which
  made the ordinary deployment order — install the gateway, then create the
  bucket at the hub — a two-step dance with a restart in the middle. It is now
  re-attempted every 60 seconds until it comes up, and the repeated failure is
  logged only when the reason changes.

  Entries that are already up are skipped, which is the correctness
  requirement: a second `startRelay` on a live bucket would open a second
  watcher and double the hub's write rate for ever, silently.

### Added

- **`cmd.health` carries `checks`: the readiness report, the same one `/ready`
  serves**, from the same registry on the same probe schedule. The status word
  is now a pure function of it — `fail` → `unhealthy`, `warn` → `degraded`,
  `ok` → `healthy`.

  This is why there is no `edge` block in the response. Everything a gateway
  knows first-hand — local leaf up, hub uplink attached, each declared bucket
  syncing and why not — is a registered check, so it arrives without a second
  structure to define, fill and keep in step with the edge's own state. Before
  this, a gateway with every synced bucket down answered `healthy` over NATS
  while its own `/ready` endpoint disagreed, because the command's status
  ladder and the readiness registry were two separate opinions about what
  "wrong" means.

  New checks: `nats`, `jetstream`, `task_metrics`, `nebula` and
  `platform_sync` on every agent; `nats_local`, `hub_uplink` and `sync` where
  there is a leaf. They reach Prometheus too, as `agent_check_state`.

- **`platform_sync` reports when the platform conversation last succeeded.** A
  credential sync that quietly stops working is fatal on a delay: the `.creds`
  on disk keeps working until the platform re-mints or revokes it, and only
  then does the agent discover it has been unable to reach the platform for
  weeks. It warns after two missed intervals.

### Changed

- `observe.Start` runs the first readiness probe synchronously rather than in a
  goroutine, which is what `health.Prober.Start` already documented and what
  guarantees `cmd.health` always has a report to answer with. Every check reads
  state the process already holds, so the first round costs microseconds.


- **System metrics now carry `memory_total_gb` and `memory_used_percent`.**
  Free alone could not be alerted on: "`memory_free_gb` below 2" means
  something different on a 4 GB gateway and a 64 GB server, so every consumer
  had to know each box's size out of band to write a rule. `disks` has carried
  total and percent since the beginning; this is memory catching up.

  Both are **omitted rather than zeroed** when the source does not report a
  total, which in practice means an exporter without `MemTotal`. Publishing
  0 GB installed and 0% used would read as an idle machine rather than as an
  unanswered question — the same rule the edge collector applies to its
  server-derived series. The agent logs a warning in that case, because an
  alert written against `memory_used_percent` would otherwise silently never
  fire.

- **`cmd.health` reports the three allowlists** — `allowed_commands`,
  `allowed_services` and `allowed_log_paths` — in its `config` block. They are
  the three gates, and a rejected command was otherwise undiagnosable without
  shell access to the box: "not allowed" is the same answer whether an entry is
  missing or merely spelled differently. They are configuration rather than
  secrets, being the list of things an authenticated caller was already
  permitted to do.

  This is deliberately not a general config-dump command. That would mean
  owning a redaction policy for every field added afterwards, where the cost of
  forgetting once is a leaked credential.

- **`enabled_tasks` includes `nebula_sync`**, which has been a real scheduled
  job since 0.2.0 and was missing from the list. Both internal jobs are now
  reported on the same condition the scheduler uses to schedule them — the
  presence of the interface, not a config key — so the list cannot claim a job
  that is not running.

### Changed

- **The ">10 reconnects means degraded" rule is gone.** It counted reconnects
  for the lifetime of the process, so an agent up for a year through eleven
  server restarts reported `degraded` for ever after, and the only fix that
  kept the rule would have been to add state for a sliding window. `connected`
  already answers the question the rule was reaching for, and `reconnects` is
  still in the response for anyone who wants to graph it. `degraded` now means
  JetStream unusable, a majority of metrics scrapes failing, or the overlay
  enabled and not carrying traffic.

### Removed

- `edge.Config.SyncInterval`, which was declared and documented but never
  populated by `edgeConfig()` and never read. The sync retry added above is the
  job it was written for, and it did not need it: that interval is a package
  constant (`syncRetryInterval`), for the same reason `monitorURL` is one —
  there is no deployment where a different number is right, so a field here
  would be a control that can only disagree with itself.

- `edge.Config.ObserveAddr`, `MetricsToken` and `ReadinessInterval`. The agent
  serves those endpoints now and reads the config keys directly; leaving the
  fields on the edge's struct would have been three more values copied to a
  place that no longer uses them.

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

[Unreleased]: https://github.com/stone-age-io/agent/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/stone-age-io/agent/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/stone-age-io/agent/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/stone-age-io/agent/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/stone-age-io/agent/releases/tag/v0.1.0
