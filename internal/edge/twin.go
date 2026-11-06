package edge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// The digital twin ("Live State") is two KV buckets per organization, split by
// who owns the data rather than by what it describes:
//
//	twin          reported state. The device writes it; it flows edge -> hub.
//	twin_desired  desired state.  The operator writes it; it flows hub -> edge.
//
// It is now one preset over the general machinery in sync.go — `sync.twin: true`
// expands to one mirror and one relay — but the reasoning below is what chose
// those two mechanisms, and it applies to every entry in either list.
//
// One writer per bucket, one direction per bucket. That is the whole safety
// property. It used to be structural: there were two buckets and no way to say
// anything else. With a list it is config.validateSyncConfig instead, refusing
// to start when a bucket appears in both directions. The alternative — one
// bucket written from both ends — does not merely pick a loser on a conflict, it
// oscillates: two concurrent writes to the same key swap across the link, then
// swap back, each write generating the next event. Encoding the owner in the key
// instead (`thing.S01.state.temp`) buys the same property but pays for it in
// every key in the system, and leaves malformed keys silently unsynced. Two
// buckets makes the conflict unrepresentable and costs one noun.
//
// Splitting them also lets each direction use the mechanism that actually fits:
//
//	twin_desired  ONE origin (the hub), N mirrors. A JetStream mirror does this
//	              natively, so the edge copy is maintained by the server with no
//	              code here at all. The edge never writes it, so a mirror's
//	              write-forwarding — which would fail during a WAN outage — never
//	              comes into play; reads are served locally from the last-known
//	              values, which is exactly right when the link is down.
//
//	twin          N origins (every site), aggregated at the hub. A native
//	              source CAN aggregate same-named buckets -- the server tells
//	              `KV_twin` from S01 and `KV_twin` from S02 apart by API prefix --
//	              but the sources live in ONE list on the hub's stream, and that
//	              list has no safe writer. A stream update replaces the whole
//	              list, with no per-source add or remove and no expected-revision
//	              check, and the server cancels every source the new list omits.
//	              Two sites adding themselves at once therefore remove each
//	              other, and nothing owns removing a decommissioned site. The
//	              Control Plane cannot hold the list either: it has no credential
//	              in the org's account. The relay below needs no shared config:
//	              each site owns its own upstream path.
//
// That last point is also why no entry in either list may be renamed across the
// link: a bucket has the same name at both ends, or the reason the relay exists
// in the first place evaporates.
const (
	twinBucket        = "twin"
	twinDesiredBucket = "twin_desired"
)

// bucketConfig is the ONE definition of a synced bucket's retention, for the
// twin preset and for any user-declared bucket the agent has to create locally.
//
// Keep in step with TWIN_BUCKET_CONFIG in ui/src/utils/twin.ts — the console
// creates the twin buckets too, and whoever gets there first defines them. One
// shape for every entry rather than a per-entry config block: nobody has asked
// for a second shape, and a retention knob per bucket is a knob that can
// disagree with the console's.
func bucketConfig(name, description string) jetstream.KeyValueConfig {
	return jetstream.KeyValueConfig{
		Bucket:      name,
		Description: description,
		History:     10,
		Storage:     jetstream.FileStorage,
	}
}

// twinSide is the slice of jetstream.KeyValue the relay needs from each end.
// Narrow so both ends can be driven by a fake in tests; jetstream.KeyValue
// satisfies it as-is.
type twinSide interface {
	Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error)
	Put(ctx context.Context, key string, value []byte) (uint64, error)
	Delete(ctx context.Context, key string, opts ...jetstream.KVDeleteOpt) error
	Watch(ctx context.Context, keys string, opts ...jetstream.WatchOpt) (jetstream.KeyWatcher, error)
}

// hubSide is the far end of a relay: everything relayEntry needs, plus the one
// write jetstream.KeyValue does not offer, an asynchronous one.
type hubSide interface {
	twinSide
	publish(key string, value []byte, deleted bool) (jetstream.PubAckFuture, error)
}

// hubKV is the hub's bucket with an asynchronous write added.
//
// A KV put is a JetStream publish to `$KV.<bucket>.<key>`, and a delete is the
// same publish with a `KV-Operation: DEL` header -- that is all nats.go's own Put
// and Delete do. Across a JetStream domain the subject carries the domain's API
// prefix, `$JS.<domain>.API.`, which the hub maps back to `$KV.`. This builds the
// subject the same way nats.go does for a KV bucket it reaches through a domain
// (kv.go, putPre), and TestHubKVPublishLandsInTheHubBucket proves it against real
// servers.
type hubKV struct {
	jetstream.KeyValue
	js  jetstream.JetStream
	pre string
}

func newHubKV(kv jetstream.KeyValue, js jetstream.JetStream, hubDomain string) *hubKV {
	return &hubKV{KeyValue: kv, js: js, pre: fmt.Sprintf("$JS.%s.API.$KV.%s.", hubDomain, kv.Bucket())}
}

