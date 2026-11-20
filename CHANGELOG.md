# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) — with the pre-1.0
caveat that a minor version may break something. Pin what you deploy.

History before `0.1.0` is not reconstructed here; `git log` is the record for
that period, and this file starts where the versioned releases do.

## [Unreleased]

## [0.11.0] - 2026-11-20

Device-held keys: a Thing whose NATS identity or Nebula host was made with
"Key held by device" on the platform keeps its own private keys. Needs
platform v0.21.0 (pb-nats v0.5.0, pb-nebula v0.8.0).
Nothing to change in the agent's config.

### Added

- **Device-held NATS keys.** When the platform has no key for this Thing the
  agent generates an nkey pair, writes the seed beside `nats.auth.creds_file`
  (same name, `.nk` extension, 0600), enrolls the public key, and builds the
  `.creds` from the JWT the platform signs. The seed never leaves the box.
  After an operator revokes the key the agent enrolls a new one on its next
  sync; it never re-sends a revoked key. A key on the platform that is not
  this device's stops the sync with an error naming the fix.
- **Device-held Nebula keys.** The same for the Nebula host: an X25519 pair
  as `nebula-cert keygen` makes it, kept as `nebula-host.key` beside
  `nebula.cache_file`, with `pki.key` in the fetched config pointed at it.
- **`agent -leaf-config` on a device-held key** builds the leaf's `.creds`
  from the served `jwt` and the local seed, enrolling the key first if the
  platform answers 409.

### Fixed

- **The credential sync no longer drops the rest of the session file.** It
  saved only the token and its own revision, so every credential sync erased
  the Nebula revision (the next Nebula sync then re-read its config, private
  key included) and the cached hub domain.

## [0.10.0] - 2026-11-20

MQTT at a site's leaf, off by default: set `platform.leaf_mqtt` and restart
the leaf. Also fixes a hang when a stop arrived as the agent started. Nothing
to change in the agent's own config unless you want MQTT at a site.

### Added

