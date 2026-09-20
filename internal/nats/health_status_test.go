package nats

import (
	"testing"

	"github.com/stone-age-io/agent/internal/health"
)

// cmd.health's three words come from the readiness report and nothing else.
//
// They used to come from a ladder of inline conditions that duplicated — and
// then drifted from — the readiness checks. The edge's checks were never in
// that ladder at all, so a gateway with every synced bucket down answered
// "healthy" over NATS while its own /ready endpoint disagreed.
func TestHealthStatusComesFromTheReport(t *testing.T) {
	tests := []struct {
		name   string
		report *health.Report
		want   string
	}{
		{
			name:   "every check ok",
			report: &health.Report{State: health.StateOK, Ready: true},
			want:   "healthy",
		},
		{
			// An islanded gateway, a rolled-back overlay, a bucket that will not
			// sync: it works and someone should look at it.
			name:   "a warning",
			report: &health.Report{State: health.StateWarn, Ready: true},
			want:   "degraded",
		},
		{
			name:   "a failure",
			report: &health.Report{State: health.StateFail},
			want:   "unhealthy",
		},
		{
			// Nothing was examined, which is not a clean bill of health.
			name:   "everything skipped",
			report: &health.Report{State: health.StateSkipped, Ready: true},
			want:   "degraded",
		},
		{
			// agent.New starts the prober, synchronously, before the command
			// subscriptions exist — so this should be unreachable. If it happens
			// anyway, a diagnostic that has not run is not a pass.
			name:   "no report yet",
			report: nil,
			want:   "degraded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := determineHealthStatus(tt.report); got != tt.want {
				t.Errorf("determineHealthStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A warning must never read as unready, and must never be lost either.
//
// health.Report.Ready is false only on a failure, so a warned agent answers 200
// on /ready and "degraded" over NATS. Those two are different questions — "may
// I route traffic here" and "is anything wrong" — and collapsing them is how a
// green tick comes to mean nothing.
func TestWarningIsDegradedButStillReady(t *testing.T) {
	rep := &health.Report{State: health.StateWarn, Ready: true}

	if got := determineHealthStatus(rep); got != "degraded" {
		t.Errorf("a warned agent reports %q over NATS, want \"degraded\"", got)
	}
	if !rep.Ready {
		t.Error("a warning must not make an agent unready: it would pull a working site out of rotation")
	}
}