func (h *hubKV) publish(key string, value []byte, deleted bool) (jetstream.PubAckFuture, error) {
	m := &nats.Msg{Subject: h.pre + key, Data: value}
	if deleted {
		m.Header = nats.Header{}
		m.Header.Set("KV-Operation", "DEL")
	}
	return h.js.PublishMsgAsync(m)
}

// relayEntry copies one observed change to dst, unless dst already agrees.
//
// The equality check is an optimisation, not the safety property: the watcher
// replays every current value on startup and after a reconnect, so without it
// every restart would rewrite the whole bucket and burn a revision per key.
// Safety comes from the source bucket having exactly one writer — the edge — so
// nothing ever writes back and there is no echo to suppress.
//
// Returns whether a write was actually issued.
func relayEntry(ctx context.Context, dst twinSide, key string, val []byte, op jetstream.KeyValueOp) (bool, error) {
	deleted := op == jetstream.KeyValueDelete || op == jetstream.KeyValuePurge

	cur, err := dst.Get(ctx, key)
	switch {
	case err == nil:
		if deleted {
			// A KV delete is a tombstone message, not an absence. Relaying it
			// explicitly is the whole job: treating a DEL as "nothing to send"
			// leaves the key gone at the edge and live at the hub forever,
			// because the equality check below only ever compares values that
			// exist. (Purge is relayed as a delete — the key goes away either
			// way; history rollup is domain-bound and does not travel.)
			if err := dst.Delete(ctx, key); err != nil {
				return false, fmt.Errorf("delete %q: %w", key, err)
			}
			return true, nil
		}
		if bytes.Equal(cur.Value(), val) {
			return false, nil
		}
	case errors.Is(err, jetstream.ErrKeyNotFound):
		if deleted {
			return false, nil // already absent
		}
	default:
		return false, fmt.Errorf("get %q: %w", key, err)
	}

	if _, err := dst.Put(ctx, key, val); err != nil {
		return false, fmt.Errorf("put %q: %w", key, err)
	}
	return true, nil
}

// twinRetryInterval is how often the relay checks its unconfirmed writes and
// re-sends the keys whose write failed. A var, not a const, so a test can shrink
// it -- the retry path is the whole point of the pending set and a 30s wait would
// make it untestable.
var twinRetryInterval = 30 * time.Second

// relayAckTimeout bounds how long one asynchronous write waits for the hub's
// acknowledgement. It has to be set: nats.go gives async publishes no timeout by
// default, so an acknowledgement lost to a WAN drop would leave its key
// unconfirmed for ever and never retried.
const relayAckTimeout = 10 * time.Second

// relay is one upstream entry: watch a local bucket, copy every change to the
// same-named bucket at the hub.
//
// It carries its own key filter and its own reporting hook so that N of these
// can run side by side, each answerable for itself in /ready and /metrics.
type relay struct {
	src twinSide
	dst hubSide

	// keys narrows the watcher. Empty means every key, which is what the twin
	// preset uses; a filter is how a site is made physically unable to relay
	// another site's keyspace up, rather than merely conventionally unlikely to.
	keys string

	// report publishes the current backlog for the collector. May be nil.
	report func(pending int)

	// Owned by the pump's goroutine; nothing else touches them, so no lock.
	//
	// pending holds keys whose last write failed. inflight holds the latest
	// unconfirmed write per key. A key is in one or the other, never both: a new
	// write takes it out of pending, and a failed one moves it back.
	pending  map[string]struct{}
	inflight map[string]jetstream.PubAckFuture
}

func (r *relay) watchKeys() string {
	if r.keys == "" {
		return jetstream.AllKeys
	}
	return r.keys
}

// backlog is every key the hub has not yet confirmed.
func (r *relay) backlog() int { return len(r.pending) + len(r.inflight) }

func (r *relay) reportBacklog() {
	if r.report != nil {
		r.report(r.backlog())
	}
}

