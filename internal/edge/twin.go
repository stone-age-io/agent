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
// One writer per bucket, one direction per bucket. That is the whole safety
// property, and it is structural rather than a discipline anyone has to
// remember. The alternative — one bucket written from both ends — does not
// merely pick a loser on a conflict, it oscillates: two concurrent writes to the
// same key swap across the link, then swap back, each write generating the next
// event. Encoding the owner in the key instead (`thing.S01.state.temp`) buys the
// same property but pays for it in every key in the system, and leaves malformed
// keys silently unsynced. Two buckets makes the conflict unrepresentable and
// costs one noun.
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
const (
	twinBucket        = "twin"
	twinDesiredBucket = "twin_desired"
)

// twinBucketConfig is the ONE definition of a twin bucket's retention. Keep in
// step with TWIN_BUCKET_CONFIG in ui/src/utils/twin.ts — the console creates
// these buckets too, and whoever gets there first defines them.
func twinBucketConfig(name, description string) jetstream.KeyValueConfig {
	return jetstream.KeyValueConfig{
		Bucket:      name,
		Description: description,
		History:     10,
		Storage:     jetstream.FileStorage,
	}
}

func reportedBucketConfig() jetstream.KeyValueConfig {
	return twinBucketConfig(twinBucket, "Digital twin: reported state (written at the edge)")
}

func desiredBucketConfig() jetstream.KeyValueConfig {
	return twinBucketConfig(twinDesiredBucket, "Digital twin: desired state (written by operators)")
}

// twinSide is the slice of jetstream.KeyValue the relay needs from each end.
// Narrow so both ends can be driven by a fake in tests; jetstream.KeyValue
// satisfies it as-is.
type twinSide interface {
	Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error)
	Put(ctx context.Context, key string, value []byte) (uint64, error)
	Delete(ctx context.Context, key string, opts ...jetstream.KVDeleteOpt) error
	WatchAll(ctx context.Context, opts ...jetstream.WatchOpt) (jetstream.KeyWatcher, error)
}

// relayEntry copies one observed change to dst, unless dst already agrees.
//
// The equality check is an optimisation, not the safety property: WatchAll
// replays every current value on startup and after a reconnect, so without it
// every restart would rewrite the whole bucket and burn a revision per key.
// Safety comes from `twin` having exactly one writer — the edge — so nothing
// ever writes back and there is no echo to suppress.
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

// twinRetryInterval is how often pumpReported re-offers keys whose relay to the
// hub failed. A var, not a const, so a test can shrink it -- the retry path is
// the whole point of the pending set and a 30s wait would make it untestable.
var twinRetryInterval = 30 * time.Second

