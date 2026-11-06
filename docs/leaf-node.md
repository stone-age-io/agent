# Leaf Nodes

Run a site's NATS leaf node, keep its KV buckets in step with the hub, and
serve health locally — all from the same agent binary that already manages the
box.

---

## What a "gateway" is here

Nothing. There is no gateway mode, no `edge.enabled` key, and no separate kind of
record on the platform.

A gateway is a **Thing** whose agent happens to have more capabilities turned on.
Nothing on the platform marks it: `thing_types` has no gateway field, and
"gateway" is at most a naming convention a tenant picks. A marker in the config
would only be a thing to get wrong, and `edge.enabled: false` beside
`sync.twin: true` has no correct behaviour. So the agent decides for itself: if
either `nats.server_config` or a `sync:` declaration is set, the edge goroutine
has work to do.

This also means you can take either one on its own. A box that hosts a plain
embedded broker with no platform at all is fine — `nats.server_config` points at
*any* `nats-server` config file, not only one this agent generated.

`observability.addr` is **not** in that list, although it was before 0.3.0.
Serving `/ready` is not leaf work: every agent does it, gateway or not, and the
agent owns that endpoint. A gateway simply contributes three more checks to it —
`nats_local`, `hub_uplink` and `sync` — which are the ones that need a leaf on
the box to mean anything.

