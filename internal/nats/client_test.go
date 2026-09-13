package nats

import (
	"net"
	"testing"
	"time"

	"github.com/stone-age-io/agent/internal/config"
	"go.uber.org/zap"
)

// unreachableConfig points at a port nothing is listening on, with a short
// reconnect wait so a test that creates a client does not spend real time
// retrying in the background before it is closed.
func unreachableConfig(t *testing.T) *config.NATSConfig {
	t.Helper()

	// Bind and immediately release, so the port is one the OS just confirmed is
	// free rather than one guessed and hoped for.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	return &config.NATSConfig{
		URLs:          []string{"nats://" + addr},
		Auth:          config.AuthConfig{Type: "none"},
		MaxReconnects: -1,
		ReconnectWait: time.Second,
		DrainTimeout:  time.Second,
	}
}

// TestNewClientSucceedsWhenServerUnreachable is the regression test for the
// startup change: the agent must be able to construct a NATS client before the
// bus is reachable.
//
// This is what lets NATS and a Nebula overlay come up in either order. Without
// RetryOnFailedConnect, an agent whose NATS endpoint lives on the overlay could
// never start: the connect would fail, the process would exit, and the overlay
// that would have made the connect succeed never got the chance to come up.
func TestNewClientSucceedsWhenServerUnreachable(t *testing.T) {
	client, err := NewClient(unreachableConfig(t), zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient() returned an error for an unreachable server: %v", err)
	}
	defer client.Close()

	if client.IsConnected() {
		t.Error("IsConnected() = true for an unreachable server, want false")
	}

	// JetStream cannot have been validated against a server that was never
	// reached, and the health command must not claim otherwise.
	if client.IsJetStreamAvailable() {
		t.Error("IsJetStreamAvailable() = true before ever connecting, want false")
	}
}

// TestNewClientFailsOnBadAuthType checks that the retry behaviour above did not
// also swallow configuration errors. An unreachable server is a condition to wait
// out; a config the client cannot act on at all is not.
func TestNewClientFailsOnBadAuthType(t *testing.T) {
	cfg := unreachableConfig(t)
	cfg.Auth.Type = "not-a-real-auth-type"

	client, err := NewClient(cfg, zap.NewNop())
	if err == nil {
		client.Close()
		t.Fatal("NewClient() returned no error for an invalid auth type")
	}
}

// TestLostStaysOpenOnDeliberateClose guards the distinction the ClosedHandler
// draws. Lost is the agent's signal to exit for a restart, so firing it during
// an orderly shutdown would turn every clean stop into a failure the service
// manager tries to recover from.
func TestLostStaysOpenOnDeliberateClose(t *testing.T) {
	client, err := NewClient(unreachableConfig(t), zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}

	client.Close()

	// The callback runs on nats.go's dispatcher, so give it a moment to be wrong.
	select {
	case <-client.Lost():
		t.Error("Lost() fired for a connection the agent closed itself")
	case <-time.After(200 * time.Millisecond):
	}
}
