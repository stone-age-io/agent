//go:build linux

package tasks

import (
	"context"
	"os"
	"testing"

	"go.uber.org/zap"
)

// QueryService against the real systemd. An allowlisted service that does not
// exist answers NotInstalled, as it does in the service_check telemetry,
// rather than failing the command.
func TestQueryServiceSystemd(t *testing.T) {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("systemd is not running here")
	}
	executor := NewExecutor(zap.NewNop(), 0, context.Background())

	tests := []struct {
		service string
		want    string
	}{
		{"systemd-journald", ServiceStatusRunning}, // always up under systemd
		{"agent-test-no-such-service", ServiceStatusNotInstalled},
	}
	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			status, err := executor.QueryService(tt.service, []string{tt.service})
			if err != nil {
				t.Fatalf("QueryService() error = %v", err)
			}
			if status.Name != tt.service || status.Status != tt.want {
				t.Errorf("QueryService() = %+v, want %s", status, tt.want)
			}
		})
	}
}
