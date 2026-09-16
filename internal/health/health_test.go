package health

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixed(res Result) Func {
	return func(context.Context) Result { return res }
}

// The central rule of the report: only a failure makes a deployment unready.
// Warnings describe things a human should fix and a probe cannot; treating them
// as failures would have an orchestrator restart a working platform because
// at-rest encryption is off, which is both useless and self-inflicted downtime.
func TestOnlyFailuresMakeUnready(t *testing.T) {
	cases := []struct {
		name      string
		results   []Result
		wantReady bool
		wantState State
	}{
		{"all ok", []Result{OK("a"), OK("b")}, true, StateOK},
		{"a warning", []Result{OK("a"), Warn("b", "fix")}, true, StateWarn},
		{"a failure", []Result{OK("a"), Fail("b", "fix")}, false, StateFail},
		{"warn and fail", []Result{Warn("a", "f"), Fail("b", "f")}, false, StateFail},
		{"skipped only", []Result{Skip("a")}, true, StateSkipped},
		{"skipped with ok", []Result{Skip("a"), OK("b")}, true, StateOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reg Registry
			for i, res := range tc.results {
				reg.Register(string(rune('a'+i)), fixed(res))
			}
			rep := reg.Run(context.Background(), "test", time.Second)

			if rep.Ready != tc.wantReady {
				t.Errorf("Ready = %v, want %v", rep.Ready, tc.wantReady)
			}
			if rep.State != tc.wantState {
				t.Errorf("State = %q, want %q", rep.State, tc.wantState)
			}
		})
	}
}

// Skipped must not read as OK. A check that never ran and a check that passed
// are different facts, and collapsing them is how a green tick stops meaning
// anything — the leaf-node checks skip themselves when the local monitoring
// port is unreachable, and reporting that as "uplink fine" would be a lie about
// an islanded site.
func TestSkippedRanksBelowOK(t *testing.T) {
	if StateSkipped.severity() >= StateOK.severity() {
		t.Fatal("skipped must rank below ok, or an unrun check reads as a passing one")
	}
	var reg Registry
	reg.Register("only", fixed(Skip("nothing to check")))
	rep := reg.Run(context.Background(), "test", 0)

	if rep.State != StateSkipped {
		t.Errorf("State = %q, want %q", rep.State, StateSkipped)
	}
	if !rep.Ready {
		t.Error("a skipped check must not make the process unready")
	}
}

// A diagnostic must not be able to crash the thing it is diagnosing.
func TestPanickingCheckBecomesAFailure(t *testing.T) {
	var reg Registry
	reg.Register("boom", func(context.Context) Result { panic("kaboom") })
	reg.Register("fine", fixed(OK("ok")))

	rep := reg.Run(context.Background(), "test", 0) // must not panic

	if rep.Ready {
		t.Error("a panicking check should leave the report unready")
	}
	var boom Result
	for _, c := range rep.Checks {
		if c.Name == "boom" {
			boom = c
		}
	}
	if boom.State != StateFail {
		t.Fatalf("boom state = %q, want %q", boom.State, StateFail)
	}
	if !strings.Contains(boom.Detail, "kaboom") {
		t.Errorf("the panic value should reach the report, got %q", boom.Detail)
	}
	// The other check still ran: one bad check must not lose the whole report.
	if len(rep.Checks) != 2 {
		t.Errorf("got %d checks, want 2", len(rep.Checks))
	}
}

// Results carry the name the registry knows them by, and come back sorted, so
// two readiness responses can be diffed without spurious reordering.
func TestResultsAreNamedAndSorted(t *testing.T) {
	var reg Registry
	reg.Register("zulu", fixed(OK("")))
	reg.Register("alpha", fixed(OK("")))
	reg.Register("mike", fixed(OK("")))

	rep := reg.Run(context.Background(), "test", 0)

	got := make([]string, len(rep.Checks))
	for i, c := range rep.Checks {
		got[i] = c.Name
	}
	want := []string{"alpha", "mike", "zulu"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("check order = %v, want %v", got, want)
		}
	}
}

