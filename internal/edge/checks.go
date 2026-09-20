package edge

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/stone-age-io/agent/internal/health"
)

// state is what the checks and the collector read: the live connection, and one
// row per declared sync bucket.
//
// Its ancestor carried the config mirror's cycle counts, per-collection record
// counts and error lists, and was called syncState. Two checks went with it when
// the mirror was removed — sync_freshness and sync_errors — rather than being
// kept and made to report on nothing. A check that cannot fail is worse than no
// check: it shows green beside a real problem and teaches people the dashboard
// is decorative. The rows below are that rule applied in the other direction:
// they report on something real, so they are worth carrying.
type state struct {
	mu   sync.Mutex
	conn *nats.Conn
	sync []*syncEntry
}

// syncEntry is one declared bucket's live status, for the check and the
// collector. One per entry, registered before anything can fail, so a bucket
// that never came up reports DOWN rather than going missing — an entry absent
// from /metrics looks exactly like an agent that was never asked to sync it.
type syncEntry struct {
	bucket    string
	direction string
	up        bool
	detail    string

	// pending is the relay's backlog, and hasPending says whether it is known at
	// all. Mirrors never set it, and a relay that has not started has no backlog
	// to report rather than a backlog of zero — the same "omit, never zero" rule
	// the collector applies to the server-derived series.
	pending    int
	hasPending bool

	// loggedDetail is the last failure written to the log. Wiring is retried on
	// a timer now, and a bucket whose hub side does not exist fails identically
	// every time — without this the retry would write the same line for ever and
	// bury whatever else the box was saying.
	loggedDetail string
}

func (s *state) setConn(nc *nats.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = nc
}

func (s *state) connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil && s.conn.IsConnected()
}

func (s *state) registerSync(bucket, direction string) *syncEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := &syncEntry{bucket: bucket, direction: direction}
	s.sync = append(s.sync, e)
	return e
}

func (s *state) setSyncUp(e *syncEntry, up bool, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.up, e.detail = up, detail
	if up {
		// Forget what was logged while it was down, so a bucket that breaks
		// again the same way still says so once.
		e.loggedDetail = ""
	}
}

// syncIsUp reports whether an entry is currently wired up, so the retry loop
// leaves working entries alone. Re-running ensureMirror on a healthy mirror
// would be harmless; restarting a healthy relay would start a second watcher
// on the same bucket.
func (s *state) syncIsUp(e *syncEntry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return e.up
}

// shouldLogDown reports whether this failure is worth a log line, and records
// it as logged. New failures and changed reasons are printed; a repeat of the
// same reason on the next retry tick is not.
func (s *state) shouldLogDown(e *syncEntry, detail string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.loggedDetail == detail {
		return false
	}
	e.loggedDetail = detail
	return true
}

func (s *state) setSyncPending(e *syncEntry, pending int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.pending, e.hasPending = pending, true
}

// syncSnapshot returns copies, so readers never hold the lock while formatting.
func (s *state) syncSnapshot() []syncEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]syncEntry, 0, len(s.sync))
	for _, e := range s.sync {
		out = append(out, *e)
	}
	return out
}

// registerChecks adds the questions an edge box can answer first-hand.
//
// All of them are answerable here and nowhere else, which is the whole reason
// these endpoints exist. The Control Plane holds the operator and $SYS and has
// no credential inside any organization's account, so it cannot see whether this
// site's local bus is up. It can count gateways configured; it cannot tell you
// one is down.
func registerChecks(reg *health.Registry, st *state) {
	reg.Register("nats_local", func(ctx context.Context) health.Result {
		if !st.connected() {
			return health.Fail(
				"not connected to the local NATS leaf",
				"Check that the leaf node is running and that nats.urls and the creds file are correct. "+
					"With nats.server_config set the server is in this process, so this failing means it did not start.",
			)
		}
		return health.OK("connected")
	})

	// The uplink to the hub, read from the leaf server's own monitoring port.
	// This is the check that distinguishes "this site is islanded" from "this
	// site is fine" — and it is only answerable here, on the box.
	reg.Register("hub_uplink", func(ctx context.Context) health.Result {
		lz, err := fetchLeafz(ctx, monitorURL)
		if err != nil {
			return health.Skip("local NATS monitoring endpoint not reachable: " + err.Error())
		}
		for _, l := range lz.Leafs {
			// IsSpoke means this server dialled out — the `remotes` entry in
			// nats-leaf.conf. Leaf connections INTO this server (a device
			// bridging in) are not the uplink.
			if l.IsSpoke {
				return health.OK(fmt.Sprintf("attached to %s (rtt %s)", l.Name, l.RTT))
			}
		}
		// Warn, never fail. An islanded edge still serves its devices, and
		// autonomy under a WAN outage is the entire reason a leaf node exists —
		// reporting 503 would invert the design and pull a working site out of
		// whatever is watching it.
		return health.Warn(
			"no outbound leaf connection to the hub",
			"This site is islanded: local NATS still works and devices keep running, but nothing reaches "+
				"the platform. Check the WAN link, and nats.leaf_url on the Control Plane.",
		)
	})

	// One check for every declared bucket. With one hardcoded pair, log lines
	// were enough; with a list an operator declared, a single silently skipped
	// entry looks exactly like a healthy agent.
	//
	// Warn, never fail, for the same reason as hub_uplink: a site whose sync is
	// down is still serving its devices, and that autonomy is the point.
	reg.Register("sync", func(ctx context.Context) health.Result {
		entries := st.syncSnapshot()
		if len(entries) == 0 {
			return health.Skip("no buckets declared")
		}

		var down []string
		for _, e := range entries {
			if !e.up {
				down = append(down, fmt.Sprintf("%s %s (%s)", e.direction, e.bucket, e.detail))
			}
		}
		if len(down) == 0 {
			return health.OK(fmt.Sprintf("%d bucket(s) syncing", len(entries)))
		}

		return health.Warn(
			fmt.Sprintf("%d of %d bucket(s) not syncing: %s", len(down), len(entries), strings.Join(down, "; ")),
			"A mirror is created once and never repaired, so a bucket that already exists in the wrong "+
				"shape has to be deleted to be recreated. A hub-side bucket must be created at the hub "+
				"first; the agent does not create shared hub buckets from a site's config.",
		)
	})
}
