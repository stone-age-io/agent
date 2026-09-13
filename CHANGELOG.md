# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) — with the pre-1.0
caveat that a minor version may break something. Pin what you deploy.

History before `0.1.0` is not reconstructed here; `git log` is the record for
that period, and this file starts where the versioned releases do.

## [Unreleased]

### Changed

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

[Unreleased]: https://github.com/stone-age-io/agent/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/stone-age-io/agent/releases/tag/v0.1.0