// Re-registering a name replaces it rather than running both. Two checks under
// one label would make the metric series ambiguous — the state set would have
// two writers racing for the same gauge.
func TestRegisterReplacesByName(t *testing.T) {
	var reg Registry
	reg.Register("dup", fixed(OK("first")))
	reg.Register("dup", fixed(Fail("second", "fix")))

	rep := reg.Run(context.Background(), "test", 0)

	if len(rep.Checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(rep.Checks))
	}
	if rep.Checks[0].Detail != "second" {
		t.Errorf("detail = %q, want the second registration to win", rep.Checks[0].Detail)
	}
}

// A check that ignores its context must not be able to hang the probe forever.
// Run gives every check the caller's context; this asserts the deadline is
// actually propagated, which is the half a check can rely on.
func TestChecksReceiveTheCallerDeadline(t *testing.T) {
	var reg Registry
	var gotDeadline bool
	reg.Register("ctx", func(ctx context.Context) Result {
		_, gotDeadline = ctx.Deadline()
		return OK("")
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reg.Run(ctx, "test", 0)

	if !gotDeadline {
		t.Error("checks must be given the caller's deadline")
	}
}

// Readiness maps onto the HTTP status a probe acts on: 503 unready, 200 ready
// — including ready-with-warnings, since a restart cannot fix a warning.
func TestStatusCodeAndBody(t *testing.T) {
	var reg Registry
	reg.Register("warn", fixed(Warn("noisy", "do something")))
	rep := reg.Run(context.Background(), "v1.2.3", 42*time.Second)

	rec := httptest.NewRecorder()
	WriteJSON(rec, rep)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200 for ready-with-warnings", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — a cached readiness answer is worse than none", cc)
	}

	var body Report
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if !body.Ready || body.State != StateWarn {
		t.Errorf("body = %+v, want ready with state warn", body)
	}
	if body.Version != "v1.2.3" {
		t.Errorf("version = %q, want it carried into the body", body.Version)
	}
}

// Before the first probe there is no answer, and "no answer" must read as
// unready rather than as an empty success — that is exactly the window during
// startup when traffic should not be routed here yet.
func TestNilReportIsUnready(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, nil)

	if rec.Code != 503 {
		t.Errorf("status = %d, want 503 before the first probe", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not been probed") {
		t.Errorf("body should say why it is unready, got %q", rec.Body.String())
	}
}

// The prober serves an answer the moment Start returns, and refreshes it.
func TestProberSnapshotAndHooks(t *testing.T) {
	var reg Registry
	reg.Register("steady", fixed(OK("initial")))

	p := NewProber(&reg, "test", 20*time.Millisecond, time.Second)

	var refreshes, changes int
	p.OnRefresh(func(*Report) { refreshes++ })
	p.OnChange(func(*Report) { changes++ })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	if snap := p.Snapshot(); snap == nil {
		t.Fatal("Snapshot must be populated by the time Start returns")
	}
	// Start runs once synchronously, so both hooks have fired exactly once —
	// OnChange included, because nil -> first report is a transition.
	if refreshes != 1 || changes != 1 {
		t.Fatalf("after Start: refreshes=%d changes=%d, want 1 and 1", refreshes, changes)
	}
}

// OnRefresh fires every probe and OnChange only on a flip. Collapsing them
// would either leave the metrics gauges stale between transitions or write a
// log line every interval forever.
func TestRefreshFiresEveryProbeChangeOnlyOnFlip(t *testing.T) {
	var reg Registry
	res := OK("")
	reg.Register("flip", func(context.Context) Result { return res })

	p := NewProber(&reg, "test", time.Hour, time.Second) // manual refreshes only

	var refreshes, changes int
	p.OnRefresh(func(*Report) { refreshes++ })
	p.OnChange(func(*Report) { changes++ })

	ctx := context.Background()
	p.refresh(ctx) // nil -> ready: a change
	p.refresh(ctx) // ready -> ready: not a change
	res = Fail("down", "fix")
	p.refresh(ctx) // ready -> unready: a change
	p.refresh(ctx) // unready -> unready: not a change

	if refreshes != 4 {
		t.Errorf("refreshes = %d, want 4 (one per probe)", refreshes)
	}
	if changes != 2 {
		t.Errorf("changes = %d, want 2 (only the flips)", changes)
	}
}
