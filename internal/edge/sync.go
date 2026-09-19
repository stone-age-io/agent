package edge

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Edge sync moves KV buckets between this site's JetStream domain and the hub,
// in two directions with two mechanisms. See twin.go for why each direction uses
// the mechanism it does — the reasoning predates the lists and is unchanged by
// them.
//
//	mirror  hub -> edge. Server-maintained; the agent only declares it and then
//	        has no part in the data path. The edge never writes it, so reads keep
//	        serving last-known values while the link is down.
//
//	relay   edge -> hub. The agent watches the local bucket and copies each
//	        change up, retrying while the link is down.
//
// A bucket belongs to exactly one direction. Config refuses to start otherwise
// (config.validateSyncConfig) — with two built-in buckets that was structural,
// and with a list it has to be a check.

// Bucket is one declared sync entry.
//
// There is no field for a different name at the far end, deliberately: a bucket
// having the same name at both ends is the reason the upstream direction is an
// application relay rather than a native JetStream source.
type Bucket struct {
	Name string

	// Keys optionally narrows the entry to a key pattern — `line-a.>`, never
	// `$KV.recipes.line-a.>`. Empty means the whole bucket. Both directions
	// spell it the same way; this package builds the subject a mirror needs.
	Keys string
}

// direction is which way an entry flows, and is a metric label.
const (
	directionMirror = "mirror"
	directionRelay  = "relay"
)

// entry is a declared Bucket plus what only this package knows about it.
type entry struct {
	Bucket
	direction string

	// preset marks the two twin buckets. It is the ONE privilege a preset entry
	// has: it may create its hub-side bucket. A user-declared bucket may not,
	// because a typo in one site's YAML would otherwise materialise a stream at
	// the shared hub with whatever retention that site happened to guess, and
	// the console would then adopt it. A typo that creates a LOCAL bucket is
	// that one site's problem, so local creation is allowed for everything.
	preset bool
}

// plan returns every entry this agent should wire up, preset first.
func plan(cfg *Config) []entry {
	var out []entry
	if cfg.TwinEnabled {
		out = append(out,
			entry{Bucket: Bucket{Name: twinDesiredBucket}, direction: directionMirror, preset: true},
			entry{Bucket: Bucket{Name: twinBucket}, direction: directionRelay, preset: true},
		)
	}
	for _, b := range cfg.Mirrors {
		out = append(out, entry{Bucket: b, direction: directionMirror})
	}
	for _, b := range cfg.Relays {
		out = append(out, entry{Bucket: b, direction: directionRelay})
	}
	return out
}

// kvFilterSubject turns a key pattern into the subject a KV bucket stores it
// under. A key K in bucket B lives at `$KV.B.K`, so this is the whole
// translation — and the reason config takes keys rather than subjects, so an
// operator writes the same thing in both directions.
func kvFilterSubject(bucket, keys string) string {
	return fmt.Sprintf("$KV.%s.%s", bucket, keys)
}