// pumpReported watches the edge's `twin` bucket and copies every change to the
// hub's. Returns when ctx is cancelled (nil) or the watcher fails (error, for
// the supervisor to back off and restart).
//
// Keys whose relay fails are held and retried, which is not a refinement but
// the only thing making this direction reliable at all. The previous version
// logged a failed write and dropped it, on the stated grounds that "a key
// missed here is re-offered by the next watcher restart's replay" -- and it was
// not. This watcher is on the LOCAL bucket, which does not die when the hub or
// the WAN does, and superviseReportedPump only restarts the pump when the
// WATCHER fails. So a value that changed during an outage, failed its hub
// write, and then never changed again was absent from the hub permanently and
// silently, in the one direction the platform takes responsibility for
// delivering.
//
// Retrying beats returning an error and letting the supervisor replay: a single
// key the hub will never accept would otherwise tear down the watcher on every
// replay and block every other key behind it, forever.
func pumpReported(ctx context.Context, src, dst twinSide) error {
	w, err := src.WatchAll(ctx)
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	defer func() { _ = w.Stop() }()

	// One entry per key currently failing, so this is bounded by the size of the
	// bucket rather than by the length of the outage.
	pending := make(map[string]struct{})
	retry := time.NewTicker(twinRetryInterval)
	defer retry.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-retry.C:
			retryPending(ctx, src, dst, pending)
		case e, ok := <-w.Updates():
			if !ok {
				return errors.New("watcher closed")
			}
			// WatchAll sends a nil entry to mark the end of the initial replay.
			// That replay covers everything present when the pump starts; the
			// pending set above covers everything that fails afterwards.
			if e == nil {
				continue
			}
			if _, err := relayEntry(ctx, dst, e.Key(), e.Value(), e.Operation()); err != nil {
				// Fail-soft, like the rest of this agent: log, keep the stream
				// moving, and come back to this key on the retry tick.
				log.Printf("⚠️ edge: twin relay: %v (will retry)", err)
				pending[e.Key()] = struct{}{}
				continue
			}
			delete(pending, e.Key())
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
func retryPending(ctx context.Context, src, dst twinSide, pending map[string]struct{}) {
	if len(pending) == 0 {
		return
	}

	before := len(pending)
	for key := range pending {
		if ctx.Err() != nil {
			return
		}

		cur, err := src.Get(ctx, key)
		switch {
		case errors.Is(err, jetstream.ErrKeyNotFound):
			if _, err := relayEntry(ctx, dst, key, nil, jetstream.KeyValueDelete); err != nil {
				continue // hub still unhappy; leave it pending
			}
		case err != nil:
			continue // cannot read locally right now; try again next tick
		default:
			if _, err := relayEntry(ctx, dst, key, cur.Value(), cur.Operation()); err != nil {
				continue
			}
		}
		delete(pending, key)
	}

	if recovered := before - len(pending); recovered > 0 {
		log.Printf("edge: twin relay caught up on %d key(s)", recovered)
	}
	if len(pending) > 0 {
		log.Printf("⚠️ edge: twin relay still behind on %d key(s)", len(pending))
	}
}

// superviseReportedPump runs the pump, restarting it with backoff if the watcher
// dies (a JetStream hiccup, a WAN drop). nats.go reconnects the connection
// underneath, but a failed watcher stays dead unless something restarts it.
func superviseReportedPump(ctx context.Context, src, dst twinSide) {
	const (
		minBackoff = 1 * time.Second
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff

	for ctx.Err() == nil {
		err := pumpReported(ctx, src, dst)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			backoff = minBackoff
			continue
		}
		log.Printf("⚠️ edge: twin relay stopped (%v); retrying in %s", err, backoff)

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

// ensureDesiredMirror creates the edge's `twin_desired` as a mirror of the hub's,
// if it is absent. A mirror is configured entirely on the receiving side, so
// there is no hub-side stream to mutate and no race between sites.
//
// If the bucket already exists but is not a mirror, it is left alone and the
// mismatch is logged loudly: silently serving a bucket that looks like desired
// state but never receives any is worse than saying so.
func ensureDesiredMirror(ctx context.Context, localJS jetstream.JetStream, hubDomain string) error {
	existing, err := localJS.KeyValue(ctx, twinDesiredBucket)
	if err == nil {
		s, serr := localJS.Stream(ctx, "KV_"+twinDesiredBucket)
		if serr == nil && s.CachedInfo().Config.Mirror == nil {
			log.Printf("⚠️ edge: local %q exists but is not a mirror of the hub's; "+
				"desired state will NOT reach this edge. Delete it to have the agent recreate it.",
				twinDesiredBucket)
		}
		_ = existing
		return nil
	}
	if !errors.Is(err, jetstream.ErrBucketNotFound) {
		return err
	}

	cfg := desiredBucketConfig()
	cfg.Mirror = &jetstream.StreamSource{
		Name:   "KV_" + twinDesiredBucket,
		Domain: hubDomain,
	}
	if _, err := localJS.CreateKeyValue(ctx, cfg); err != nil {
		return err
	}
	log.Printf("edge: %q mirrored from hub domain %q", twinDesiredBucket, hubDomain)
	return nil
}

// startTwin wires up both twin buckets for this leaf and starts the reported
// relay. Best-effort in the same spirit as the heartbeat: it logs why and
// returns without starting anything rather than failing the agent, since config
// sync must keep working even when the data plane cannot.
func startTwin(ctx context.Context, nc *nats.Conn, cfg *Config) {
	if !cfg.TwinEnabled {
		return
	}
	if cfg.HubDomain == "" {
		log.Printf("⚠️ edge: twin sync enabled but nats.hub_domain is unset; disabled")
		return
	}

	localJS, err := jetstream.New(nc)
	if err != nil {
		log.Printf("⚠️ edge: twin sync disabled (local JetStream): %v", err)
		return
	}
	hubJS, err := jetstream.NewWithDomain(nc, cfg.HubDomain)
	if err != nil {
		log.Printf("⚠️ edge: twin sync disabled (JetStream on domain %q): %v", cfg.HubDomain, err)
		return
	}

	// A leaf sharing the hub's domain has nothing to sync: one set of buckets
	// serves both. Mirroring a bucket onto itself would be a configuration loop,
	// so bail out early and say so rather than producing something broken.
	if info, err := localJS.AccountInfo(ctx); err == nil && info.Domain == cfg.HubDomain {
		log.Printf("edge: local JetStream domain is the hub's (%q); twin sync not needed", cfg.HubDomain)
		return
	}

	// The hub side is the source of truth for both buckets and must exist before
	// anything mirrors or relays into it.
	hubReported, err := openOrCreateKV(ctx, hubJS, reportedBucketConfig())
	if err != nil {
		log.Printf("⚠️ edge: twin sync disabled (hub %q bucket): %v", twinBucket, err)
		return
	}
	if _, err := openOrCreateKV(ctx, hubJS, desiredBucketConfig()); err != nil {
		log.Printf("⚠️ edge: twin sync disabled (hub %q bucket): %v", twinDesiredBucket, err)
		return
	}

	// Desired state: server-maintained mirror, no code in the data path.
	if err := ensureDesiredMirror(ctx, localJS, cfg.HubDomain); err != nil {
		// Not fatal: reported state can still flow up without it.
		log.Printf("⚠️ edge: %q mirror unavailable (desired state will not reach this edge): %v",
			twinDesiredBucket, err)
	}

	// Reported state: a real writable bucket here, relayed up.
	localReported, err := openOrCreateKV(ctx, localJS, reportedBucketConfig())
	if err != nil {
		log.Printf("⚠️ edge: twin relay disabled (local %q bucket): %v", twinBucket, err)
		return
	}

	log.Printf("edge: twin relay %q edge → hub domain %q", twinBucket, cfg.HubDomain)
	go superviseReportedPump(ctx, localReported, hubReported)
}
