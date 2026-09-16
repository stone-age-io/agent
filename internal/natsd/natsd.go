// Package natsd runs a NATS server inside the Control Plane process.
//
// The server is an ordinary nats-server. It reads the same nats.conf that
// `stone-age nats export` writes, and behaves exactly as it would if you ran
// `nats-server -c nats.conf` yourself. There is no embedded-only mode, no
// options derived in Go, and no second way for an account JWT to reach it: the
// NATS support library still publishes account claims over $SYS exactly as it
// does to an external server.
//
// That sameness is the point. It keeps one code path in the library, keeps the
// config a real file you can read and edit, and makes moving to an external
// server a config change rather than a migration.
//
// Clustering follows from that sameness rather than being a separate feature:
// a `cluster` block in nats.conf is honoured like any other directive, so a
// Control Plane can be one node of a cluster whose other nodes are ordinary
// external nats-servers. TestEmbeddedServerClustersWithAPeer pins this, because
// an earlier version of this comment claimed the opposite and nothing in the
// code ever enforced it.
//
// The one thing to know before choosing it: this server's lifetime is the
// Control Plane's, so restarting the platform takes a cluster node down with
// it. That is an operational cost to weigh, not a restriction — run the bus as
// a separate process (the default) wherever it is the wrong trade.
package natsd

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"sync"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

const (
	// readyTimeout is how long to wait for the server to accept connections
	// before giving up. Generous: JetStream recovers its store on startup, and
	// a large store on slow disk is not an error.
	readyTimeout = 30 * time.Second

	// defaultConfigDir is the output directory the docs use for `nats export`.
	defaultConfigDir = "./nats-config"

	// DefaultConfigFile is the config --nats loads unless --nats-config says
	// otherwise. It is what `nats export --output ./nats-config/` produces.
	DefaultConfigFile = defaultConfigDir + "/nats.conf"
)

// Server wraps an embedded nats-server.
type Server struct {
	ns *natsserver.Server
}

// Start loads confPath, starts a NATS server from it, and waits for it to
// accept connections.
//
// clientURL is the address the calling process is configured to dial. It is not
// used to connect — it is checked against the port the server actually listens
// on, because the two disagreeing is the one misconfiguration that leaves a
// process talking to itself and failing. clientURLSetting names the config key
// clientURL came from (`nats.server_url` in the Control Plane, `nats.local_url`
// on an edge), so the error can tell the reader which knob to turn.
//
// The returned error is intended to be fatal to startup. A process asked for an
// embedded server that cannot start should say so and stop, not run on in a
// state where every credential it issues points at nothing.
func Start(confPath, clientURL, clientURLSetting string) (*Server, error) {
	if _, err := os.Stat(confPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"NATS config not found at %s\n"+
					"       Generate it first:  ./stone-age nats export --output %s\n"+
					"       Or point at an existing config with --nats-config",
				confPath, defaultConfigDir)
		}
		return nil, fmt.Errorf("cannot read NATS config %s: %w", confPath, err)
	}

	opts, err := natsserver.ProcessConfigFile(confPath)
	if err != nil {
		return nil, fmt.Errorf("invalid NATS config %s: %w", confPath, err)
	}

	// PocketBase owns SIGINT/SIGTERM. Without this nats-server installs its own
	// handlers and the two fight over shutdown.
	opts.NoSigs = true

	ns, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create NATS server: %w", err)
	}

	// Route NATS output through the platform logger so there is one log stream.
	// Set before Start so nothing is written anywhere else first.
	//
	// A `log_file` in nats.conf is not honoured as a result. That is a known
	// trade for having one place to look when something goes wrong.
	logger := newLogger()
	ns.SetLogger(logger, opts.Debug, opts.Trace)

	go ns.Start()

	if err := waitReady(ns, logger); err != nil {
		ns.Shutdown()
		ns.WaitForShutdown()
		return nil, err
	}

	if err := checkPortsAgree(ns.ClientURL(), clientURL, clientURLSetting, confPath); err != nil {
		ns.Shutdown()
		ns.WaitForShutdown()
		return nil, err
	}

	log.Printf("✅ Embedded NATS server listening on %s (config: %s)", ns.ClientURL(), confPath)
	return &Server{ns: ns}, nil
}

// waitReady blocks until the server accepts connections, or fails.
//
// It polls rather than calling ReadyForConnections(readyTimeout) once, because
// a server can log a fatal and never become ready — a JetStream store it cannot
// open, a system account it will not accept — and waiting out the full timeout
// to then report a recorded fatal turns a one-second failure into a thirty
// second one. nats-server's Start() returns after a fatal instead of panicking,
// so the recorded message is the only diagnosis available.
func waitReady(ns *natsserver.Server, logger *logger) error {
	deadline := time.Now().Add(readyTimeout)
	for {
		if ns.ReadyForConnections(250 * time.Millisecond) {
			// A server can accept connections while a subsystem the config
			// asked for failed. Refuse it: JetStream silently missing is worse
			// than not starting, because the twins just never persist.
			if reason := logger.lastFatal(); reason != "" {
				return fmt.Errorf("embedded NATS server started but reported a fatal error: %s", reason)
			}
			return nil
		}
		if reason := logger.lastFatal(); reason != "" {
			return fmt.Errorf("embedded NATS server failed to start: %s", reason)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("embedded NATS server did not become ready within %s", readyTimeout)
		}
	}
}