> **Previously `leaf-sync`.** This was a separate binary in the platform
> repository. It also mirrored an organization's config collections into the
> edge's local KV; that half was dropped rather than moved, because nothing
> consumed the mirrored rows. See [What went away](#7-what-went-away).

---

## 1. Bootstrapping the leaf config

```sh
agent -leaf-config
```

One shot: it authenticates as this agent's Thing, calls `GET /api/me/leaf-config`,
and writes two files next to `nats.auth.creds_file` — `nats-leaf.conf` (0644) and
the creds (0600). Then it exits. The conf names a third, `nats-leaf.pid`, which
the leaf writes when it starts; see [Keeping the account JWT current](#keeping-the-account-jwt-current).

**Every path in `nats-leaf.conf` is absolute** — `credentials`, `store_dir`
(`<confdir>/jetstream`) and `pid_file` — computed from the conf's own directory
when the file is written. `nats-server` resolves a relative path against its
working directory, not the config file's, so the relative paths older agents
wrote pointed somewhere else whenever the leaf ran from another directory, as a
service usually does. (The platform applies the same rule to `nats export`.) The
cost is that the directory is not relocatable: move it, and re-run
`agent -leaf-config` there.

**The leaf needs write access to its config directory** — the same access it
already needs to create its JetStream store there.

It has to be separable from `run`, because the usual edge shape is a separately
supervised `nats-server`, and that server needs its config file to exist *before*
it starts — which is before the agent has anything to connect to. Bootstrapping
and running cannot be the same invocation.

It requires `nats.auth.type: "platform"` and a `platform:` block. Everything it
fetches is either public trust material or this thing's own credential, so the
platform serves it to any authenticated Thing and gates it on nothing:

| Field | What it is |
|---|---|
| `code` | This thing's code |
| `domain` | The leaf's JetStream domain — **the same string as `code`** |
| `creds` | This thing's own NATS credential |
| `account_jwt` / `account_pub` | The organization's NATS account |
| `operator_jwt` | The NATS operator |
| `sys_account_jwt` / `sys_account_pub` | The `$SYS` account (see §2) |
| `hub_leaf_url` | Where the leaf remote dials the hub |
| `hub_domain` | The hub's own JetStream domain |

Account *seeds* and signing keys are never served, and a `$SYS` **user**
credential — the one that would actually let you connect as `$SYS` — does not
exist on this path at all.

**The JetStream domain is the thing's code, computed rather than stored.** The
platform derives it; `buildLeafConf` writes it into both `server_name` and
`jetstream { domain }`. The console matches a leaf's reported `server_name` back
to a Thing's code to show a site attached, so those two must not diverge. There
is no `edge-` prefix and no org segment in it: JetStream is already scoped to the
account, so the account is the namespace.

---

## 2. Two directives that are not optional

A generated `nats-leaf.conf` has to satisfy operator-mode validation, and **no
string assertion can check that**. Both of these were missing for months, so the
generator produced a file `nats-server` refused to load — invisible, because the
only tests were substring checks over the output.

1. **Every leaf remote needs an `account` key** naming the local account.
2. **`resolver_preload` needs the `$SYS` account JWT**, not just the
   organization's. The operator JWT names a system account, and `resolver: MEMORY`
   has nowhere to fetch it — so without it the server dies with
   `error resolving system account: account missing`, *before JetStream starts*.

Preloading the `$SYS` **account** JWT is public trust material and grants nothing.
Connecting *as* `$SYS` needs a `$SYS` **user** credential, which is never served.
Those are different objects and it is worth being precise, because the first one
looks alarming and is not.

`TestBuildLeafConfIsAcceptedByNATSServer` runs the real generator's output through
the `nats-server` package's own `ProcessConfigFile` + `NewServer` — no ports, no
network. **Keep it, and do not replace it with more `Contains` checks.**

---

## 3. Running the server

Two shapes, and the two-process one is the default for a reason.

**Separately supervised (default).** Leave `nats.server_config` empty and let
systemd or Docker run `nats-server -c nats-leaf.conf`. The bus then survives an
agent restart, which is what you want when upgrading the agent on a live site.

**In-process.** Set `nats.server_config` to the config file and the agent hosts
the server itself. The edge is one supervised service instead of two, and
"regenerated the config, forgot to restart the server" stops being possible.

```yaml
nats:
  urls: ["nats://127.0.0.1:4222"]
  server_config: "/etc/agent/nats-leaf.conf"
```

`nats.urls` must name the port that config listens on — startup refuses the pair
if they disagree, because otherwise nothing in the process would ever reach the
server and the symptom is a silent retry loop.

**`nats-server` links into the binary either way.** It is not conditional on the
setting, so a scanner flagging a `nats-server` CVE against this binary is
reporting code that does not run unless you configured it to. The size cost is
paid on every install regardless; noted so the number is not a surprise.

### Keeping the account JWT current

A leaf runs `resolver: MEMORY`: the only account JWT it knows is the one in
`resolver_preload`. **NATS user revocations live inside that JWT** — pb-nats
re-signs the account with the revocation list — so a leaf that never re-reads
it keeps letting a revoked device in. The agent re-reads it:

```yaml
platform:
  leaf_refresh_interval: "5m"   # default. 0 turns the timer off; 30s–1h otherwise
```

Every interval the agent asks the probe route, `GET /api/me/account-jwt`, for
the account JWT alone. **Only when that differs from the JWT it last applied —
or nothing has been applied since the agent started — does it fetch the full
`GET /api/me/leaf-config`**, build `nats-leaf.conf` with the same code
`-leaf-config` uses, and compare it byte for byte with the file on disk. Equal:
nothing happens. Different: it rewrites the conf (temp file and rename, 0644 —
**never the creds**, which stay the credential sync's job) and has the leaf
reload it. The first run is one interval after start, and always does the full
fetch, which is how an upgraded agent rewrites a conf an older one generated.
`cmd.leaf_refresh` runs one now; see below.

There is no key that turns this on. It runs where `agent -leaf-config` has
written a `nats-leaf.conf` — beside `nats.auth.creds_file` — on a
platform-managed agent, and the startup log says which file it watches and how it
will reload the leaf, or that it is inactive. A hand-written `nats-server`
config elsewhere is never touched.

How the leaf is reloaded depends on who runs it:

| Leaf | Reload |
|---|---|
| **In-process** (`nats.server_config` is that file) | In-process, the same reload `SIGHUP` triggers. Every platform. |
| **Separately supervised**, Linux or FreeBSD | `SIGHUP` to the pid in `nats-leaf.pid`, which the generated conf's `pid_file` line makes the leaf write. |
| **Separately supervised**, Windows | **Not attempted.** The agent rewrites the file, logs that the leaf must be restarted, and `leaf_preload` warns until it is. |

The pid file sits beside the conf, which is why the leaf needs write access to
that directory: `nats-server` refuses to start if it cannot write it.

#### Upgrading: restart the leaf once

A conf generated by an older agent has no `pid_file` and relative `credentials`
and `store_dir`. The upgraded agent's first refresh rewrites it, and **a running
leaf cannot adopt that by reload**:

- **`nats-server` refuses to reload a changed `store_dir`** while JetStream is
  running (`config reload not supported for jetstream storage directory`,
  `server/reload.go:1830` in v2.15.0), and the refusal is all-or-nothing:
  nothing in the new file takes effect, the account JWT included. An in-process
  leaf reports this as the refresh's error (logged, and in a
  `cmd.leaf_refresh` reply). A separately supervised one has no pid file yet, so
  the refresh fails before signalling it and says to restart it.
- That error is reported on the run that rewrote the file. After it the file on
  disk is current and the refresh is quiet, so from then on **`leaf_preload` is
  what says it**: it warns as soon as the account JWT moves and the leaf has not
  loaded it — which, until the restart, is every revocation.

So after upgrading, make sure the conf is the new one — re-run
`agent -leaf-config`, or let the first refresh (or `cmd.leaf_refresh`) rewrite
it — and then **restart the leaf once**: the separately supervised
`nats-server`, or the agent when it hosts the leaf. Upgrading an agent that
hosts its leaf restarts that leaf too, but on the old file, because the first
refresh runs one interval later; re-running `agent -leaf-config` before the
upgraded agent starts saves the second restart. Every later change then
reaches the leaf without one (except a separately supervised leaf on Windows,
above).

**Where the leaf's JetStream data was.** With the old relative
`store_dir: "./jetstream"`, the data lived in `jetstream/` under the leaf's
*working directory* — the conf's directory only if the leaf happened to run
from there (for a service, typically `/` under systemd or
`C:\Windows\System32` for a Windows service). After the restart the leaf uses
`<confdir>/jetstream`. Either **move the old `jetstream` directory to
`<confdir>/jetstream` before the restart**, or accept a resync: hub → edge
mirrors refill from the hub, and any relay backlog not yet sent to the hub is
lost.

**A changed operator or `$SYS` account is not reloadable**, and the agent does not
try: `nats-server` refuses to reload a new operator. The refresh leaves the file
alone and logs, on every run, that you must re-run `agent -leaf-config` and
restart the leaf. That is a deployment-wide event — a new operator — not a
revocation.

**Revocation latency at a site.** A device revoked at the hub stops connecting
at its site's leaf within one `leaf_refresh_interval` (plus however long the
platform takes to re-sign the account), or at once after `cmd.leaf_refresh`.
"Stops connecting" includes the connection it already has: on reload,
nats-server closes a revoked user's live connections ("authentication revoked")
and refuses its reconnect (`Authorization Violation`), while every other user
stays connected. `TestLeafReloadAppliesRevocation` pins that against the real
server. The exception is a separately supervised Windows leaf, whose latency is
whenever someone restarts it.

**What crosses the wire.** `/api/me/leaf-config` includes this thing's own
`.creds`, nkey seed and all. The probe, `/api/me/account-jwt`, carries only the
account JWT and its public key — public trust material — so a routine refresh
that finds nothing changed puts no key material on the wire, as the credential
sync's `?fields=updated` probe does. The full read, and with it the credential,
happens on the first run after start and when the account JWT changed. The
agent discards the creds it receives there.

**Platform version.** The probe route is newer than `/api/me/leaf-config`. A
platform without it answers 404; the agent then reads the full leaf config on
every run, as before, and logs a warning once. Anything else that fails the
probe fails that refresh, and the next one retries.

#### `cmd.leaf_refresh`

```
{prefix}.{code}.cmd.leaf_refresh
```

Empty request body. Runs one refresh now — the thing to send after revoking a
device that must stop connecting at a site immediately — and answers with the
outcome:

```json
{"status": "success", "changed": true, "reloaded": true, "ts": "2026-10-13T12:00:00Z"}
```

`changed` is whether `nats-leaf.conf` was rewritten; `reloaded` is whether the
leaf was told to re-read it. `changed: true` with `status: "error"` is the case
to act on: the file is current and the running leaf is not, and `error` says
why (no pid file, Windows, a reload the server refused). An agent with no
generated conf answers with `status: "error"` rather than timing out.

Unlike `cmd.nebula`, it acts first and answers afterwards. A Nebula action can
cut the tunnel the request arrived through; a leaf reload drops only the
connections the new JWT revokes, so the reply can wait for the result.

---

## 4. KV sync

Off by default, because it moves data-plane traffic and an upgrade must not
silently start doing that. It needs the `platform:` block, since the hub's
JetStream domain arrives with the leaf config rather than being configured per
box.

Two directions, two mechanisms, a list each:

```yaml
sync:
  twin: true                 # preset: the two digital-twin buckets

  mirrors:                   # hub -> edge, maintained by the server
    - bucket: recipes
      keys: "line-a.>"       # optional. CANNOT be changed later — see below

  relays:                    # edge -> hub, pumped by the agent
    - bucket: events
      keys: "site.S01.>"     # optional
```

`sync.twin: true` is shorthand for the two buckets the platform already knows:

| Bucket | Written by | Flows | Mechanism |
|---|---|---|---|
| `twin` | the device, at the edge | edge → hub | relay |
| `twin_desired` | operators, at the hub | hub → edge | JetStream **mirror** |

The point is edge autonomy: a site whose uplink drops keeps writing reported state
locally and catches the hub up when the link returns, while still reading the
last-known desired state from its local mirror.

### A bucket belongs to one list

A bucket named in both directions has two writers, and two writers oscillate
rather than converge (see the next section). The agent refuses to start and names
the bucket. With the two built-in buckets that was unrepresentable; with a list
it is one typo away.

### `keys:` is a key pattern, in both directions

Write `line-a.>`, never `$KV.recipes.line-a.>`. The agent builds the subject a
mirror's filter needs and hands the same pattern straight to the relay's watcher.
A `$KV.` written here is rejected, because it would otherwise be doubled and
match nothing.

On a **relay**, `keys:` is what makes a site physically unable to relay another
site's keyspace up, rather than merely conventionally unlikely to.

On a **mirror**, it cannot be changed afterwards. nats-server rejects any change
to a mirror block on an existing stream:

```
JSStreamMirrorNotUpdatableErr (10055): stream mirror configuration can not be updated
```

So narrowing an existing mirror means deleting the bucket at every site and
letting the agent recreate it. Get it right the first time. The agent detects the
mismatch and reports it in `/ready` rather than repairing it.

### The agent does not create hub buckets

A bucket in `sync.mirrors` or `sync.relays` must already exist at the hub; the
agent creates only the local side. A typo in one site's YAML that creates a local
bucket is that site's problem, but one that creates a *hub* bucket is everyone's,
with whatever retention that site happened to guess — and the console then adopts
it. The two preset twin buckets are the exception, because the platform knows
their shape.

The hub side is checked whenever an entry is wired up — at start, and again on
every retry while it is down — in **both** directions, and a missing one is
reported in `/ready`. That matters most for a mirror: JetStream cannot validate
a cross-domain mirror source when the stream is created, so a mirror of a bucket
the hub does not have would otherwise be accepted, look healthy, and receive
nothing for ever.

Wiring is retried every 60 seconds while an entry is down, so the ordinary
order — install the gateway, then create the bucket at the hub — comes right on
its own within a minute, with no restart.

### One writer per bucket is the whole safety property

A single bucket written from both ends does not pick a loser on a conflict — it
*oscillates*. Two concurrent values for one key swap across the link, then swap
back, each write generating the next event. Measured at roughly 170,000 writes to
a single key in 300 ms before the buckets were split.

Encoding the owner in the key (`thing.S01.state.temp`) buys the same safety and
was tried and reverted: it taxes every key in firmware, rule configs, widgets and
docs, and a mistyped segment silently never syncs, with no error anywhere. Two
buckets makes the conflict unrepresentable and costs one noun. **Do not merge
them.**

### Why desired is a mirror and reported is a relay

**Desired state is a mirror** because it has exactly one origin. Mirrors forward
writes to the origin transparently, and that write would fail during a WAN
outage — but the edge never writes desired state, so it never comes up. Reads are
served locally from the last-known values, which is precisely what you want when
the link is down. The mirror is configured on the *receiving* side, so there is no
hub-side stream to mutate and no race between sites.

**Upstream cannot be a source,** which would otherwise be the symmetric
answer. The mechanism itself works: a hub `twin` bucket can source every site's
`KV_twin`, because the server tells same-named sources apart by their domain's
API prefix. What fails is managing it. Every site is an entry in **one** source
list on the hub's stream, and that list has no safe writer:

- A stream update replaces the whole list. There is no call to add or remove one
  source, and no expected-revision check to catch a concurrent edit.
- The server cancels every source the new list leaves out. Two sites adding
  themselves at the same moment do not merely lose an add — each removes the
  other.
- Nothing owns removing a decommissioned site, and a site whose agent still runs
  would put itself back.
- The Control Plane, which knows the fleet, has no credential in the
  organization's account, so it cannot hold the list either.

The relay needs no shared configuration: each site owns its own path upstream.
Hence the relay for this one direction. Do not "finish the job" by making
reported state a source without solving the list. (An earlier version of this
section blamed the server's internal `iname`; that was wrong.)

### Relay mechanics

Boring on purpose:

- **One watcher**, edge → hub. There is no reverse pump, so there is no echo.
- **Live changes are published without waiting.** Each change is a JetStream
  publish to the hub bucket's subject, and the relay moves on without waiting for
  the hub's answer. Waiting on a read and then an acknowledgement for every change
  held a relay to one change per two round trips (5 a second over a 100 ms link);
  publishing ahead lets the writes overlap (about 1,300 a second over the same
  link). Writes leave on one connection and the server stores them in the order
  sent, so the last value written for a key is the one the hub keeps.
- **The replay compares before it writes.** On start the watcher replays every
  current value, and each one is checked against the hub first, so a restart does
  not rewrite the bucket, burn a revision per key, and wake every hub-side
  watcher for nothing. It costs a round trip per key, once per start.
- **Failures are held and retried.** Every 30 seconds the relay collects the
  hub's answers. A key whose write failed is re-sent with the value the edge
  holds *then*, never the one that failed, so a retry cannot overwrite a newer
  reading. That is the catch-up after an outage: the local watcher does not die
  when the WAN does, so nothing else would re-offer a value that failed and then
  never changed. Each write waits at most 10 seconds for an answer
  (`relayAckTimeout`); nats.go sets no such limit by default, and without one a
  lost acknowledgement would leave its key unconfirmed for good.
- **Deletes are relayed explicitly.** A KV delete is a tombstone message, not an
  absence; dropping it would leave the key live at the hub forever, because the
  equality check only compares values that exist. (A purge is relayed as a
  delete — the key goes away either way, but history rollup is domain-bound.)
- **Upsert, never reconcile.** The relay acts only on what this site's bucket
  reports, so one site can never purge another site's keys from the hub.
- **The watcher is supervised** with 1s→30s backoff: nats.go reconnects the
  connection, but a dead watcher stays dead.
- **Failure is soft.** If any of it cannot start — no hub domain, bucket
  unreachable, mirror rejected — it logs why and carries on with what it can.
  A bucket that would not come up is retried every minute (an unknown hub
  domain is not: that needs a restart), and a failure that repeats with the
  same reason is logged once rather than every minute.

Buckets are created if absent and otherwise **left alone**. Unlike a private
mirror, these are shared with the console and with operators, so the agent does
not reassert retention over whatever they set. Keep `bucketConfig()` in step
with `TWIN_BUCKET_CONFIG` in the platform's `ui/src/utils/twin.ts` — whoever
creates a bucket first defines it, and the two now live in different repositories,
so nothing can enforce that they agree.

> **Operational note.** Enabling this makes the agent load-bearing for reported
> state. Down, it no longer just means stale config — it means a frozen twin in
> the console while the site itself runs fine.

---

## 5. Readiness and metrics

```yaml
observability:
  addr: "127.0.0.1:9100"    # 0.0.0.0:9100 to scrape from elsewhere
  metrics_token: ""         # empty = open; Bearer or Basic when set
  interval: "15s"
```

```sh
curl -s localhost:9100/ready
curl -s localhost:9100/metrics
```

The same report is also in `cmd.health`, under `checks`, so a fleet dashboard
sees exactly what a local scrape sees — one registry, two channels. A gateway
adds `nats_local` (fail), `hub_uplink` (warn) and `sync` (warn) to the checks
every agent already runs. `leaf_preload` (warn) is registered on every agent and
skips unless there is a generated `nats-leaf.conf` to keep current — it has to,
because a separately supervised leaf with no `sync:` declared runs no edge
subsystem at all, and that is the shape that most needs it.

A site's real health can only be measured on the site. `cmd.health` travels over
NATS, which is the link that breaks — the box you most need to ask is the one
whose uplink is down, and that is exactly when it goes quiet. The Control Plane
cannot help either: it holds the NATS operator and the `$SYS` account and has no
credential inside any organization's account, so the most it can report is how
many devices are *configured*.

The checks always run and always log a readiness transition. Setting `addr` is
what makes them reachable; leaving it empty serves neither endpoint, and a bind
failure is logged rather than fatal.

| Check | State when it trips |
|---|---|
| `nats_local` | **fail** — the agent is not connected to the local leaf |
| `hub_uplink` | **warn** — no outbound leaf connection: this site is *islanded* |
| `sync` | **warn** — a declared bucket could not be brought up, and says which |
| `leaf_preload` | **warn** — the leaf enforces a different account JWT from the one the platform last served, so a device revoked since then can still connect here |

`leaf_preload` asks the leaf's loopback monitoring port for
`/accountz?acc=<account>` — the same credential-free local read `hub_uplink`
makes — and compares the JWT the server has loaded with the one the last refresh
fetched. It skips until the first refresh has run, and when the port is
unreachable. When it warns, the agent log says why the leaf did not reload; the
usual answers are "restart the leaf once" (started before `pid_file` existed) or
"restart it after each change" (Windows, separately supervised).

**An islanded site warns rather than fails, and still answers 200.** Local NATS
keeps working and devices keep running; that autonomy is the entire reason a leaf
node exists, so reporting it as unready would invert the design.

Every agent's `/metrics` carries `agent_ready`, `agent_check_state{name,state}`
(1 on each check's current state), `agent_check_timestamp_seconds` and
`agent_build_info{version}`, plus the Go runtime and process collectors. A
gateway adds:

| Metric | What it says |
|---|---|
| `agent_edge_nats_connected` | 1 when this agent is connected to the local leaf |
| `agent_edge_hub_uplink_connected` | 0 = islanded |
| `agent_edge_nats_connections` | Devices actually attached at this site |
| `agent_edge_jetstream_bytes` | The number to watch on a small edge disk |
| `agent_edge_sync_up{bucket,direction}` | 0 = declared and not syncing |
| `agent_edge_relay_pending{bucket}` | Keys the hub has not confirmed yet: writes in flight plus writes waiting to be retried. Small and moving when healthy; grows during an outage |

The server-derived rows come from the leaf's own loopback monitoring port — the
`http:` line the generated `nats-leaf.conf` carries. It is unauthenticated by
design, and it is how the edge reads its own server **without ever holding a
`$SYS` user credential**. It works the same whether the leaf runs embedded or as a
separate process.

When that port is unreachable those series are **omitted rather than reported as
zero**. Zero would claim an islanded site with no devices, which is a much louder
statement than "not scraped". The same rule governs the check registry, where
`skipped` ranks *below* `ok` — a report that is entirely skipped must not read as
a clean bill of health.

---

## 6. Deploy flow

1. In the console, create the site's **Thing** (any type; nothing marks it as a gateway).
   Copy the login password from the success dialog.
2. Install the agent and write `/etc/agent/config.yaml`:

   ```yaml
   code: "s01"
   platform:
     url: "https://platform.acme.io"
     identity: "s01@things.acme.io"
     password_env: "AGENT_PLATFORM_PASSWORD"
   nats:
     urls: ["nats://127.0.0.1:4222"]
     auth:
       type: "platform"
       creds_file: "/etc/agent/device.creds"
   sync:
     twin: true
   observability:
     addr: "127.0.0.1:9100"
   tasks:
     service_check:
       enabled: false   # on by default, and refuses to start with no services listed
   ```

   The default `commands.scripts_directory` has to exist too; the install
   guides create it.

3. `agent -leaf-config` → writes `/etc/agent/nats-leaf.conf` and the creds.
4. Start the leaf: either `nats-server -c /etc/agent/nats-leaf.conf` under your
   init system, or set `nats.server_config` to that path and let the agent host
   it.
5. `agent -service install && agent -service start`. From here the agent keeps
   the leaf's account JWT — and so its revocations — current on its own; there
   is no need to re-run step 3 unless the operator changes.
6. Point any site-local rule engine at the same creds file.

The console shows the site as attached on the Thing's own page, in its NATS card.
That reading comes from asking the hub (`$SYS.REQ.ACCOUNT.PING.CONNZ`), not from
anything this agent publishes — see §7.

---

## 7. What went away

Three things that used to exist here do not, and each has its own reason.

**The config mirror.** `leaf-sync` copied an organization's `things`, `locations`,
`thing_types`, `location_types` and `thing_type_operations` into the edge's local
KV so devices could read them offline. Nothing consumed the mirrored rows — no
rule-router rule, no firmware — so it was moving data nobody asked for, and it was
the reason an edge identity needed read grants spread across the inventory. If a
real consumer appears, build that consumer's read path rather than a general
mirror.

**The `leaf_nodes` collection.** With the mirror gone, a leaf node was a Thing
with a `domain` column and one server-provisioned NATS user. Nothing needed to mark
a device as a gateway at all, and the `domain` column was a second copy of
the code that could disagree with it.

**The `leaf_status` heartbeat.** The agent used to write a liveness beat into a
hub KV bucket, and the console rendered it as online/offline. It is gone and
nothing replaced it on this side. A heartbeat travels over the very link whose
failure it is meant to report, so a missing beat could not distinguish "edge box
down" from "WAN down" from "agent crashed". The hub always knows which leaves it
is holding, so the console asks the hub directly over its own in-account
connection.

**If that console badge ever starts timing out for one organization and not
another**, the cause is almost certainly this: in NATS a publish **DENY beats a
publish ALLOW**. A `nats_roles` entry carrying `$SYS.>` in its publish deny list
cannot reach the account-scoped endpoints no matter what its allow list says — and
the symptom is a plain request timeout, with the real reason arriving
asynchronously on the connection's error handler and never on the request. Deny
`$SYS.REQ.SERVER.>` instead.

---

## See also

- **[Platform Credentials](./credentials.md)** — how the agent authenticates as its Thing
- **[Architecture](./architecture.md)** — where the edge goroutine sits
- **[Nebula Overlay](./nebula.md)** — the other thing a site often runs
