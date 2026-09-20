package edge

import "time"

// Config is what the edge subsystem needs, independent of how the agent's YAML
// is shaped. The agent's own config package fills it — keeping this struct
// separate is what let the whole package arrive from the platform repo without
// touching a single function signature.
//
// It is deliberately smaller than the leaf-sync config it descends from (that
// binary lived in the platform repo and is gone). Dropped in the move:
// the PocketBase URL and credentials (the agent already holds one authenticated
// platform client, and a second set of credentials for the same identity was
// the duplication this merge exists to remove); HubLeafURL (served by
// /api/me/leaf-config, so a fleet is told it once on the Control Plane rather
// than per box); MonitorURL (it duplicated a constant the generated conf
// hardcodes — see monitorURL below); and the config-mirror settings, which went
// with the mirror.
type Config struct {
	// LocalNatsURL is the local leaf this agent connects to at run time.
	LocalNatsURL string

	// CredsFile is the creds filename written beside the generated conf and
	// referenced from it.
	CredsFile string

	// OutputDir is where the one-shot writes nats-leaf.conf and the creds.
	OutputDir string

	// EmbeddedConfig is the nats-leaf.conf to host in-process. Empty means the
	// agent does not run a NATS server; something else supervises it.
	EmbeddedConfig string

	// HubDomain is the hub's JetStream domain, served by /api/me/leaf-config.
	// The twin relay addresses the hub across the leaf link with it.
	HubDomain string

	// TwinEnabled turns on the digital-twin preset: a mirror of `twin_desired`
	// down and a relay of `twin` up, with the bucket shape the console agrees on
	// (see twin.go). Off by default — it moves data plane traffic, so an upgrade
	// must not silently start doing it. Requires HubDomain.
	//
	// It is a bool rather than two entries supplied by the caller because the
	// preset's bucket names and retention are this package's knowledge, and
	// because a preset entry is allowed to create its hub-side bucket where a
	// user-declared one is not.
	TwinEnabled bool

	// Mirrors and Relays are the user-declared buckets, hub → edge and
	// edge → hub respectively. See sync.go.
	Mirrors []Bucket
	Relays  []Bucket

	// ObserveAddr is the listen address for /ready and /metrics. Empty means the
	// endpoints are not served — the readiness checks still run and still log,
	// they just are not reachable over the network. Opening a port on an edge
	// appliance should be a decision, not a default.
	ObserveAddr string

	// MetricsToken optionally protects /metrics. Empty means open, which is
	// reasonable on an address bound to loopback or a management LAN.
	MetricsToken string

	// ReadinessInterval is how often the checks run.
	ReadinessInterval time.Duration
}

// monitorURL is the local NATS server's monitoring endpoint: the `http:` line
// buildLeafConf writes into every generated nats-leaf.conf. It is how the edge
// reads its own server's state — uplink attached? JetStream size? — without ever
// holding a $SYS identity, which is why it is loopback and unauthenticated.
//
// This used to be a config key that defaulted to the same string the generator
// hardcodes: two controls for one value, one of them written by this very
// process, free to disagree. It is a constant now. If a deployment ever needs
// to move it, change it in ONE place and both the writer and the reader follow.
const monitorURL = "http://127.0.0.1:8222"
