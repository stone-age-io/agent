package edge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"time"

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
//	twin          N origins (every site), aggregated at the hub. Native sourcing
//	              cannot do this cleanly: every site's bucket would be the stream
//	              `KV_twin`, and aggregating same-named sources needs the server's
//	              internal `iname`, which nats.go does not expose. The documented
//	              guidance is unique stream names for centrally-referenced
//	              streams — which would mean `twin_<code>` at each edge, so
//	              rule-router would read a different bucket name at every site.
//	              Not worth it. The relay below carries this one direction.
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

// twinRetryInterval is how often the relay re-offers keys whose write to the hub
// failed. A var, not a const, so a test can shrink it -- the retry path is the
// whole point of the pending set and a 30s wait would make it untestable.
var twinRetryInterval = 30 * time.Second

// relay is one upstream entry: watch a local bucket, copy every change to the
// same-named bucket at the hub.
//
// It carries its own key filter and its own reporting hook so that N of these
// can run side by side, each answerable for itself in /ready and /metrics. The
// mechanics below are unchanged from when there was exactly one.
type relay struct {
	src, dst twinSide

	// keys narrows the watcher. Empty means every key, which is what the twin
	// preset uses; a filter is how a site is made physically unable to relay
	// another site's keyspace up, rather than merely conventionally unlikely to.
	keys string

	// report publishes the current backlog for the collector. May be nil.
	report func(pending int)
}

func (r *relay) watchKeys() string {
	if r.keys == "" {
		return jetstream.AllKeys
	}
	return r.keys
}

func (r *relay) setPending(n int) {
	if r.report != nil {
		r.report(n)
	}
}

// pump watches the local bucket and copies every change to the hub's. Returns
// when ctx is cancelled (nil) or the watcher fails (error, for supervise to back
// off and restart).
//
// Keys whose relay fails are held and retried, which is not a refinement but
// the only thing making this direction reliable at all. The previous version
// logged a failed write and dropped it, on the stated grounds that "a key
// missed here is re-offered by the next watcher restart's replay" -- and it was
// not. This watcher is on the LOCAL bucket, which does not die when the hub or
// the WAN does, and supervise only restarts the pump when the WATCHER fails. So
// a value that changed during an outage, failed its hub write, and then never
// changed again was absent from the hub permanently and silently, in the one
// direction the platform takes responsibility for delivering.
//
// Retrying beats returning an error and letting the supervisor replay: a single
// key the hub will never accept would otherwise tear down the watcher on every
// replay and block every other key behind it, forever.
func (r *relay) pump(ctx context.Context) error {
	w, err := r.src.Watch(ctx, r.watchKeys())
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	defer func() { _ = w.Stop() }()

	// One entry per key currently failing, so this is bounded by the size of the
	// bucket rather than by the length of the outage.
	pending := make(map[string]struct{})
	retry := time.NewTicker(twinRetryInterval)
	defer retry.Stop()

	r.setPending(0)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-retry.C:
			r.retryPending(ctx, pending)
		case e, ok := <-w.Updates():
			if !ok {
				return errors.New("watcher closed")
			}
			// The watcher sends a nil entry to mark the end of the initial
			// replay. That replay covers everything present when the pump
			// starts; the pending set above covers everything that fails
			// afterwards.
			if e == nil {
				continue
			}
			if _, err := relayEntry(ctx, r.dst, e.Key(), e.Value(), e.Operation()); err != nil {
				// Fail-soft, like the rest of this agent: log, keep the stream
				// moving, and come back to this key on the retry tick.
				log.Printf("⚠️ edge: relay: %v (will retry)", err)
				pending[e.Key()] = struct{}{}
				r.setPending(len(pending))
				continue
			}
			delete(pending, e.Key())
			r.setPending(len(pending))
		}
	}
}

// retryPending re-offers each key whose relay failed earlier.
//
// The value is re-read from the local bucket rather than remembered from the
// failed attempt, so a retry can never write a stale value over a newer one --
// the device may have reported twice more while the hub was unreachable, and
// only the current value is worth sending. A key deleted locally in the
// meantime is relayed as a delete, which is the same tombstone-not-absence
// distinction relayEntry already makes.
func (r *relay) retryPending(ctx context.Context, pending map[string]struct{}) {
	if len(pending) == 0 {
		return
	}

	before := len(pending)
	for key := range pending {
		if ctx.Err() != nil {
			return
		}

		cur, err := r.src.Get(ctx, key)
		switch {
		case errors.Is(err, jetstream.ErrKeyNotFound):
			if _, err := relayEntry(ctx, r.dst, key, nil, jetstream.KeyValueDelete); err != nil {
				continue // hub still unhappy; leave it pending
			}
		case err != nil:
			continue // cannot read locally right now; try again next tick
		default:
			if _, err := relayEntry(ctx, r.dst, key, cur.Value(), cur.Operation()); err != nil {
				continue
			}
		}
		delete(pending, key)
	}
	r.setPending(len(pending))

	if recovered := before - len(pending); recovered > 0 {
		log.Printf("edge: relay caught up on %d key(s)", recovered)
	}
	if len(pending) > 0 {
		log.Printf("⚠️ edge: relay still behind on %d key(s)", len(pending))
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