- **MQTT at a site's leaf: `platform.leaf_mqtt`.** Set it to a `host:port`
  and the generated `nats-leaf.conf` gets an `mqtt` block, so a site's MQTT
  3.1.1 devices connect to their own leaf and keep working while the WAN is
  down. It works in-process and separately supervised. Empty, the default,
  leaves the file byte-for-byte as before, so an upgrade rewrites and reloads
  nothing. `nats-server` cannot reload an `mqtt` block: **restart the leaf
  after setting or changing it.** Sessions and retained messages stay in the
  leaf's JetStream domain, so a client at the hub does not see a value
  retained at a site. See [Leaf Nodes](docs/leaf-node.md#mqtt-for-site-devices).

### Fixed

- **A stop that arrived as the agent started could hang it.** The scheduler's
  start and shutdown could overlap, and gocron's `Start` then waited for ever
  for a confirmation its own shutdown had cancelled. The service stopped
  anyway, but the agent's run loop never returned. They are now serialised.
  `TestRunReturnsCleanlyWhenStopped` failed about one run in twenty under
  `-race` because of it.

## [0.9.0] - 2026-11-18

Nebula 1.12, which is why this is a minor release: it refuses some configs
1.11 accepted. On an agent with `nebula.enabled`, read the Nebula entry under
Changed before upgrading. Builds now use Go 1.27.4. nats.go, `golang.org/x/net`
and `golang.org/x/crypto` carry security fixes. CI runs golangci-lint. Nothing
to change in the agent's own config.

### Security

- **nats.go 1.56.0.** A security release for how the client picks servers
  and decides to use TLS. Credentials embedded in a server's `connect_urls`
  entry are no longer used for that server, and over TLS the client no longer
  adds servers it learned before the handshake. The agent turns TLS on only
  through `tls.enabled`, which its breaking changes do not affect.
- **`golang.org/x/net` 0.63.0 and `golang.org/x/crypto` 0.59.0.** govulncheck
  found three known vulnerabilities in the old versions, none in code the
  agent calls. Scanners that check the binary report them anyway.
- **Nebula 1.12.0, built with Go 1.27.4.** It includes Nebula 1.11.1's fix for
  an IPv6 firewall bypass: an IPv6 packet carrying a protocol Nebula does not
  parse (SCTP, GRE, IP-in-IP) could be read as TCP or UDP and match a TCP/UDP
  rule. It also enforces the per-tunnel message counter limit, so a tunnel
  rehandshakes before its nonce could wrap. Go 1.27.4 is the latest patch;
  Nebula 1.12 needs at least 1.27.3.

### Changed

- **Telemetry uses the same JetStream API as KV sync.** A publish whose ack
  does not arrive within 10 seconds is logged as failed. It used to wait
  forever, one goroutine per publish. Failed publishes are now logged from
  one place, as `Failed to publish telemetry`.
- **Dependency updates:** gocron 2.23.2, zap 1.28.0, `prometheus/client_golang`
  1.25.0 and `prometheus/common` 0.74.0. gocron's releases since 2.18 fix
  several bugs in singleton rescheduling, the mode every scheduled task here
  runs in. `/metrics` output is unchanged. The binary now includes
  `pandatix/go-abnf`, which `prometheus/common` uses for its OpenMetrics
  parser.
- **Nebula 1.12.0** (from 1.11.0), the same version pb-nebula now uses. On an
  agent with `nebula.enabled`, check these before upgrading:
  - **A firewall rule's `group` must be a string.** A number like `1.10`, or a
    `null` group with no `host`, other group or `cidr` beside it, is now
    refused. A host given such a rule keeps running its last good config.
    1.11 accepted both, and quietly turned `1.10` into the group `1.1`.
  - **IPv6 traffic in protocols Nebula does not parse** (SCTP, GRE, IP-in-IP)
    now needs a `proto: any` rule. It may have been passing only through the
    bypass fixed above.
  - **On Linux, Nebula refuses a `tun.dev` that already exists**, such as one
    created ahead of time with `ip tuntap add`. Remove it and let Nebula create
    it.
  - **On Windows, an IPv6 overlay with `tun.mtu` below 1280 refuses to start**,
    as it already did on the other platforms.

  The agent now restarts Nebula straight after a reload that Nebula reports
  as needing it: a config that did not parse, a part of the new config that
  could not be applied, or a key that applies only on a restart. Before, only
  a parse failure was reported, and anything else waited on the verify step.
  The embedded agent opens none of Nebula 1.12's new local control sockets;
  only the `nebula` command does.
- **CI runs golangci-lint for Linux, Windows and FreeBSD**, pinned to v2.15.0.
  `make install-tools` installs the same version. It used to install the v1
  linter, which cannot read this repo's v2 config.

## [0.8.0] - 2026-11-16

Two defaults changed, which is why this is a minor release. Check both before
upgrading: the `/ready` + `/metrics` port, and `commands.scripts_directory`.

### Changed

- **`/ready` and `/metrics` default to `127.0.0.1:10063`, not `:9100`.** 9100
  is node_exporter's port, so on a box running both, one of them failed to
  bind. **Anything that scrapes or probes the agent on 9100 must move**, or pin
  the old port with `observability.addr: "127.0.0.1:9100"`. 10063 was the next
  free entry in Prometheus's default port allocation list; 9100-9999 is full.
- **A minimal config starts.** `code`, `nats.urls` and `nats.auth` are now
  enough. Two defaults used to stop it:
  - `tasks.service_check` with no `services` listed is off, where it used to
    refuse to start.
  - `commands.scripts_directory` defaults to empty (no scripts). It used to
    default to a per-OS path that had to exist. **If you run scripts and never
    set this key, set it now**, or `cmd.exec` will refuse them.
- **The example configs are short and ship with nothing allowed.** Only the
  identity and the NATS connection are live; everything else is commented out
  and shows its default. The old examples had placeholder services, commands
  and log paths enabled, so a copied file allowed things nobody chose. The
  three per-OS files now differ only inside quoted strings, and a test keeps
  them that way. The Linux, FreeBSD and Windows install guides copy the
  example instead of writing out a full config of their own.
- **CI runs the tests under the race detector and validates
  `.goreleaser.yaml` on every push**, and releases pin goreleaser to v2. The
  workflow actions moved to v7 (Node 24).

### Fixed

- **nats-server 2.15.3** for an embedded leaf. Its leaf node fixes include
  account JWT updates no longer losing leaf interest or resetting permissions
  (what the leaf preload refresh does), reloads waiting for startup to finish,
  and stale leaf connections and inflated leaf counts. Brings `nats-io/jwt`
  2.8.3 and `nkeys` 0.4.17.
- **`agent -leaf-config` writes the creds atomically**, through the same temp
  file and rename as every other secret. It wrote them in place, so a crash
  mid-write could leave a truncated credential.

## [0.7.0] - 2026-11-14

### Security

- **Built with Go 1.26.11.** Releases through 0.6.2 were built with Go 1.26.0,
  because `go.mod` said `go 1.26.0` and the release build installs exactly
  that. Its standard library has 30 known vulnerabilities the agent's code can
  reach, in `net/http`, `crypto/tls`, `crypto/x509`, `net/url` and others, all
  fixed by 1.26.11. Upgrade every agent. CI now runs `govulncheck`, which is
  what found it.

### Changed

- **The agent's NATS connections are named after its code.** The main
  connection is `agent-<code>`, the `<app>-<code>` shape atv-bridge and the
  kiosk already use, and a gateway's edge connection is `agent-edge-<code>`.
  They were the constants `win-agent` and `agent-edge` on every platform, so in
  `connz` and the console every agent in an account looked like the same
  Windows box. Anything that matches connections by the old names needs
  updating.
- **A scheduled task no longer overlaps itself.** One still running when its
  next tick comes skips that tick instead of starting a second copy. A Nebula
  sync can wait up to `verify_timeout` (5 minutes at most) on a 1-minute
  interval; the extra runs used to queue behind it and then run back to back.

### Fixed

- **A missing `nats.server_config` file names the agent's command.** The error
  told the operator to run the Control Plane's `stone-age nats export` or pass
  `--nats-config`, neither of which an agent has. It now says to run
  `agent -leaf-config`, or to point `nats.server_config` at an existing config.

## [0.6.2] - 2026-11-14

### Fixed

- **Stopping the agent no longer waits 12 seconds and logs a scheduler
  error.** Every stop called the agent's shutdown twice at once: under systemd,
  SIGTERM reaches both the agent's signal handler and the service library's,
  and on Windows the service stop wakes the agent's run loop, which shut down
  again. The scheduler hands its stop result to only one caller, so the other
  waited out its 12-second timeout and logged `gocron: timed out waiting for
  scheduler to stop` about a scheduler that had stopped cleanly; the NATS
  drain and the Nebula stop also ran twice, concurrently. Shutdown now runs
  once and every other caller waits for it (`internal/agent/agent.go`,
  `TestConcurrentShutdownIsQuick`).

## [0.6.1] - 2026-11-14

### Fixed

- **The edge waits for a leaf that is not up yet.** Its connection to the
  local leaf did not retry a failed first connect. A separately supervised leaf
  reads the conf the agent writes, so it is normally not listening when the
  agent starts: `nats.Connect` failed, the edge stopped, and the agent logged it
  once and never ran it again. KV sync stayed down and `nats_local` failed until
  the agent was restarted. The edge connection now retries until the leaf is
  up, like the agent's own NATS client (`internal/edge/run.go`,
  `TestLocalConnectionWaitsForALeafThatIsNotUpYet`).

