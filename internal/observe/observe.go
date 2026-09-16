// Package observe serves this agent's readiness and metrics over HTTP.
//
// It is deliberately not edge-specific. The checks and collectors are supplied
// by the caller, so a gateway registers leaf checks and a plain device
// registers whatever it can answer — the serving, the prober, the token and the
// listener are the same either way.
//
// WHY LOCAL HTTP AT ALL, when the agent already reports over NATS. Because the
// NATS report travels over the link that breaks. A device whose uplink is down
// is exactly the device you want to ask "are you alright", and `cmd.health`
// goes quiet at precisely that moment. These endpoints are scraped on the box,
// or from the LAN, and keep answering through a WAN outage.
package observe

import (
	"context"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/stone-age-io/agent/internal/health"
	"github.com/stone-age-io/agent/internal/metrics"
)

// Options configures one observability endpoint.
type Options struct {
	// Namespace prefixes the readiness gauges (agent_ready, and friends).
	Namespace string
	Version   string

	// Addr is the listen address. Empty serves nothing — the checks still run
	// and still log, they are just not reachable over the network.
	Addr string

	// Token optionally protects /metrics, accepted as Bearer or as Basic with
	// any username. Empty means open, which is reasonable on loopback.
	Token string

	// Interval is how often the checks run.
	Interval time.Duration

	// Registry holds the checks. Required.
	Registry *health.Registry

	// Collectors are registered alongside the readiness gauges.
	Collectors []prometheus.Collector

	// LogLabel names this agent in the readiness transition log line.
	LogLabel string
}

// Server owns the prober and, when an address is configured, the listener.
type Server struct {
	prober  *health.Prober
	metrics *metrics.Set
	addr    string
	token   string
	srv     *http.Server
}

func New(opts Options) *Server {
	set := metrics.New(opts.Namespace, opts.Version)
	for _, c := range opts.Collectors {
		set.Registry.MustRegister(c)
	}

	prober := health.NewProber(opts.Registry, opts.Version, opts.Interval, health.DefaultProbeTimeout)
	set.Bind(prober)

	label := opts.LogLabel
	if label == "" {
		label = "agent readiness"
	}
	prober.OnChange(func(rep *health.Report) { health.LogReport(label, rep) })

	return &Server{
		prober:  prober,
		metrics: set,
		addr:    opts.Addr,
		token:   opts.Token,
	}
}

// Start begins probing and, when an address is configured, serves /ready and
// /metrics on it.
//
// The listener is opt-in and the prober is not. Running the checks costs
// nothing and means the log carries a readiness transition even on a box with
// no monitoring; opening a port on an appliance is a decision someone should
// have made on purpose.
func (s *Server) Start(ctx context.Context) {
	go s.prober.Start(ctx)

	if s.addr == "" {
		return
	}

	mux := http.NewServeMux()
	mux.Handle("/ready", s.prober.Handler())
	mux.Handle("/metrics", s.metrics.Handler(s.token))
	// A bare GET on the root is what someone typing the address into a browser
	// does. Point them at the two real paths rather than 404ing blankly.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "agent: try /ready or /metrics", http.StatusNotFound)
	})

	s.srv = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		// Never fatal. A monitoring port that cannot bind must not stop the
		// agent doing its job — that would make observability a dependency of
		// the thing it observes.
		log.Printf("⚠️ agent: cannot serve /ready and /metrics on %s: %v", s.addr, err)
		return
	}
	log.Printf("agent: readiness + metrics on http://%s/ready and /metrics", s.addr)

	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("⚠️ agent: observability listener stopped: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
	}()
}
