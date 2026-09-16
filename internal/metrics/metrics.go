// Package metrics is the Prometheus exposition shared by the Control Plane and
// the edge agent.
//
// WHY PROMETHEUS AND NOT A JSON ENDPOINT. The point of a metrics endpoint is
// that something else already knows how to read it. A bespoke JSON shape needs
// a bespoke consumer, and this platform's users are integrators who already run
// Prometheus, Grafana Alloy, Telegraf or an uptime checker — all of which speak
// this format and none of which speak ours. The client library rather than a
// hand-written text encoder for the same reason the leaf config is validated by
// nats-server itself: there is no frontend test runner and no scrape test here,
// so a subtly malformed exposition (label escaping, `_total` conventions, NaN)
// would look fine and be wrong. It also costs no binary size — slackhq/nebula
// already links client_golang in.
//
// WHAT IS DELIBERATELY NOT HERE. No per-organization labels. The Control Plane
// can only count rows in its own database, so per-org series would be tenant
// names attached to inventory counts — customer-identifying, of no operational
// use, and a poor trade on an endpoint that defaults to open. The numbers that
// would justify a label (which site is offline, which bucket is stale) live
// inside an organization's NATS account, which this process has no credential
// for; this agent exposes those from the edge, where they can actually be seen.
package metrics

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/stone-age-io/agent/internal/health"
)

// allStates is every state a check can report. Each one gets a series so the
// exposition is a complete state set rather than a boolean that loses the
// difference between "warned" and "was never run".
var allStates = []health.State{
	health.StateOK,
	health.StateWarn,
	health.StateFail,
	health.StateSkipped,
}

// Set is one process's metrics: its own registry plus the readiness gauges
// every binary here publishes.
//
// A private registry rather than prometheus.DefaultRegisterer, because
// slackhq/nebula registers its own collectors into the default one when a
// Nebula host runs in-process. Sharing it would mean this endpoint's contents
// depend on which unrelated library happened to initialise.
type Set struct {
	Registry *prometheus.Registry

	ready      prometheus.Gauge
	checkState *prometheus.GaugeVec
	checkTime  prometheus.Gauge
}

// New builds a metrics set under the given namespace ("stone_age", "agent")
// and registers the Go runtime and process collectors alongside it.
func New(namespace, version string) *Set {
	reg := prometheus.NewRegistry()

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	// The conventional build-info pattern: a constant 1 whose labels carry the
	// facts. It makes "which version reported this" answerable in a query
	// rather than out of band, which is the whole reason `--version` reports
	// the pb-* library versions too.
	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "build_info",
		Help:      "Build information. Always 1; read the labels.",
	}, []string{"version"})
	buildInfo.WithLabelValues(version).Set(1)
	reg.MustRegister(buildInfo)

	s := &Set{
		Registry: reg,
		ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "ready",
			Help:      "1 if the last readiness probe passed, 0 if any check failed. Warnings do not clear this.",
		}),
		checkState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "check_state",
			Help:      "Readiness check state set: 1 on the check's current state, 0 on the others.",
		}, []string{"name", "state"}),
		checkTime: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "check_timestamp_seconds",
			Help:      "Unix time of the last readiness probe. Alert on this going stale — it means the prober itself is wedged.",
		}),
	}
	reg.MustRegister(s.ready, s.checkState, s.checkTime)
	return s
}

// Observe copies a readiness report into the gauges. Safe to call with nil,
// which leaves the gauges at their zero values — correct, because "never
// probed" and "not ready" should both read as not ready.
func (s *Set) Observe(rep *health.Report) {
	if rep == nil {
		s.ready.Set(0)
		return
	}
	if rep.Ready {
		s.ready.Set(1)
	} else {
		s.ready.Set(0)
	}
	s.checkTime.Set(float64(rep.Checked.Unix()))

	for _, c := range rep.Checks {
		for _, st := range allStates {
			v := 0.0
			if c.State == st {
				v = 1.0
			}
			s.checkState.WithLabelValues(c.Name, string(st)).Set(v)
		}
	}
}

// Bind wires a prober's reports into this set, updating the gauges after every
// probe. Call it before Prober.Start.
//
// It hangs the update off the prober rather than off scrape time on purpose. A
// collector that ran the checks during a scrape would put a NATS dial on the
// scrape path, so the probe rate would become whatever the operator configured
// in Prometheus, and a slow check would time out the scrape rather than showing
// up as a stale check_timestamp_seconds.
func (s *Set) Bind(p *health.Prober) {
	p.OnRefresh(s.Observe)
}

// Handler returns the /metrics handler, optionally protected by a shared token.
//
// An empty token means no authentication, which is the default. That is a
// deliberate choice and not laziness: the series exposed here are aggregate
// counts and process statistics with no customer identifiers in them, and a
// metrics endpoint that needs a credential to read is one that mostly does not
// get scraped. Operators who do want it closed get a token that satisfies both
// forms every scraper already supports.
//
// The token is accepted two ways because scrapers differ and neither form is
// more correct: `Authorization: Bearer <token>` (Prometheus `authorization`,
// Alloy, Vector) and HTTP Basic with the token as the password and any username
// (Prometheus `basic_auth`, Telegraf, most uptime checkers, and a plain
// browser). PocketBase's own auth is deliberately NOT used: its tokens are JWTs
// that expire, and no scraper has a refresh flow, so it would take a custom
// sidecar to read a standard format.
func (s *Set) Handler(token string) http.Handler {
	h := promhttp.HandlerFor(s.Registry, promhttp.HandlerOpts{
		// A broken collector should not take the endpoint down; report what
		// works and log the rest.
		ErrorHandling: promhttp.ContinueOnError,
	})
	if token == "" {
		return h
	}
	return requireToken(token, h)
}

func requireToken(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenMatches(r, want) {
			next.ServeHTTP(w, r)
			return
		}
		// Advertise Basic so a browser offers a prompt and a misconfigured
		// scraper gets a diagnosable 401 rather than a blank refusal.
		w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func tokenMatches(r *http.Request, want []byte) bool {
	if _, pass, ok := r.BasicAuth(); ok {
		if subtle.ConstantTimeCompare([]byte(pass), want) == 1 {
			return true
		}
	}
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		if subtle.ConstantTimeCompare([]byte(bearer), want) == 1 {
			return true
		}
	}
	return false
}
