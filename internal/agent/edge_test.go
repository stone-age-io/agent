package agent

import (
	"testing"

	"github.com/stone-age-io/agent/internal/config"
)

// A plain device must not start the edge subsystem.
//
// This is the regression that matters most here. `observability.addr` defaults
// to 127.0.0.1:9100, and it used to be one of the disjuncts in edgeEnabled — so
// every agent ever deployed started the edge, dialled a second NATS connection
// built from nothing but a .creds file (ignoring token and userpass auth and
// the whole nats.tls block), and registered three checks about a leaf node the
// box did not have. A token-authenticated device served /ready as 503 for ever
// while its real connection was fine.
func TestEdgeIsNotEnabledByObservabilityAlone(t *testing.T) {
	cfg := &config.Config{}
	cfg.Observability.Addr = "127.0.0.1:9100"

	if edgeEnabled(cfg) {
		t.Error("serving /ready must not make a box a leaf node: observability is the agent's, " +
			"and the edge only contributes its leaf checks when there is a leaf")
	}
}

// The two capabilities that genuinely need a leaf on the box.
func TestEdgeIsEnabledByLeafCapabilities(t *testing.T) {
	server := &config.Config{}
	server.NATS.ServerConfig = "/etc/agent/nats-leaf.conf"
	if !edgeEnabled(server) {
		t.Error("a box hosting a nats-server has a server to supervise")
	}

	twin := &config.Config{}
	twin.Sync.Twin = true
	if !edgeEnabled(twin) {
		t.Error("a box syncing KV buckets has buckets to wire up")
	}

	mirrors := &config.Config{}
	mirrors.Sync.Mirrors = []config.SyncBucket{{Bucket: "recipes"}}
	if !edgeEnabled(mirrors) {
		t.Error("a declared mirror is edge work")
	}

	relays := &config.Config{}
	relays.Sync.Relays = []config.SyncBucket{{Bucket: "events"}}
	if !edgeEnabled(relays) {
		t.Error("a declared relay is edge work")
	}
}

// An agent with nothing edge-shaped configured does no edge work at all.
func TestBareAgentDoesNoEdgeWork(t *testing.T) {
	if edgeEnabled(&config.Config{}) {
		t.Error("a bare agent has no leaf, no server and no buckets")
	}
}
