package edge

import (
	"context"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/stone-age-io/agent/internal/health"
)

// state is what the checks and the collector read. It is only the live
// connection now: the config mirror that used to feed cycle counts, per
// collection record counts and error lists into here went with the mirror.
//
// Its ancestor carried all of that and was called syncState. Two checks went
// with it — sync_freshness and sync_errors — rather than being kept and made to
// report on nothing. A check that cannot fail is worse than no check: it shows
// green beside a real problem and teaches people the dashboard is decorative.
type state struct {
	mu   sync.Mutex
	conn *nats.Conn
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

// registerChecks adds the two questions an edge box can answer first-hand.
//
// Both are answerable here and nowhere else, which is the whole reason these
// endpoints exist. The Control Plane holds the operator and $SYS and has no
// credential inside any organization's account, so it cannot see whether this
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
}