// pump watches the local bucket and copies every change to the hub's. Returns
// when ctx is cancelled (nil) or the watcher fails (error, for supervise to back
// off and restart).
//
// Two phases, because they want different things:
//
//   - The REPLAY -- every current value, which the watcher sends first, up to a
//     nil marker -- goes through relayEntry, one key at a time, comparing before
//     it writes. A restart replays the whole bucket, and without the compare it
//     would rewrite every key at the hub, burning a revision each and waking every
//     hub-side watcher for nothing. It is slow over a WAN (a round trip per key),
//     but it runs once per start.
//
//   - LIVE changes are published asynchronously with no compare. Waiting for a
//     read and then an acknowledgement on every change held a relay to one change
//     per two round trips -- 5 a second over a 100 ms link. Publishing without
//     waiting lets them overlap: the same link carried hundreds a second.
//
// Order still holds. Every write goes out on one connection, which the server
// stores in the order sent, so the newest value for a key is the one that lands
// last.
//
// Failures are held and retried, which is not a refinement but the only thing
// making this direction reliable at all. The watcher is on the LOCAL bucket,
// which does not die when the hub or the WAN does, so nothing else would ever
// re-offer a value that failed and then never changed again. Retrying beats
// returning an error and letting the supervisor replay: a single key the hub
// will never accept would otherwise tear down the watcher on every replay and
// block every other key behind it, forever.
func (r *relay) pump(ctx context.Context) error {
	w, err := r.src.Watch(ctx, r.watchKeys())
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	defer func() { _ = w.Stop() }()

	// Bounded by the size of the bucket rather than by the length of an outage:
	// one entry per key, whatever happened to it in between.
	r.pending = make(map[string]struct{})
	r.inflight = make(map[string]jetstream.PubAckFuture)
	retry := time.NewTicker(twinRetryInterval)
	defer retry.Stop()

	r.reportBacklog()

	live := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-retry.C:
			r.retry(ctx)
		case e, ok := <-w.Updates():
			if !ok {
				return errors.New("watcher closed")
			}
			if e == nil { // the end of the replay
				live = true
				continue
			}
			deleted := e.Operation() == jetstream.KeyValueDelete || e.Operation() == jetstream.KeyValuePurge
			if live {
				r.send(e.Key(), e.Value(), deleted)
				continue
			}
			if _, err := relayEntry(ctx, r.dst, e.Key(), e.Value(), e.Operation()); err != nil {
				// Fail-soft, like the rest of this agent: log, keep the stream
				// moving, and come back to this key on the retry tick.
				log.Printf("⚠️ edge: relay: %v (will retry)", err)
				r.pending[e.Key()] = struct{}{}
				r.reportBacklog()
			}
		}
	}
}

// send writes one key to the hub without waiting for the answer.
//
// It always sends what it is given and never reads the hub first. A delete is
// sent even if the hub never had the key; that leaves a tombstone, which is
// harmless.
func (r *relay) send(key string, value []byte, deleted bool) {
	f, err := r.dst.publish(key, value, deleted)
	if err != nil {
		// Refused before it left: nats.go's limit on unacknowledged writes is
		// full, or the connection is closed. Retry it like any other failure.
		delete(r.inflight, key)
		r.pending[key] = struct{}{}
		r.reportBacklog()
		return
	}
	// This write supersedes whatever failed before it, and whatever was still in
	// flight: only the latest write for a key decides what the hub ends up with.
	delete(r.pending, key)
	r.inflight[key] = f
}

// retry runs on the tick. It collects the answers to the writes in flight, then
// re-sends every key whose write failed.
//
// The value is re-read from the local bucket rather than remembered from the
// failed attempt, so a retry can never write a stale value over a newer one --
// the device may have reported twice more while the hub was unreachable, and
// only the current value is worth sending. A key deleted locally in the
// meantime is sent as a delete.
func (r *relay) retry(ctx context.Context) {
	r.collect()

	resent := 0
	for key := range r.pending {
		if ctx.Err() != nil {
			return
		}
		cur, err := r.src.Get(ctx, key)
		switch {
		case errors.Is(err, jetstream.ErrKeyNotFound):
			r.send(key, nil, true)
		case err != nil:
			continue // cannot read locally right now; try again next tick
		default:
			r.send(key, cur.Value(), false)
		}
		resent++
	}
	r.reportBacklog()

	if resent > 0 {
		log.Printf("⚠️ edge: relay re-sent %d key(s) the hub had not accepted", resent)
	}
}

// collect takes the answer to every write that has one. A confirmed write is
// done with; a failed one goes back to pending for the next retry. A write still
// waiting is left alone, and relayAckTimeout guarantees it gets an answer.
func (r *relay) collect() {
	failed := 0
	var firstErr error
	for key, f := range r.inflight {
		select {
		case <-f.Ok():
			delete(r.inflight, key)
		case err := <-f.Err():
			delete(r.inflight, key)
			r.pending[key] = struct{}{}
			if failed == 0 {
				firstErr = err
			}
			failed++
		default:
		}
	}
	if failed > 0 {
		log.Printf("⚠️ edge: relay: the hub did not accept %d write(s), first error: %v (will retry)", failed, firstErr)
	}
}

// supervise runs the pump, restarting it with backoff if the watcher dies (a
// JetStream hiccup, a WAN drop). nats.go reconnects the connection underneath,
// but a failed watcher stays dead unless something restarts it.
func (r *relay) supervise(ctx context.Context) {
	const (
		minBackoff = 1 * time.Second
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff

	for ctx.Err() == nil {
		err := r.pump(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			backoff = minBackoff
			continue
		}
		log.Printf("⚠️ edge: relay stopped (%v); retrying in %s", err, backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
