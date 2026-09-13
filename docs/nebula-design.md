# Nebula Support — Design

**Status: in progress.** Steps 1 to 3 of [Staging](#staging) are implemented — the
startup change, `internal/nebula`, and the surfaces. Step 4 (`leaf-sync`) is not.

This is not an installation guide, and it is deliberately not linked from the README's documentation index,
which lists shipped behaviour only. Read it as the record of a decision and of the
alternatives that were rejected on the way to it.

## Why

[Nebula](https://github.com/slackhq/nebula) is an overlay mesh, and the
stone-age.io platform already manages one: `pb-nebula` runs the CA, signs host
certificates, and generates each host's complete `config.yaml`. What the platform
cannot do is make a device *adopt* the config it just generated. Today that is a
person with `curl` and a text editor.

That gap is load-bearing, not cosmetic. Three platform features are already built
on the assumption that hosts re-read their config on their own:

- **Revocation.** Nebula has no CRL and no OCSP. The only way to refuse a
  certificate the CA already signed is `pki.blocklist`, which every *other* host
  carries in its own config. Deactivating a host rewrites every peer config in
  the network — and none of those peers find out until something re-reads them.
- **CA rotation.** `POST /api/org/nebula-ca/rotate` is a three-step interlock
  (`prepare`, `commit`, `finish`), and `finish` refuses while any active host
  still holds a certificate from the outgoing CA. The dangerous part is the wait
  in the middle, and the wait only ends if hosts pull the new trust bundle.
- **Renewal.** Host certificates default to one year. `pb-nebula` re-signs on the
  `renew` field and on its own sweeps; the new certificate then has to reach the
  host.

The agent is already the thing that authenticates to the platform as itself,
polls for changes, and writes secrets to disk safely. Teaching it to do that for
a Nebula config closes all three loops at once. **This feature is convergence,
not connectivity.** Connectivity is a side effect.

## What already exists

Nothing needs to be built on the platform side. Specifically:

- `things.nebula_host` is a relation, sitting directly alongside
  `things.nats_user`.
- The `nebula_hosts` list and view rules already carry an explicit Thing branch:
  *"a Thing sees ONLY the Nebula host assigned to its `nebula_host` field."* An
  authenticated agent can read its own `config_yaml`, `certificate`,
  `private_key` and `ca_certificate` with no new route and no rule change.
- `pb-nebula` already generates `unsafe_networks` (cert-bound, the gateway half),
  `unsafe_routes` (config-only, the consumer half), relays, `preferred_ranges`,
  the blocklist, and signs host certificates at the network mask rather than /32 —
  which is what makes a host usable as an `unsafe_routes` gateway at all.
- `leaf_nodes.nebula_host` exists too. The platform already models both identities
  as independent mesh members. See [Rejected: one edge binary](#rejected-one-edge-binary).

## Scope: one mode

The agent embeds Nebula as a library and runs it with a system TUN device.

- **Opt-in.** `nebula.enabled` defaults to `false`. An agent with the feature off
  behaves exactly as it does today.
- **Embedded, not supervised.** `nebula.Main()` (v1.11) returns a `*Control` with
  `Start`, `Stop`, `ListHostmapHosts`, `GetCertByVpnIp`, `CloseTunnel`. Nebula
  v1.11 logs through `log/slog`, so it folds into the existing zap setup rather
  than dragging in a second logging stack. `config.C.ReloadConfigString` means a
  config fetched from the platform can reach Nebula without the private key
  touching disk on the hot path.
- **One code path.** No modes, no build tags, no second binary flavour.

`pb-nebula` issues v2 certificates, so every host on the mesh needs Nebula 1.10
or newer. That is a property of the mesh, not of this agent, but a host running
an older build fails silently — it simply never completes a handshake — so it is
worth restating wherever this feature is documented for users.

## Rejected alternatives

This section is the point of the document. Each of these was proposed seriously
and dropped for a specific reason.

### Rejected: an `observe` mode that manages an external Nebula service

The idea: Nebula runs as its own system service; the agent syncs `config_yaml` to
disk, signals a reload, and reports status. Attractive because it decouples mesh
uptime from agent restarts, and because the existing Prometheus scraper in
`internal/tasks/collector_exporter.go` could read Nebula's own stats listener
almost for free.

It dies on a fact: `config.C.CatchHUP` registers `syscall.SIGHUP` and Nebula's
config package has no Windows variant. Reload-by-signal works on Linux and
FreeBSD and silently does nothing on Windows, where the fallback is restarting a
service — which is the very coupling the mode existed to avoid. A feature that
behaves differently on a third of the supported platforms is a permanent
maintenance tax, paid to avoid a risk that is addressed more cheaply below.

### Rejected: a userspace mode with no TUN

Nebula's `service` package wraps `Control` in a gvisor netstack with `Dial` and
`Listen`, which would give the agent mesh reachability with no kernel privileges
at all. Genuinely elegant, and the answer to a question nobody has asked: the
agent already runs as root/SYSTEM because it controls services. Pulling gvisor in
for a hypothetical is the wrong trade. Revisit only if a concrete deployment
needs it.

### Rejected: one edge binary

The proposal was to collapse the agent, `leaf-sync`, and a Nebula host into a
single process. It is the wrong shape:

- They are different identities in the platform. `things` and `leaf_nodes` are
  separate auth collections with separate rules.
- They have different cardinality. One leaf node per site; N agents per site.
- `leaf-sync` is several thousand lines of embedded `nats-server`, KV
  reconciliation, twin relay and readiness endpoints. Merging means every device
  carries a site controller it will never run.

The platform has already answered this: `leaf_nodes` has its own `nebula_host`
relation. The intended shape is the same small feature built twice — a couple of
hundred lines in each binary — not one binary doing three jobs.

| binary | identity | cardinality | Nebula role |
|---|---|---|---|
| `agent` | `things` | N per site | device on the mesh, optionally a gateway |
| `leaf-sync` | `leaf_nodes` | 1 per site | site node on the mesh |

### Rejected: extracting the platform-client shape into a shared library

After this lands there will be three near-copies of "authenticate to PocketBase
as myself, pull my own config, write it 0600, retry": `leaf-sync`'s bootstrap,
`internal/platform`, and the Nebula fetcher. The rule of three says extract.

Don't. A shared module across two repositories means a third repository, a third
release cadence, and version skew between the agent and the platform for roughly
two hundred lines of HTTP. The divergences are real, too: `things` versus
`leaf_nodes`, `expand=nats_user` versus `nebula_host`, `creds_file` versus
`config_yaml`, one revision probe versus another. Copy it. If it still hurts at
the fifth copy, extract then — with five real examples instead of three guesses.

### Rejected: merging a local config fragment into the generated config

It is tempting to let the agent overlay local settings (a `stats:` block, a
custom `tun.dev`) onto the `config_yaml` the platform generates. That creates two
sources of truth for one file, and every future debugging session starts with
"which half of this came from where."

**One generator, no local merging.** If a setting needs to be configurable, it
becomes a field in `pb-nebula` and the generator emits it — the same discipline
`pb-nebula` already applies to the blocklist and the relay section.

## Design

### Prerequisite: don't die in the constructor

`agent.New` currently treats NATS as a construction-time dependency: `nats.Connect`
is called without `RetryOnFailedConnect`, the JetStream `AccountInfo()` probe is
fatal, and a failure at either point returns an error that ends the process. The
service manager restarts it. That is a reasonable design *while the agent owns
only itself*.

It stops being reasonable the moment the agent owns a TUN:

- With NATS reachable only over the overlay, first boot is a guaranteed crash
  loop. Construction fails before Nebula is ever started, so the overlay never
  comes up, so NATS never becomes reachable. A deadlock by structure, not by
  timing.
- Even with NATS on the underlay, any NATS outage now bounces the mesh on a
  restart timer.

The fix is small and useful on its own: **wire in the constructor, connect in the
supervisor.** Each subsystem gets `Start`/`Stop` and its own backoff; `agent.New`
touches no network; `agent.Run` supervises. NATS gains
`nats.RetryOnFailedConnect(true)`, and the JetStream validation is demoted from
fatal to a reason to retry — it is still worth keeping, since it turns a silent
telemetry failure into a loud one.

This is not a new pattern for the codebase. `leaf-sync` reached the same
conclusion and wrote it down:

> With `--nats` it retries instead, because exiting would stop the leaf server
> too. A supervisor restarting the pair every few seconds through a WAN outage
> means devices reconnecting and JetStream recovering its store on a loop.

That is the Nebula problem exactly. Copy the answer.

### NATS on or off the overlay

Both work, and neither needs ordering logic. Nebula and NATS are independent
supervised loops that do not know about each other: Nebula comes up when it comes
up, NATS is already retrying, the reconnect handler fires. Where `nats.urls`
points becomes a line in a config file rather than an architectural fork.

One wrinkle worth documenting for users: if `nats.urls` holds a hostname that
only resolves over the overlay, the ordering problem has moved into the resolver.
**Use an overlay IP literal.**

### Fetch and sync

The same shape as `internal/platform`, for the same reasons:

1. Authenticate as the Thing (reusing the existing session file and token).
2. Probe the `nebula_hosts` record with `?fields=updated`.
3. Fetch the body only when the revision moved.

The probe matters more here than it does for NATS credentials. `config_yaml` is
up to 50 KB and contains `pki.key` — the host's private key, inline, because
Nebula's PKI requires it there.

**The sync interval is a security number, not a tuning knob.** Nebula's only
revocation mechanism is the blocklist carried by every peer, so *the poll
interval is the revocation latency*. The 24h used for `creds_sync` is wrong here;
default to 10m, allow 1m–1h. A NATS command (`cmd.nebula_sync`) lets the platform
collapse it to seconds when it matters, but polling remains the mechanism of
record — the mesh must converge without the command channel, because the command
channel may be riding on the mesh.

### Apply, verify, roll back

A bad config can take a device off the network permanently, and if NATS rides the
overlay it takes the recovery channel with it. So applying a config is a
transaction:

1. Cache the running config as last-known-good (0600, temp file + rename).
2. Apply the new one via `ReloadConfigString`.
3. Watch for a completed handshake with a lighthouse within a bounded window.
4. If that fails, restart `Control` on the new config and watch again.
5. If that fails too, restore the cached config and report the rollback in health.

**The agent does not classify config changes, and must not.** An earlier draft
proposed a table of which keys hot-reload and which need a restart. Nebula
already owns that classification internally — `interface.go` registers reload
callbacks for the firewall, `disconnect_invalid` and the rest; `lighthouse.go`,
`hostmap.go`, `connection_manager.go`, `dns_server.go` and every
`overlay/tun_*.go` register their own, each deciding for itself what changed. A
second table in the agent would duplicate logic one layer down and drift from it
on every Nebula upgrade.

The one field that genuinely cannot hot-reload is `listen.port` — `udp_linux.go`
reloads `listen.read_buffer` and nothing else — and a device agent can never hit
it: `pb-nebula`'s `extractPort` returns `0` for any host that is neither a
lighthouse nor a relay, so the port is always `0` and never changes.

That is why the ladder above is stated in terms of *outcomes* rather than fields.
Reload, and if the mesh does not come back, restart; if it still does not, roll
back. No knowledge of Nebula's internals, nothing to keep in sync, and the same
verify step drives both decisions.

### Secrets

Identical discipline to `.creds`: 0600, temp file plus rename, and **never log a
response body** — one of them is a private key. Non-2xx bodies are error
documents and are safe to fold into errors, exactly as `internal/platform` does.

### Surface 1: mesh state in `cmd.health`

Mesh state folds into the existing health response rather than getting a
telemetry subject of its own. It mirrors `NATSHealth` and is `omitempty`
throughout, so an agent with the feature off emits a byte-identical response to
the one it emits today.

```go
type NebulaHealth struct {
	Enabled        bool     `json:"enabled"`
	Running        bool     `json:"running"`
	OverlayIP      string   `json:"overlay_ip,omitempty"`
	Tunnels        int      `json:"tunnels"`
	LighthouseUp   bool     `json:"lighthouse_up"`
	CertExpiresAt  string   `json:"cert_expires_at,omitempty"`
	ConfigRevision string   `json:"config_revision,omitempty"`
	LastSync       string   `json:"last_sync,omitempty"`
	RolledBack     bool     `json:"rolled_back,omitempty"`
	UnsafeNetworks []string `json:"unsafe_networks,omitempty"`
	Error          string   `json:"error,omitempty"`
}
```

**`ConfigRevision` is the field that justifies the feature.** It is the `updated`
value the agent last adopted. A console can compare it against what the platform
currently holds and answer *"has this revocation actually landed on this
device?"* across a fleet. Convergence, made queryable, for the cost of one
string.

`UnsafeNetworks` is parsed out of the live certificate, not read from config —
the certificate is what Nebula actually enforces.

**Status contribution: `degraded` only, never `unhealthy`.** Nebula being down
does not stop telemetry or commands, and turning a fleet dashboard red for
someone else's network problem is noise. `determineHealthStatus` should return
`degraded` when Nebula is enabled but not running, running with zero tunnels, or
running on a rolled-back config.

This creates an asymmetry worth naming: on a device whose NATS rides the overlay,
a mesh failure means the health response never arrives at all, so the `degraded`
signal is unobservable in exactly the case where it matters most. That is
inherent to putting the control channel on the thing being monitored. It is one
more reason to prefer NATS on the underlay, and one more reason the heartbeat
stays minimal.

**The heartbeat is not touched.** Mesh state belongs in health for the same
reason the agent version does: the beacon stays a liveness signal and nothing
more.

There is no `telemetry.nebula` subject. Mesh state changes slowly, the platform
already knows the desired state, and health answers the question on demand. If
fleet-wide mesh dashboards later justify a stream, adding a subject is purely
additive and needs no redesign.

Keep all of this to a handful of numbers. It is not a replacement for Nebula's
own metrics, and see [the rejected config merge](#rejected-merging-a-local-config-fragment-into-the-generated-config)
for why the agent will not inject a `stats:` block to get them.

### Surface 2: `cmd.nebula`

An embedded Nebula is a supervised unit with start/stop/restart semantics, which
makes it look like a job for `cmd.service`. It is not, for two reasons.

**It is a subsystem the agent owns.** `ControlService` acts on units the agent
knows nothing about — it shells out to systemd or SCM and reports the result.
Nebula has agent-held state: the adopted config revision, the last-known-good
cache, an in-flight verify window. Any action has to interact with all of it.
There is also nothing to put in `allowed_services`, because the target is not
enumerable — it is "me."

**And a magic service name would collide with a real one.** This document
recommends running stock `nebula` as a system service on lighthouses. A
pseudo-service named `nebula`, intercepted before it reaches `ControlService`,
would shadow a service we tell people to run. Not a hypothetical.

So `cmd.nebula` is its own command, shaped like `cmd.rotate_creds`: self-scoped,
no allowlist, and answering with an error when the feature is off — exactly as
`rotate_creds` does when `auth.type` is not `stone-age`.

#### Verbs

| verb | meaning |
|---|---|
| `sync` | pull from the platform now and apply if the revision moved |
| `restart` | bounce `Control` on the *currently running* config |

`sync` is the convergence verb — it collapses revocation latency from the poll
interval to seconds when somebody is watching. `restart` is the debugging verb,
for a wedged tunnel. It deliberately does not re-fetch: keeping the two separate
keeps each one dumb.

**There is no `status` verb.** It would return the same `NebulaHealth` block
`cmd.health` already returns, and a second way to ask one question is a second
thing to keep in step with the first.

**There is no `stop` and no `start`:**

1. On a device whose NATS rides the overlay, `stop` severs the only channel that
   could deliver `start`. That is a remote-brick primitive, and it is precisely
   the failure the rollback machinery exists to prevent. Shipping both would be
   incoherent.
2. Even on the underlay, a stopped Nebula has no recovery path — nothing turns it
   back on. It is a *config state* wearing a command's clothes.
3. "Turn Nebula off" is `nebula.enabled: false`. That survives a restart and
   leaves a trace; a runtime command that evaporates on next boot does neither.

**A `rollback` verb was considered and dropped.** Rollback restores the cached
config, and the next sync pulls the same bad config back within the interval. It
buys one poll interval. A bad config is fixed on the platform, where it was
generated.

#### Respond before acting

Every handler in `internal/nats/handlers.go` acts and then responds.
`cmd.nebula` must invert that for `sync` and `restart`:

```
validate → respond {"status": "accepted"} → act asynchronously
```

On an overlay-NATS device the reply would otherwise ride the tunnel that was just
torn down, and the operator sees a timeout on an operation that in fact
succeeded. That is the worst available signal, because the obvious response to a
timeout is to retry.

Two consequences:

- **The outcome is reported through health, not through the reply.** This is the
  other half of why mesh state belongs in `cmd.health`: it is the completion
  channel, not merely a dashboard.
- **The detached goroutine needs its own `recover()`.** `handleWithRecovery`
  wraps the handler, not anything the handler spawns, and a panic in a detached
  goroutine takes the process down.

`restart` runs through the same verify-and-rollback path as an apply: re-establish
within the window, or fall back to last-known-good.

## Gateways and unsafe routes

A host serving `unsafe_routes` for its LAN needs nothing special *from the agent*.
Nebula tunnels the packet, Nebula's own host firewall authorises it, and the
kernel forwards it. `pb-nebula` already has both halves, and already signs the
certificate at the network mask so the gateway passes Nebula's own
`isGatewayInVpnNetworks` check. The agent's entire contribution is delivering the
config.

Three things are nevertheless required, none of which the agent should do:

- **IP forwarding.** Nebula does not set `net.ipv4.ip_forward`.
- **The return path.** A peer at `10.128.0.5` reaches `192.168.1.50`; the LAN host
  replies to `10.128.0.5` and has no route for it. Either the LAN gateway carries
  a route for the overlay CIDR, or the gateway host masquerades. This is the most
  common cause of "unsafe routes doesn't work."
- **Windows is a different animal.** Not a sysctl — `IPEnableRouter` in the
  registry plus the RemoteAccess service. Document Linux and FreeBSD as the
  supported gateway platforms and treat a Windows gateway as possible but
  unsupported.

**The agent detects and reports; it does not mutate.** Writing sysctls and
firewall rules is a different class of authority from running whitelisted
commands out of a config file, and it is a one-way door for this project's
security posture. Surfacing "gateway certified, forwarding disabled" in
`cmd.health` is most of the value at none of the cost.

## Lighthouses

An agent can run on a lighthouse — `is_lighthouse` is just a field in the
generated config and the agent does not care. There is a real case for it: the
lighthouse is the host whose failure is most consequential and is usually the
least monitored.

But a lighthouse is also the worst host to couple to agent restarts. Lighthouse
downtime is more benign than it sounds — established tunnels survive and punchy
keeps them alive; what breaks is new handshakes and roaming — so a two-second
restart is survivable and a crash loop is not. Since the crash loop is exactly
what the startup work above removes, this is a documentation matter rather than a
code one.

If a deployment genuinely needs the mesh to outlive agent restarts on a
lighthouse, the answer is a stock `nebula` service and no agent Nebula feature on
that host at all. That is simpler than any mode this agent could offer.

Lighthouse constraints are inherent to lighthouses, not to the agent: a static,
reachable public `IP:PORT` (`pb-nebula` requires `public_host_port`), no NAT, and
the knowledge that changing lighthouse fields regenerates every peer config in
the network.

## Costs, honestly

- **Binary size.** 19 MB before, **30.5 MB** after — better than the 35–40 MB this
  document first estimated. Nebula pulls in gvisor, `miekg/dns`, `gopacket`,
  `prometheus/client_golang` and `go-metrics`.
- **Go version.** Nebula v1.11 requires Go 1.26, so the agent module does too.
  Every Nebula release new enough to issue v2 certificates needs at least 1.25, so
  this was not avoidable by pinning an older one. CI and goreleaser read
  `go-version-file: go.mod` and follow automatically.
- **Memory.** The `<50 MB` design target comes under real pressure. State it as
  tiered — agent alone versus agent plus mesh — rather than quietly missing it.
- **cgo.** `miekg/pkcs11` is in Nebula's module graph but sits behind a `pkcs11`
  build tag, so `CGO_ENABLED=0` cross-compiles are unaffected.
- **Windows packaging.** TUN on Windows is Wintun; `wintun.dll` must ship
  alongside the executable, which means a goreleaser archive change.
- **FreeBSD.** Nebula's FreeBSD TUN support is thinner than Linux or Windows.
  Smoke-test before promising it.

## Configuration sketch

Not final — recorded so the shape can be argued with before it is written.

```yaml
nebula:
  enabled: false               # opt-in; default off
  source: "platform"           # platform | file
  config_file: "/etc/agent/nebula.yaml"          # source: file
  cache_file: "/var/lib/agent/nebula-cache.yaml" # last-known-good
  sync:
    interval: "10m"            # 1m-1h; this is the revocation latency
  verify:
    timeout: "30s"             # handshake window before rollback
```

`source: file` exists so the feature is usable without the platform at all, and
so a device can be brought onto the mesh before it has a Thing record. It is not
a peer of `source: platform` and should not grow into one: no revision probe, no
sync, no rollback. It is a path handed to `config.C.Load`, and the `sync` and
`verify` settings above do not apply to it.

## Staging

1. ~~**Don't die in the constructor.**~~ **Done.** It turned out much smaller than
   this document first implied: `nats.go` is already the supervisor for NATS, so
   no subsystem framework was needed. `RetryOnFailedConnect(true)`, the JetStream
   probe moved from a fatal one-shot to a per-connect check surfaced in
   `cmd.health`, and — the part that was not obvious — a `ClosedHandler` that
   exits the agent when `nats.go` abandons the connection for good. Retrying on
   failed connect had silently removed the revoked-credential recovery path, which
   depended on the old constructor failure to exit the process and let the service
   manager restart it into a fresh credential sync.
2. ~~**`internal/nebula`.**~~ **Done.** Fetch, cache last-known-good, `nebula.Main`
   plus `Control`, verify-and-roll-back, reload on revision change. The apply
   ladder needs a real TUN device, so it is covered by tests on a host rather
   than in `go test`; the unit tests cover the state machine around it.
3. ~~**Surfaces.**~~ **Done**, less the IP-forwarding read, which was cut — see
   the settled questions below.
4. **`leaf-sync`.** The same feature against `leaf_nodes.nebula_host`, when site
   nodes are wanted on the mesh.

## Open questions

- Does any real deployment want NATS on the overlay, or is underlay-always good
  enough to document as a recommendation rather than support as a case?
Settled during review, recorded so they are not reopened:

- **`sync` does not report "no change" separately.** `ConfigRevision` already
  says it: if the revision moved, something changed. A field that restates an
  existing field is how a payload rots.
- **`IPForwarding` is cut from the first version.** No gateway has been deployed
  yet, and `UnsafeNetworks` — read from the certificate, for free — already says
  the host is *certified* as a gateway, which is the half that matters when
  diagnosing one. The forwarding read can arrive with the first real gateway and
  costs nothing to add then. The section on
  [gateways](#gateways-and-unsafe-routes) still stands: when it is added, it
  detects and reports, never mutates.

- **Self-restart on a config the reload could not apply: yes.** Converge
  automatically. See the ladder in [Apply, verify, roll back](#apply-verify-roll-back)
  — it is expressed in outcomes, so no field table is needed to implement it.
- **Mesh state lives in `cmd.health`, not a telemetry subject.**