// startSync wires up every declared bucket and starts the relays.
//
// Best-effort throughout, in the same spirit as the heartbeat: a bucket that
// cannot be brought up is logged, recorded as down for /ready and /metrics, and
// skipped. Config sync and the local bus must keep working even when the data
// plane cannot, and an agent that refused to start over a hub bucket would be
// one nobody could ask what went wrong.
func startSync(ctx context.Context, nc *nats.Conn, cfg *Config, st *state) {
	entries := plan(cfg)
	if len(entries) == 0 {
		return
	}

	if cfg.HubDomain == "" {
		// Unreachable through normal startup: config refuses sync without
		// platform auth, and the platform is what supplies this. It stays as a
		// guard because the value arrives over the network and an empty string
		// would otherwise be addressed as a domain.
		failAll(st, entries, "the hub's JetStream domain is unknown; run `agent -leaf-config` to fetch it")
		return
	}

	localJS, err := jetstream.New(nc)
	if err != nil {
		failAll(st, entries, "local JetStream: %v", err)
		return
	}
	hubJS, err := jetstream.NewWithDomain(nc, cfg.HubDomain)
	if err != nil {
		failAll(st, entries, "JetStream on hub domain %q: %v", cfg.HubDomain, err)
		return
	}

	// A leaf sharing the hub's domain has nothing to sync: one set of buckets
	// serves both, and mirroring a bucket onto itself would be a configuration
	// loop. Nothing is registered for it, so the check reports "no buckets
	// declared" rather than a fleet of red rows — this is a deployment where
	// sync is unnecessary, not one where it is broken.
	if info, err := localJS.AccountInfo(ctx); err == nil && info.Domain == cfg.HubDomain {
		log.Printf("edge: local JetStream domain is the hub's (%q); sync not needed", cfg.HubDomain)
		return
	}

	// Registered before anything else can fail, so a bucket that never comes up
	// is visibly down rather than absent. An entry missing from /metrics looks
	// exactly like an agent that was never asked to sync it.
	for _, e := range entries {
		h := st.registerSync(e.Name, e.direction)

		var err error
		switch e.direction {
		case directionMirror:
			err = ensureMirror(ctx, localJS, hubJS, e, cfg.HubDomain)
		case directionRelay:
			err = startRelay(ctx, localJS, hubJS, e, func(pending int) { st.setSyncPending(h, pending) })
		}

		if err != nil {
			markDown(st, h, e, "%v", err)
			continue
		}
		st.setSyncUp(h, true, "")
	}
}

// failAll registers every entry as down with the same reason, for the failures
// that stop any of them from being attempted.
func failAll(st *state, entries []entry, format string, args ...any) {
	for _, e := range entries {
		markDown(st, st.registerSync(e.Name, e.direction), e, format, args...)
	}
}

func markDown(st *state, h *syncEntry, e entry, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	log.Printf("⚠️ edge: sync %s %q disabled: %s", e.direction, e.Name, detail)
	st.setSyncUp(h, false, detail)
}

// hubBucket opens the far end of an entry, and is the ONE place that decides
// whether the agent may create a bucket at the hub.
//
// A preset entry may, because the platform knows those two names and their
// shape. A user-declared one may not: a typo in one site's YAML that creates a
// local bucket is that site's problem, but one that creates a hub bucket is
// everyone's, with whatever retention that site happened to guess, and the
// console then adopts it.
//
// **Both directions must come through here.** The mirror path originally did
// not, and the result was silent in exactly the way this whole feature is meant
// not to be: JetStream cannot validate a cross-domain mirror source when the
// stream is created, so a mirror of a hub bucket that does not exist is accepted,
// reports healthy, and receives nothing for ever.
func hubBucket(ctx context.Context, hubJS jetstream.JetStream, e entry) (jetstream.KeyValue, error) {
	if e.preset {
		kv, err := openOrCreateKV(ctx, hubJS, bucketConfig(e.Name, description(e)))
		if err != nil {
			return nil, fmt.Errorf("hub bucket: %w", err)
		}
		return kv, nil
	}

	kv, err := hubJS.KeyValue(ctx, e.Name)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, errors.New("hub bucket: no such bucket at the hub; create it there first " +
			"(an agent does not create shared hub buckets from a site's config)")
	}
	if err != nil {
		return nil, fmt.Errorf("hub bucket: %w", err)
	}
	return kv, nil
}

