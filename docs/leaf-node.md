# Leaf Nodes

Run a site's NATS leaf node, keep its KV buckets in step with the hub, and
serve health locally — all from the same agent binary that already manages the
box.

---

## What a "gateway" is here

Nothing. There is no gateway mode, no `edge.enabled` key, and no separate kind of
record on the platform.

A gateway is a **Thing** whose agent happens to have more capabilities turned on.
Its `thing_type` on the platform already says it is a gateway; a second marker in
the config would be a second thing to get wrong, and `edge.enabled: false` beside
`sync.twin: true` has no correct behaviour. So the agent decides for itself:
if any of `nats.server_config`, a `sync:` declaration or `observability.addr` is set,
the edge goroutine has work to do.

This also means you can take any one of them on its own. A box that serves
`/ready` but hosts no server is fine. A box that hosts a plain embedded broker
with no platform at all is fine — `nats.server_config` points at *any*
`nats-server` config file, not only one this agent generated.

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
the creds (0600). Then it exits.

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

> **`twin.enabled` is gone.** It is rejected by name at config load. It never
> worked — the hub's JetStream domain never reached the running agent, so it
> disabled itself on every start — so there is nothing to migrate, but a config
> file still carrying it would otherwise be silently ignored. Write
> `sync: { twin: true }`.

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
answer. Aggregating N sites at the hub means N sources all named `KV_twin`;
same-named sources need the server's internal `iname`, which nats.go does not
expose. The alternative is `twin_<code>` at every edge, which makes a rule engine
read a different bucket name at every site. Not worth it — hence the relay for
this one direction. Do not "finish the job" by making reported state a source
without solving that.

### Relay mechanics

Boring on purpose:

- **One watcher**, edge → hub. There is no reverse pump, so there is no echo.
- **Compare before write.** A value already equal at the hub is skipped. This is
  an optimisation, not the safety property — the watcher replays every current
  value on start, so without it each restart would rewrite the bucket and burn a
  revision per key.
- **That replay is also the resync.** Reconnecting after an outage walks every
  current value, so there is no separate catch-up path to get wrong.
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

**An islanded site warns rather than fails, and still answers 200.** Local NATS
keeps working and devices keep running; that autonomy is the entire reason a leaf
node exists, so reporting it as unready would invert the design.

| Metric | What it says |
|---|---|
| `agent_edge_nats_connected` | 1 when this agent is connected to the local leaf |
| `agent_edge_hub_uplink_connected` | 0 = islanded |
| `agent_edge_nats_connections` | Devices actually attached at this site |
| `agent_edge_jetstream_bytes` | The number to watch on a small edge disk |
| `agent_edge_sync_up{bucket,direction}` | 0 = declared and not syncing |
| `agent_edge_relay_pending{bucket}` | The outage backlog: keys waiting on the hub |

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

1. In the console, create the site's **Thing** (its `thing_type` says gateway).
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
   ```

3. `agent -leaf-config` → writes `/etc/agent/nats-leaf.conf` and the creds.
4. Start the leaf: either `nats-server -c /etc/agent/nats-leaf.conf` under your
   init system, or set `nats.server_config` to that path and let the agent host
   it.
5. `agent -service install && agent -service start`.
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
with a `domain` column and one server-provisioned NATS user. `thing_types` already
says whether a device is a gateway, and the `domain` column was a second copy of
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
