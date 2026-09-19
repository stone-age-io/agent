# Edge Sync — Design

**Status: implemented.** All four steps of [Staging](#staging) landed together.
Like
[nebula-design.md](nebula-design.md) this is not an installation guide and is
deliberately not linked from the README's documentation index, which lists
shipped behaviour only. Read it as the record of a decision and of the
alternatives rejected on the way to it.

The whole proposal is one sentence: **the twin's two hardcoded buckets become
two short lists of buckets, and nothing else changes.**

A first draft of this document also proposed replicating JetStream *streams*
upstream with durable consumers and `Nats-Msg-Id` dedupe. That is cut. See
[What we are not building](#what-we-are-not-building).

## What it is

Two directions, each a list of buckets.

**Down (hub → leaf).** A bucket at the hub is **mirrored** into this leaf's
JetStream domain under the same name, optionally narrowed to a key prefix so a
site carries only the keys it needs. The server maintains the copy; the agent
only declares it. The leaf never writes it, so reads keep serving last-known
values when the link is down.

**Up (leaf → hub).** A bucket in this leaf's domain is **relayed** to the
same-named bucket at the hub, optionally narrowed to a key prefix. The agent
watches the local bucket and copies each change up, retrying while the link is
down.

A bucket may appear in one list or the other, never both: one bucket, one
writer, one direction.

Note the word *mirrored*, not *sourced*. A JetStream **source** is a different
mechanism with a specific meaning here, and it is the one
[rejected for the upstream direction](#what-stays-exactly-as-it-is) — keeping
the two words apart is worth the pedantry.

## Prerequisite: the twin does not currently run

`internal/agent/edge.go:edgeConfig()` never sets `HubDomain`, and nothing else
does either. The only producer is `platform.Client.LeafConfig()`, called by the
`agent -leaf-config` one-shot, which writes `nats-leaf.conf` and exits without
persisting the hub domain anywhere the daemon reads.

So `twin.enabled: true` always falls through to the guard at the top of
`startTwin` and logs

    ⚠️ edge: twin sync enabled but nats.hub_domain is unset; disabled

naming a config key that does not exist. Every line below that guard is dead at
runtime.

Two consequences:

- **Fix it first.** The hub domain must reach the running agent — persisted into
  the platform session file at bootstrap, parsed back out of the generated
  `nats-leaf.conf`, or fetched at startup under platform auth. Whichever, the
  error message must stop naming a key that was never implemented.
- **Nothing is deployed, so nothing migrates.** That matters mostly for one
  decision below, where a default is otherwise very expensive to change later.

## What stays exactly as it is

The two mechanisms and the reason for each are unchanged. They are already
correct and this proposal does not touch them:

| Direction | Mechanism | Why |
|---|---|---|
| hub → edge | JetStream **mirror** | one origin, server-maintained, no code in the data path, serves last-known values offline |
| edge → hub | **application relay** | N origins aggregated; native sourcing needs the server's `iname`, which nats.go still does not expose |

So does the naming rule, which is easy to mistake for an accident: **a bucket
has the same name at both ends.** That is the whole reason the upstream
direction is a relay rather than a source — a source would force `twin_<code>`
at every site, and then the console and the rule engine would read a different
bucket name per site. The generalisation must therefore *not* offer a rename
knob in either direction. Same name both ends, or the reason the relay exists
evaporates.

And the safety property is unchanged: **one writer per bucket.** That is what
makes the conflict unrepresentable rather than merely unlikely.

All that changes is that each direction takes a *list* instead of one built-in
name.

## The one new risk, and the one check that closes it

Today the invariant holds because there are two buckets going opposite
directions and no way to express anything else. A config list lets someone name
`foo` in both lists and rediscover the oscillation measured at ~170,000 writes
to one key in 300 ms.

So the invariant stops being structural and has to be restored by a check:

> **A bucket may not appear as both a mirror target and a relay source.** Refuse
> to start, naming the bucket.

One comparison at config load. That is the entire safety cost of the feature,
and it is worth paying attention to precisely because it is the only thing
standing where a compiler used to.

CLAUDE.md's note should be restated to match: the rule was never "there are
exactly two buckets", it was "no bucket has two writers".

## Who creates what

`openOrCreateKV` deliberately never modifies an existing bucket, so an agent
cannot silently revert retention an operator set in the console. Keep that. With
a list of buckets it needs one addition, which replaces a whole family of
options this document previously proposed (per-entry `manage:` flags, retention
fields, create-or-update policy — all cut):

- **Local buckets: create if absent, one fixed shape, never modify.** It is this
  box's own domain and the shape only matters locally.
- **Hub buckets: must already exist.** Skip the entry and warn if not.
- **Exception: the `twin` preset creates both,** as it does today, because those
  two names and their shape are the platform's, not a user's.

The reason for the asymmetry is blast radius. A typo in one site's YAML that
creates a local bucket is that site's problem. A typo that creates a hub bucket
is everyone's problem, with whatever retention that one site happened to guess,
on a bucket the console will then adopt.

A bucket that exists but disagrees with the declaration — most importantly,
exists but is not a mirror — is warned about and skipped, exactly as
`ensureDesiredMirror` does today. No flag, no repair mode.

## The mirror filter cannot wait

This is the one place where "we can add it later" is false, so it goes in the
first change even though nobody has asked for it.

Today `ensureDesiredMirror` mirrors the whole of `KV_twin_desired` with no
filter, so **every site holds every other site's desired state**. That is a
fanout cost and a blast radius that both scale with the fleet.

`StreamSource.SubjectTransforms` fixes it — a site mirrors only the keys under
its own prefix. But nats-server rejects *any* change to a mirror block on an
existing stream:

    JSStreamMirrorNotUpdatableErr (10055): stream mirror configuration can not be updated

Adding the filter later therefore means deleting and recreating the bucket on
every site in the fleet, by hand, losing the local copy each time. Adding it now
costs one optional field. Nothing is deployed, so now is free and later is not.

This is not an argument for building things before they are needed. It is an
argument about one specific door that closes.

## Configuration sketch

```yaml
sync:
  twin: true                      # preset, replaces twin.enabled. Expands to the
                                  # two entries with the shape the console agrees on.

  mirrors:                        # hub -> edge
    - bucket: recipes
      keys: "line-a.>"            # optional; CANNOT be changed later

  relays:                         # edge -> hub
    - bucket: events
      keys: "site.S01.>"          # optional
```

There is no `name` or `local_name` field, in either direction, for the reason in
[What stays exactly as it is](#what-stays-exactly-as-it-is): a bucket has the
same name at both ends or the relay has no reason to exist.

`sync.twin: true` is expanded by the config loader into two ordinary entries
carrying an unexported `preset` flag (which is what permits hub-side creation).
Everything downstream — the startup loop, the metrics, the check, the tests —
then sees one kind of thing. Two lists instead of one polymorphic list with a
`kind:` field, because every reader would have to branch on `kind` before doing
anything, and mirrors and relays share no validation.

`edgeEnabled()` grows a `cfg.Sync.Any()` term. It does not grow a
`sync.enabled` key, for the same reason it has no `edge.enabled` key: a flag
naming the role is a second control that can disagree with the first.

### `keys:` means keys, in both directions

Both directions take the same field, spelled the same way, holding a KV key
pattern — `line-a.>`, not `$KV.recipes.line-a.>`. The agent builds whatever the
mechanism underneath actually wants:

- **Mirror:** a subject filter on the mirror's `SubjectTransforms`, so the agent
  prefixes `$KV.<bucket>.` itself.
- **Relay:** the pattern goes straight to `WatchFiltered` instead of `WatchAll`.

Two mechanisms, one user-facing idea. Making an operator write a `$KV.` subject
in one list and a bare key in the other would leak an implementation detail into
config and invite a mismatch between the two halves of one key space.

`keys:` on a relay is also the enforcement half of the disjointness the
[both-lists check](#the-one-new-risk-and-the-one-check-that-closes-it)
protects: aggregation is safe today only because sites write non-overlapping
keys *by convention*, and a filtered watcher makes a site physically unable to
relay another site's keyspace up.

## Observability: two series and one check

`startTwin` is fail-soft with log lines only. That is fine for one hardcoded
pair on one known box and not fine for N entries an operator declared, where a
silently skipped entry looks exactly like a healthy agent.

The previous draft proposed four metric families. Two are enough to answer the
only two questions anyone asks:

| Series | Question it answers |
|---|---|
| `agent_edge_sync_up{bucket,direction}` | is this entry actually running? |
| `agent_edge_relay_pending{bucket}` | how far behind is it? |

`pending` is already tracked inside `pumpReported`; this exports it. Plus one
check covering all entries, which **warns, never fails** — an islanded edge with
a growing backlog is the design working, not a site to pull out of rotation. And
the existing rule holds: when the local server is unreachable these series are
**omitted, not zeroed**, because zero claims an empty backlog.

## What we are not building

### Stream replication

Rejected after being designed. The proposal was: the agent manages leaf-local
streams as spools, creates durable pull consumers, republishes to a pre-existing
hub stream, and dedupes with `Nats-Msg-Id`.

It is coherent, and it is too much machinery for the benefit. Recorded here so
the idea does not get re-derived from scratch:

- **It is not a generalisation of the relay, it is a second subsystem.** Nothing
  in `pumpReported` carries over — KV semantics are last-value and
  compare-before-write; a stream is an append log where "the destination already
  agrees" is meaningless.
- **Dedupe does not survive the case it exists for.** `duplicate_window`
  defaults to two minutes. It covers a lost `PubAck` retried seconds later. A
  six-hour outage ends outside the window, so the boundary produces duplicates
  anyway, and the window cannot be widened to cover it (the server holds a
  message-id map per stream). At-least-once with best-effort dedupe, sold
  honestly, is a weaker guarantee than it first appears.
- **Silent double delivery.** A device publishing to a subject the hub has
  interest in *already* reaches the hub natively, because interest propagates
  down the leaf link — that is how this agent's telemetry works today. A spool
  capturing the same subject delivers everything twice, and dedupe cannot help
  because the native copy carries no message id. Avoiding it requires a subject
  space the hub does not bind, which means changing what devices publish.
- **Correct operation needs half a dozen non-obvious decisions**, each of which
  is a silent data-loss bug when wrong: acking locally only after the hub's
  `PubAck`; deriving the message id from `<domain>-<stream>-<seq>` rather than
  generating one; not pulling at all while the uplink is down, or redelivery
  burns `MaxDeliver` on the entire backlog and terminates it; separating "could
  not reach the hub" from "the hub said no"; choosing `Discard: Old` versus
  `New`; choosing order versus throughput.
- **The server may already do it better.** For streams, unlike KV, the name is
  ours to choose, so `spool_<code>` sidesteps the `iname` problem entirely and a
  hub-side source does the job with no agent code and a stronger guarantee — a
  source tracks sequence, so it needs no dedupe window and produces no
  duplicates at an outage boundary. The config then lives on the receiving side
  and in the Control Plane, which already knows the fleet.

If durable store-and-forward is ever genuinely needed, the first question is
whether a hub-side source answers it. Building the pump requires a reason the
source cannot serve: egress control decided at the site, a hub that must not be
able to reach edges, or transformation before data crosses the WAN.

### Also cut from the first draft

- **Mirroring arbitrary streams** (`mirrors.streams`). KV only. If a stream
  mirror is ever wanted it is a separate list with separate validation, added
  when someone asks.
- **Per-entry `manage:` flags and retention fields.** Replaced by the ownership
  rule above, which needs no configuration.
- **A repair path for a bucket that disagrees with its declaration.** It warns
  and skips. Deleting and recreating an operator's bucket is not something an
  agent should do from a config file.

## Staging

1. ~~**Fix `HubDomain`.**~~ Cached in the platform session file and read by
   `platform.Client.HubDomain()`, which fetches a leaf config only when the cache
   is empty. `nats-leaf.conf` was never an option: it does not carry the hub's
   domain at all, only the leaf's own.
2. ~~**Add the mirror filter.**~~ `SyncBucket.Keys`, as a key pattern in both
   directions. One-way door, so it shipped before anything was deployed.
3. ~~**Two lists, the preset expansion, and the both-lists check.**~~
   `sync.mirrors` / `sync.relays`, with `sync.twin: true` expanding to one entry
   in each direction and `config.validateSyncConfig` refusing a bucket that
   appears in both.
4. ~~**The two metrics and the check.**~~ `agent_edge_sync_up{bucket,direction}`,
   `agent_edge_relay_pending{bucket}`, and one `sync` readiness check that warns.

Two things were added during implementation that this document had not called
for, both for the same reason — a silent no-op is worse than a refusal:

- **`twin.enabled` is rejected by name.** It never worked, so there is nothing to
  be compatible with, but viper ignores keys it does not know and a config file
  still carrying it would have synced nothing and said nothing.
- **Sync without platform auth is refused at load.** The hub's domain is
  reachable no other way, so the alternative was an agent that starts, declares
  buckets, and disables all of them — which is exactly the failure this document
  opens with.

## Open questions

- Does anyone actually have a second bucket pair to sync? The lists exist now,
  but the preset is still the only thing using them. If nothing else ever goes in
  them, that is a sign the generalisation was one step further than needed — not
  a disaster, but worth noticing rather than backfilling reasons for.
- The mirror filter narrows what a site *receives*. Nothing stops a site
  declaring a wider filter than it should have; the hub could enforce scoping in
  the credential instead, which is nearly free at issue time. Platform-side
  question, unanswered.