// checkPortsAgree fails when the embedded server listens on a different port
// from the one the calling process dials.
//
// Only the port is compared. Hosts differ legitimately all the time — the
// server binds 0.0.0.0 while the configured URL names a hostname — but a port
// mismatch cannot be anything except a mistake, and the symptom is miserable to
// diagnose: the caller sits retrying forever against a server running in its
// own process.
func checkPortsAgree(listenURL, configuredURL, setting, confPath string) error {
	listenPort, err := portOf(listenURL)
	if err != nil {
		return fmt.Errorf("cannot parse embedded server address %q: %w", listenURL, err)
	}
	configuredPort, err := portOf(configuredURL)
	if err != nil {
		// Not our business to validate the caller's URL beyond this check.
		return nil
	}
	if listenPort == configuredPort {
		return nil
	}
	return fmt.Errorf(
		"embedded NATS server listens on port %s but %s is %s\n"+
			"       Nothing in this process would ever reach it. Make them agree:\n"+
			"       set %s to port %s, or set `port: %s` in %s",
		listenPort, setting, configuredURL, setting, listenPort, configuredPort, confPath)
}

func portOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if p := u.Port(); p != "" {
		return p, nil
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host in %q", rawURL)
	}
	return "4222", nil // NATS default when the URL omits a port
}

// Stats is the embedded server's own view of itself, for the metrics endpoint.
//
// WHY THIS IS IN BOUNDS. Everything here is server-level, not account-level: a
// NATS server counts its own connections and leaf remotes regardless of which
// account they authenticated into, and reports its own JetStream store size.
// None of it requires a credential inside a tenant's account, which is why the
// Control Plane may publish it while it may not publish, say, the contents of
// an organization's `twin` bucket.
//
// Leafnodes is the number of leaf CONNECTIONS currently attached to this
// server, across every account it hosts. It is a capacity number rather than a
// per-tenant availability signal: a tenant asks about its own sites through
// $SYS.REQ.ACCOUNT.PING.CONNZ, which is scoped to its account and names each
// leaf by the server_name its agent set, which is the Thing code.
type Stats struct {
	Connections int
	Leafnodes   int
	// Routes is peer connections to other servers in the same cluster. Not
	// always zero: Start honours a `cluster` block like any other directive, so
	// a Control Plane peered with external nodes reports them here. See
	// TestEmbeddedServerClustersWithAPeer.
	Routes        int
	SlowConsumers int64
	InMsgs        int64
	OutMsgs       int64
	InBytes       int64
	OutBytes      int64

	JetStreamEnabled bool
	JetStreamMemory  uint64
	JetStreamStore   uint64
}

// Stats reports the embedded server's counters. The second return is false when
// no embedded server is running (the default topology), so the caller can leave
// the corresponding metrics unpublished rather than exporting zeros that look
// like a server with no traffic.
func (s *Server) Stats() (*Stats, bool) {
	if s == nil || s.ns == nil {
		return nil, false
	}
	vz, err := s.ns.Varz(nil)
	if err != nil || vz == nil {
		return nil, false
	}
	st := &Stats{
		Connections:   vz.Connections,
		Leafnodes:     vz.Leafs,
		Routes:        vz.Routes,
		SlowConsumers: vz.SlowConsumers,
		InMsgs:        vz.InMsgs,
		OutMsgs:       vz.OutMsgs,
		InBytes:       vz.InBytes,
		OutBytes:      vz.OutBytes,
	}
	if js := vz.JetStream.Stats; js != nil {
		st.JetStreamEnabled = true
		st.JetStreamMemory = js.Memory
		st.JetStreamStore = js.Store
	}
	return st, true
}

// Stop shuts the server down and waits for it to finish.
func (s *Server) Stop() {
	if s == nil || s.ns == nil {
		return
	}
	log.Println("ℹ️ Shutting down embedded NATS server")
	s.ns.Shutdown()
	s.ns.WaitForShutdown()
}

// logger adapts nats-server's Logger onto the standard library log package,
// which is what the rest of the binary uses.
//
// Fatalf deliberately does not exit. nats-server calls it and then returns from
// Start, so recording the message lets Start report a real reason instead of a
// bare timeout.
type logger struct {
	mu    sync.Mutex
	fatal string
}

func newLogger() *logger { return &logger{} }

func (l *logger) lastFatal() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fatal
}

func (l *logger) Noticef(format string, v ...any) { log.Printf("nats: "+format, v...) }
func (l *logger) Warnf(format string, v ...any)   { log.Printf("nats: ⚠️  "+format, v...) }
func (l *logger) Errorf(format string, v ...any)  { log.Printf("nats: ❌ "+format, v...) }
func (l *logger) Debugf(format string, v ...any)  { log.Printf("nats: "+format, v...) }
func (l *logger) Tracef(format string, v ...any)  { log.Printf("nats: "+format, v...) }

func (l *logger) Fatalf(format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	l.mu.Lock()
	l.fatal = msg
	l.mu.Unlock()
	log.Printf("nats: ❌ %s", msg)
}