## [0.6.0] - 2026-11-05

### Changed

- **The edge → hub relay no longer waits on the hub for every change.** Each
  live change used to cost a read from the hub and then a wait for the hub's
  acknowledgement, which held a relay to about 5 changes a second over a 100 ms
  link. Live changes are now published without waiting: about 1,300 a second
  over the same link. The startup replay still compares each key with the hub
  before writing, so a restart does not rewrite the hub's bucket, and failed
  writes are still retried with the edge's current value. Each write waits at
  most 10 seconds for the hub's answer before it counts as failed.
- `agent_edge_relay_pending{bucket}` now counts every key the hub has not
  confirmed: writes in flight as well as failed writes waiting to be retried. It
  is small and moving on a healthy site, and grows during an outage as before.
- nats.go v1.54.0 → v1.55.0, a release made almost entirely of reconnect, drain
  and consumer fixes. Three reach this agent directly:
  - A KV watcher (the relay's, and every other `Watch`) runs on an ordered
    consumer, which used the 15s pull heartbeat instead of the 5s ordered one,
    so a stalled watcher was reset after about 30s rather than 10s.
  - `ForceReconnect()` called while the connection was already reconnecting
    disabled `ReconnectWait` and dialed in a tight loop. The scheduler and the
    reconnect command both call it.
  - A partial write that left a truncated message on the wire closed the
    connection for good instead of reconnecting, which this agent turns into
    an exit and restart through `Lost()`.
- **The embedded nats-server is v2.15.2** (was v2.15.0), matching the
  platform's hub. This is the server a gateway hosts with
  `nats.server_config`. What reaches a site's leaf:
  - A mirror or source whose flow control has stalled is now reported in
    stream info and the server log. A missing `$JS.FC.>` permission across the
    leaf used to look like a mirror that simply never caught up.
  - A leaf connection checks publish permissions against the subject it
    actually sends, after any transform, so a role no longer has to allow the
    original import subject as well.
  - A partial compressed write could leave the leaf connection's pending bytes
    miscounted, stalling it and stopping it from reconnecting. Fixed.
  - Leaf ACLs no longer implicitly allow `$MQTT.>`.
- The upgrade also moves `golang.org/x/sys`, the other `golang.org/x/*`
  modules and `klauspost/compress` to the versions nats-server v2.15.2 requires.

### Removed

- **`twin.enabled` is no longer refused by name.** The key never worked, and
  since 0.2.0 the agent refused to start with it so that a config file still
  carrying it could not silently sync nothing. That check is gone: a config with
  `twin.enabled` now starts and ignores the key. Write `sync: { twin: true }`.

## [0.5.0] - 2026-10-15

> **Upgrading a gateway: restart the leaf once.** The generated
> `nats-leaf.conf` now names a `pid_file` (`nats-leaf.pid`, beside the conf) and
> writes every path -- `credentials`, `store_dir`, `pid_file` -- as an absolute
> path into the conf's directory. Re-run `agent -leaf-config` (or let the first
> refresh rewrite the conf), then restart the leaf: the separately supervised
> `nats-server`, or the agent when it hosts the leaf. A running leaf cannot
> adopt the new file by reload: it has no pid file to be signalled through, and
> nats-server refuses to reload a changed `store_dir` at all ("config reload not
> supported for jetstream storage directory"), applying nothing from the file.
> The refresh reports the failed reload on the run that rewrote the file, and `leaf_preload`
> warns from the next account JWT change until the restart. The leaf needs write
> access to its config directory -- the same access it already needs to create
> its JetStream store there. **If the leaf did not run from its conf's
> directory, its JetStream data is not in `<confdir>/jetstream`:** move the old
> `jetstream` directory there before the restart, or accept a resync (hub → edge
> mirrors refill from the hub; any relay backlog not yet sent to the hub is
> lost). On Windows a separately supervised leaf is never signalled; restart it
> after each change, or host it in-process with `nats.server_config`.

### Added

- **A gateway keeps its leaf's account JWT current**, so a device revoked at
  the hub stops connecting at its site. NATS revocations live in the account
  JWT, and the leaf's `resolver: MEMORY` only ever knew the one `agent
  -leaf-config` preloaded -- a revoked device kept authenticating at its site's
  leaf indefinitely. Every `platform.leaf_refresh_interval` (default `5m`; `0`
  off, otherwise 30s–1h) the agent probes `/api/me/account-jwt`, and only when
  the account JWT changed (or on the first run after start) reads the full
  `/api/me/leaf-config`, rewrites `nats-leaf.conf` if it changed, and reloads
  the leaf: in-process when the agent hosts it, by `SIGHUP` via `pid_file` on
  Linux and FreeBSD. On reload the leaf closes a revoked user's live
  connections and refuses its reconnects. It runs only where a generated
  `nats-leaf.conf` sits beside `nats.auth.creds_file`, and never writes the
  creds. A changed operator is not reloadable and is reported instead. See
  `docs/leaf-node.md`.
- **The probe keeps the credential off the wire.** The full leaf config carries
  this thing's `.creds`; the probe carries only the account JWT and its public
  key, so a refresh that finds nothing changed transfers no key material. It
  needs a platform that serves `/api/me/account-jwt`. An older platform answers
  404, and the agent falls back to reading the full leaf config on every run --
  credential included -- and warns once.
- **`cmd.leaf_refresh`** runs one refresh now and answers
  `{"changed": ..., "reloaded": ...}`.
- **A `leaf_preload` readiness check** warns when the leaf is enforcing a
  different account JWT from the one the platform last served, read from the
  leaf's loopback `/accountz`.

### Fixed

- **`agent -leaf-config` writes `nats-leaf.conf` atomically** (temp file and
  rename). The creds write is unchanged.
- **Paths in the generated `nats-leaf.conf` are absolute.** It wrote
  `credentials` as the creds file's base name and `store_dir: "./jetstream"`,
  and nats-server resolves both against its working directory, not the conf's
  -- so a leaf run from anywhere else (under a service manager, typically)
  could not find its credential and kept its JetStream store in that other
  directory. See the upgrade note above.
- **Shutdown waits for an embedded leaf to stop** (up to 30s) instead of
  letting the process exit while it was still flushing JetStream.

- **`nebula.source: "file"` no longer re-applies an unchanged file.** Every
  `sync_interval` it reloaded the config, waited for a lighthouse handshake,
  rewrote `cache_file` and logged "Adopted new Nebula config", whether the file
  had changed or not. The file's SHA-256 is now its revision, so only an edit is
  applied -- within `sync_interval`, or at once with `cmd.nebula` `sync` -- and
  `config_revision` in `cmd.health` is that hash.

- **A file source with `nebula.sync_interval: "0"` is refused at config load.**
  The interval was only checked for `source: "platform"`, so a zero passed
  validation and the agent then failed to start when the scheduler refused the
  job. Any positive interval is accepted for a file source; the 1m–1h bounds
  still apply to the platform.

## [0.4.0] - 2026-10-08

> **Upgrading.** Add `$SRV.>` to the subscribe list of each agent's NATS role.
> Without it nothing breaks -- commands answer exactly as before -- but the agent
> is missing from service discovery, and the new `nats_permissions` check reports
> `degraded` in `cmd.health` (`/ready` stays 200) until the role is updated.
> The same check will also surface any subject the agent's role was already
> refusing, which until now left no trace beyond a crash (see Fixed).

### Fixed

- **A subject the server refused crashed the agent.** The NATS error callback
  read the subscription's subject without checking it, and nats.go calls it with
  no subscription for a permissions violation and for an authorization error on
  reconnect. The panic happened on a goroutine nothing recovers, so the process
  died. Present since the first release. An agent whose role did not allow one of
  its own subjects -- its command subtree, say -- could not stay up.

### Added

- **Service discovery.** The commands are now the endpoints of a NATS micro
  service named `stone-agent`, so `nats micro ls stone-agent` lists every agent
  in the account, and `nats micro info` / `nats micro stats` give each one's
  code, location, OS and version, and request, error and timing counts per
  command. The console can ask the same questions with the logged-in user's own
  credential, where it cannot reach `/metrics` on a box's loopback. The command
  subjects and reply bodies are unchanged. See `docs/architecture.md`.
- **Error replies carry `Nats-Service-Error` and `Nats-Service-Error-Code`
  headers** beside the unchanged JSON body: `400` when the agent refused the
  request itself, `500` when the work failed.
- **A `nats_permissions` readiness check.** It warns with the server's own
  message when the server has refused a subject since the agent last connected.

### Changed

- nats.go v1.51.0 → v1.54.0. Among other fixes, a micro endpoint no longer
  over-matches a longer subject.
- **The embedded nats-server is v2.15.0** (was v2.14.7). This is the server a
  gateway hosts with `nats.server_config`. Two changes reach a site: a mirror
  now recovers by itself when its source stream is recreated, and a stream
  now allows at most 1000 consumers unless `max_consumers` or the server's
  `jetstream.limits.default_max_consumers` says otherwise. A site's watchers
  and relays use a small fraction of that. See the
  [2.15 upgrade guide](https://docs.nats.io/release-notes/upgrade-to-2.15).

## [0.3.2] - 2026-09-24

### Added

- **`cmd.service` takes a `status` action.** It reads one service's state
  without changing it, where before the only way to see a service was to wait
  for the next `service_check` telemetry. The reply carries `service_status`
  in the telemetry's words (`Running`, `Stopped`, `NotInstalled`, ...), and
  the lookup is the same one the telemetry makes. It uses the same
  `allowed_services` gate as start, stop and restart.

## [0.3.1] - 2026-09-24

> **Security fix. Upgrade any agent with a `commands.scripts_directory`.**
> Anyone able to publish to `cmd.exec` could run arbitrary commands on the
> device, whatever the allowlist said, as long as one script existed in the
> scripts directory.
>
> Script requests must now be a bare filename (`deploy.sh`, not
> `/opt/agent/scripts/deploy.sh`). A request that used the full path is now
> refused, where it used to work.
>
> Other changes a caller or operator can see:
>
> - `cmd.exec` replies carry `exit_code` whenever the command ran, and failures
>   carry their `output`.
> - Windows scripts report their own exit code.
> - FreeBSD runs allowlisted commands through `/bin/sh`.
> - `tasks.system_metrics.source` and `exporter_url` are ignored.

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

- **A timeout kills everything the command started.** It used to kill only
  the shell or `powershell.exe`, so a child it had started (a `sleep`, a
  long-running check) kept running after the reply had gone out. The command
  now runs in its own process group on Linux and FreeBSD, and the timeout
  kills the group. On Windows it runs `taskkill /T`. Only a timeout or agent
  shutdown does this: a command that exits normally and leaves a daemon
  running (a script that starts a service, say) is left alone.

- **Allowlisted commands work on a stock FreeBSD.** They ran through
  `/bin/bash`, which FreeBSD's base system doesn't have, so every entry in
  `allowed_commands` failed with "no such file" unless someone had installed
  bash and linked it there by hand. FreeBSD now uses its own `/bin/sh`. Linux
  still uses `/bin/bash`. If you linked bash into `/bin` to make this work and
  your FreeBSD allowlist uses bash-only syntax, rewrite those entries for `sh`.

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

[Unreleased]: https://github.com/stone-age-io/agent/compare/v0.11.0...HEAD
[0.11.0]: https://github.com/stone-age-io/agent/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/stone-age-io/agent/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/stone-age-io/agent/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/stone-age-io/agent/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/stone-age-io/agent/compare/v0.6.2...v0.7.0
[0.6.2]: https://github.com/stone-age-io/agent/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/stone-age-io/agent/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/stone-age-io/agent/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/stone-age-io/agent/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/stone-age-io/agent/compare/v0.3.2...v0.4.0
[0.3.2]: https://github.com/stone-age-io/agent/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/stone-age-io/agent/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/stone-age-io/agent/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/stone-age-io/agent/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/stone-age-io/agent/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/stone-age-io/agent/releases/tag/v0.1.0
