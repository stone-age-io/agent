// Package health is the readiness-check engine behind the agent /ready endpoint.
//
// The same package lives in the platform repo. It was COPIED rather than
// extracted into a shared module: two small copies that drift are cheaper to
// live with than a third repo to version and keep both consumers pinned to, and
// the two processes check genuinely different things. If they ever need to agree
// on something, write a test on each side rather than a library between them.
//
// WHAT A READINESS CHECK IS FOR HERE. PocketBase already answers /api/health,
// and that answer is "the HTTP server is listening" — which is true of every
// interesting failure this platform has. A binary serving 200s while the NATS
// server does not trust its operator, or while `bootstrap` was run before
// `migrate up` and every tenancy flag silently went nowhere, is exactly the
// state an operator needs to be told about. So the checks here are deliberately
// about *the things that are silently wrong*, not about liveness.
//
// WHAT EACH PROCESS MAY CHECK. A check must be answerable first-hand by the
// process running it. The Control Plane holds the NATS operator and the $SYS
// account; it has no user credential in any organization's account, so it
// cannot read an org's KV — not `twin`, and not anything a site reports about
// itself. Those are visible to the console, because a browser connects with the
// logged-in user's own in-account credential, and to this agent, because it runs
// inside the account. Do not "improve" a check here
// by giving the Control Plane a credential inside a tenant's account: that
// turns the platform from a credential issuer into a data-plane participant in
// every tenant's bus, which is the one boundary the whole NATS design is built
// around.
package health

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// State is a single check's verdict.
//
// Four states, and only Fail makes the process unready. The two middle states
// exist because collapsing them into OK is how a green tick comes to mean
// nothing:
//
//   - Warn is running-but-misconfigured — the deployment works today and a
//     human should still fix it (at-rest encryption off, no browser-facing
//     WebSocket URL). Failing on these would refuse to serve stock dev
//     deployments, which are legitimately in this state.
//   - Skipped is "this check does not apply here" — no embedded NATS server, no
//     hub domain configured. Reporting it as OK would claim a check ran when
//     nothing was examined.
type State string

const (
	StateOK      State = "ok"
	StateWarn    State = "warn"
	StateFail    State = "fail"
	StateSkipped State = "skipped"
)

// severity orders states so a report can take the worst one. Skipped ranks
// below OK: it is the absence of an answer, never a better one.
func (s State) severity() int {
	switch s {
	case StateFail:
		return 3
	case StateWarn:
		return 2
	case StateOK:
		return 1
	default:
		return 0
	}
}

// Result is one check's outcome.
//
// Fix is the distinguishing field and is not decoration: these checks exist to
// be read by someone who has just deployed the binary and does not yet know the
// bootstrap order. "operator not seeded" sends them to a search engine; "run
// ./stone-age superuser upsert ... then migrate up then bootstrap" ends the
// incident. Every non-OK result should carry one.
type Result struct {
	Name   string `json:"name"`
	State  State  `json:"state"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
	// Took is how long this check ran, filled in by Registry.Run.
	Took string `json:"took,omitempty"`
}

// OK, Warn, Fail and Skip build results. The name is supplied by the registry,
// so a check function never has to repeat it.
func OK(detail string) Result   { return Result{State: StateOK, Detail: detail} }
func Skip(detail string) Result { return Result{State: StateSkipped, Detail: detail} }

func Warn(detail, fix string) Result {
	return Result{State: StateWarn, Detail: detail, Fix: fix}
}

func Fail(detail, fix string) Result {
	return Result{State: StateFail, Detail: detail, Fix: fix}
}

// Func is a single check. It must respect ctx: Registry.Run gives every check a
// deadline, and a check that ignores it can hang a readiness probe until the
// orchestrator restarts a perfectly healthy container.
type Func func(ctx context.Context) Result

// Registry holds the checks a process runs. The zero value is usable.
type Registry struct {
	mu     sync.Mutex
	names  []string
	checks map[string]Func
}

// Register adds a check. Registering the same name twice replaces the first —
// last writer wins rather than silently running two checks under one label,
// which would make the metric series ambiguous.
func (r *Registry) Register(name string, fn Func) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.checks == nil {
		r.checks = make(map[string]Func)
	}
	if _, dup := r.checks[name]; !dup {
		r.names = append(r.names, name)
	}
	r.checks[name] = fn
}

// Report is the whole answer: one Result per registered check, plus the summary
// fields a probe or a human reads first.
type Report struct {
	// Ready is false if and only if some check failed. Warnings do not make a
	// deployment unready — see State.
	Ready bool `json:"ready"`
	// State is the worst state across all checks, so a caller can tell "ready,
	// but you should look at this" from "ready, nothing to report".
	State   State     `json:"state"`
	Version string    `json:"version"`
	Uptime  string    `json:"uptime"`
	Took    string    `json:"took"`
	Checked time.Time `json:"checked"`
	Checks  []Result  `json:"checks"`
}

// Run executes every registered check and assembles a Report.
//
// Checks run concurrently. They are independent by construction (each one
// answers a different question about this process) and several do network I/O,
// so running them in sequence would make the report's latency the sum of the
// slowest paths — which matters because a probe has a timeout and this is what
// it is waiting on.
//
// Each check gets the caller's context; use a deadline on it. A panicking check
// is caught and reported as a failure rather than taking the process down: a
// diagnostic is the last thing that should be able to crash the thing it is
// diagnosing.
func (r *Registry) Run(ctx context.Context, version string, uptime time.Duration) *Report {
	r.mu.Lock()
	names := append([]string(nil), r.names...)
	checks := make(map[string]Func, len(r.checks))
	for k, v := range r.checks {
		checks[k] = v
	}
	r.mu.Unlock()

	started := time.Now()
	results := make([]Result, len(names))

	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string, fn Func) {
			defer wg.Done()
			checkStart := time.Now()
			res := runSafely(ctx, fn)
			res.Name = name
			res.Took = time.Since(checkStart).Round(time.Millisecond).String()
			results[i] = res
		}(i, name, checks[name])
	}
	wg.Wait()

	// Sorted by name so the JSON body and the scrape output are stable between
	// calls; an operator diffing two readiness responses should see only what
	// actually changed.
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })

	// The worst state PRESENT, seeded from the first result rather than from a
	// hardcoded StateOK. Seeding it at OK silently floors the answer: skipped
	// ranks below OK, so a report in which every check skipped would come back
	// "ok" — claiming a clean bill of health for a set of checks not one of
	// which ran. An empty registry is the only case that is genuinely OK.
	worst := StateOK
	if len(results) > 0 {
		worst = results[0].State
	}
	ready := true
	for _, res := range results {
		if res.State.severity() > worst.severity() {
			worst = res.State
		}
		if res.State == StateFail {
			ready = false
		}
	}

	return &Report{
		Ready:   ready,
		State:   worst,
		Version: version,
		Uptime:  uptime.Round(time.Second).String(),
		Checked: started.UTC(),
		Took:    time.Since(started).Round(time.Millisecond).String(),
		Checks:  results,
	}
}

// runSafely turns a panic inside a check into a failed result.
func runSafely(ctx context.Context, fn Func) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			res = Fail(
				fmt.Sprintf("check panicked: %v", p),
				"This is a bug in the check itself, not necessarily in the thing it inspects. Please report it.",
			)
		}
	}()
	if fn == nil {
		return Skip("no check registered")
	}
	return fn(ctx)
}