// ensureMirror creates the local bucket as a mirror of the hub's, if it is
// absent. A mirror is configured entirely on the receiving side, so there is no
// hub-side stream to mutate and no race between sites.
//
// An existing bucket is left alone and reported, never repaired. These buckets
// are shared with operators and the console, so an agent that recreated one from
// a config file would be destroying data to satisfy a declaration. Both
// mismatches it can detect are reported as DOWN rather than as a warning beside
// a green check, because in both cases the declared configuration is not the one
// in effect.
//
// The hub side is checked FIRST, and on every start rather than only when the
// local bucket is absent: a local mirror that already exists is just as empty as
// a new one when the thing it mirrors is not there.
func ensureMirror(ctx context.Context, localJS, hubJS jetstream.JetStream, e entry, hubDomain string) error {
	if _, err := hubBucket(ctx, hubJS, e); err != nil {
		return err
	}

	want := mirrorSource(e, hubDomain)

	if _, err := localJS.KeyValue(ctx, e.Name); err == nil {
		s, serr := localJS.Stream(ctx, "KV_"+e.Name)
		if serr != nil {
			return fmt.Errorf("bucket exists but its stream could not be read: %w", serr)
		}
		got := s.CachedInfo().Config.Mirror
		switch {
		case got == nil:
			return errors.New("bucket exists locally but is not a mirror of the hub's, " +
				"so nothing will arrive in it; delete it to have the agent recreate it")
		case got.FilterSubject != want.FilterSubject:
			// A mirror block cannot be updated on an existing stream
			// (JSStreamMirrorNotUpdatableErr), which is why the filter has to be
			// right the first time and why this can only be reported.
			return fmt.Errorf("bucket exists as a mirror filtered on %q but %q is declared; "+
				"a mirror cannot be updated in place, so delete the bucket to have the agent recreate it",
				got.FilterSubject, want.FilterSubject)
		}
		return nil
	} else if !errors.Is(err, jetstream.ErrBucketNotFound) {
		return err
	}

	cfg := bucketConfig(e.Name, description(e))
	cfg.Mirror = want
	if _, err := localJS.CreateKeyValue(ctx, cfg); err != nil {
		return err
	}

	if e.Keys == "" {
		log.Printf("edge: %q mirrored from hub domain %q", e.Name, hubDomain)
	} else {
		log.Printf("edge: %q mirrored from hub domain %q, keys %q", e.Name, hubDomain, e.Keys)
	}
	return nil
}

// mirrorSource is the one place a declared entry becomes a JetStream mirror.
//
// FilterSubject rather than SubjectTransforms: this is a filter with no
// transform, the bucket keeps the same name and the same keys at both ends, and
// an identity transform would be a more elaborate way to say the same thing.
func mirrorSource(e entry, hubDomain string) *jetstream.StreamSource {
	src := &jetstream.StreamSource{
		Name:   "KV_" + e.Name,
		Domain: hubDomain,
	}
	if e.Keys != "" {
		src.FilterSubject = kvFilterSubject(e.Name, e.Keys)
	}
	return src
}

// description is what a bucket this agent creates says about itself to anyone
// reading `nats kv ls`. The preset keeps the wording the console uses.
func description(e entry) string {
	switch {
	case e.preset && e.direction == directionMirror:
		return "Digital twin: desired state (written by operators)"
	case e.preset:
		return "Digital twin: reported state (written at the edge)"
	case e.direction == directionMirror:
		return "Mirrored from the hub by the agent"
	default:
		return "Relayed to the hub by the agent"
	}
}

// startRelay opens both ends of an upstream entry and starts its pump.
//
// The hub side must already exist unless this is a preset entry; see entry.preset.
//
// It takes a reporting closure rather than the state, so the relay's only tie to
// readiness is one function of one int.
func startRelay(ctx context.Context, localJS, hubJS jetstream.JetStream, e entry, report func(pending int)) error {
	hub, err := hubBucket(ctx, hubJS, e)
	if err != nil {
		return err
	}

	local, err := openOrCreateKV(ctx, localJS, bucketConfig(e.Name, description(e)))
	if err != nil {
		return fmt.Errorf("local bucket: %w", err)
	}

	r := &relay{src: local, dst: hub, keys: e.Keys, report: report}

	log.Printf("edge: relaying %q edge → hub, keys %q", e.Name, r.watchKeys())
	go r.supervise(ctx)
	return nil
}
