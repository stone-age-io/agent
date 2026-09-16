package health

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// Default probe timings. Deliberately conservative: the checks talk to SQLite
// and open a TCP connection to the NATS server, and none of that needs to
// happen at the rate a Kubernetes probe or a Docker HEALTHCHECK polls.
const (
	DefaultProbeInterval = 15 * time.Second
	DefaultProbeTimeout  = 5 * time.Second
)

// Prober runs a Registry on an interval and caches the latest Report.
//
// WHY CACHED RATHER THAN ON DEMAND. A readiness endpoint is polled by things
// that restart containers. Running the checks inside the request would mean a
// NATS dial per probe (so a probe rate that scales with an unrelated setting),
// and — worse — a slow check would blow the prober's own timeout and be read as
// "unready", killing a container whose only problem is that the diagnostic was
// slow. Decoupling the two means the endpoint always answers instantly with a
// recent answer, and a check that hangs shows up as a stale report rather than
// as an outage.
//
// The staleness that buys is real and bounded: a report can be up to one
// interval old. Report.Checked carries the timestamp so a reader can see that
// for themselves rather than having to know the interval.
type Prober struct {
	reg      *Registry
	version  string
	interval time.Duration
	timeout  time.Duration
	started  time.Time
	snap     atomic.Pointer[Report]

	// Two hooks, because they answer different questions and collapsing them
	// gets one of the two wrong.
	//
	// onRefresh fires after every probe: metrics must track the latest report,
	// and a gauge that only updated on a transition would sit stale for as long
	// as nothing changed — indistinguishable from a wedged prober.
	//
	// onChange fires only when the ready flag flips, because that is what a log
	// line is for. Logging every probe would write a line every interval
	// forever and bury the one that mattered.
	onRefresh func(*Report)
	onChange  func(*Report)
}

// NewProber builds a prober. A zero interval or timeout uses the defaults.
func NewProber(reg *Registry, version string, interval, timeout time.Duration) *Prober {
	if interval <= 0 {
		interval = DefaultProbeInterval
	}
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	return &Prober{
		reg:      reg,
		version:  version,
		interval: interval,
		timeout:  timeout,
		started:  time.Now(),
	}
}

// OnRefresh registers a callback fired after every probe. Call it before Start.
func (p *Prober) OnRefresh(fn func(*Report)) { p.onRefresh = fn }

// OnChange registers a callback fired when the ready flag flips. Call it before
// Start.
func (p *Prober) OnChange(fn func(*Report)) { p.onChange = fn }

// Start runs the checks once synchronously, then keeps them refreshed in the
// background until ctx is cancelled.
//
// The first run is synchronous so that by the time Start returns there is
// always a Report to serve. Otherwise the first probe — which for a container
// orchestrator arrives immediately — would race the first refresh and read a
// nil snapshot, and "no data yet" is indistinguishable from "not ready" at the
// HTTP layer.
func (p *Prober) Start(ctx context.Context) {
	p.refresh(ctx)
	go func() {
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.refresh(ctx)
			}
		}
	}()
}

func (p *Prober) refresh(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	rep := p.reg.Run(runCtx, p.version, time.Since(p.started))
	prev := p.snap.Swap(rep)

	if p.onRefresh != nil {
		p.onRefresh(rep)
	}
	if p.onChange != nil && (prev == nil || prev.Ready != rep.Ready) {
		p.onChange(rep)
	}
}

// Snapshot returns the most recent Report, or nil before the first run.
func (p *Prober) Snapshot() *Report { return p.snap.Load() }

// statusCode is the HTTP status a readiness response should carry: 200 when
// ready, 503 when not.
//
// Warnings return 200 on purpose. A probe's only lever is to restart or
// de-register the process, and neither fixes "you have not set an encryption
// key" — it would just take a working deployment offline over a note.
func statusCode(rep *Report) int {
	if rep == nil || !rep.Ready {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

// WriteJSON writes a Report as an HTTP response with the right status code.
// A nil report (probe has not run yet) is a 503 with a body saying so, rather
// than an empty 503 that looks identical to a failing check.
func WriteJSON(w http.ResponseWriter, rep *Report) {
	w.Header().Set("Content-Type", "application/json")
	// Readiness is a point-in-time answer; a cached one is worse than useless
	// to the thing deciding whether to route traffic here.
	w.Header().Set("Cache-Control", "no-store")

	if rep == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ready":  false,
			"state":  StateFail,
			"checks": []Result{},
			"detail": "readiness has not been probed yet",
		})
		return
	}

	w.WriteHeader(statusCode(rep))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ") // read by humans with curl at least as often as by probes
	_ = enc.Encode(rep)
}

// Handler serves the cached report over plain net/http. The Control Plane
// registers its own route on PocketBase's router instead and calls WriteJSON;
// this is for the agent, which has no router of its own.
func (p *Prober) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, p.Snapshot())
	})
}

// LogReport prints a readiness transition. Used as the OnChange callback by
// both binaries so the two logs read the same.
func LogReport(prefix string, rep *Report) {
	if rep.Ready {
		if rep.State == StateWarn {
			log.Printf("✅ %s: ready (with warnings)", prefix)
			for _, c := range rep.Checks {
				if c.State == StateWarn {
					log.Printf("   ⚠️  %s: %s", c.Name, c.Detail)
				}
			}
			return
		}
		log.Printf("✅ %s: ready", prefix)
		return
	}
	log.Printf("❌ %s: NOT ready", prefix)
	for _, c := range rep.Checks {
		if c.State != StateFail {
			continue
		}
		log.Printf("   ❌ %s: %s", c.Name, c.Detail)
		if c.Fix != "" {
			log.Printf("      → %s", c.Fix)
		}
	}
}
