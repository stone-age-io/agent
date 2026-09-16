package nats

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stone-age-io/agent/internal/config"
	"go.uber.org/zap"
)

// Client manages the NATS connection and provides methods for publishing and subscribing
type Client struct {
	conn   *nats.Conn
	js     nats.JetStreamContext
	logger *zap.Logger
	config *config.NATSConfig

	// jsAvailable records whether the last JetStream check against the connected
	// server succeeded. It is written from the connect/reconnect callbacks and
	// read by the health command, hence atomic.
	jsAvailable atomic.Bool

	// closing distinguishes a connection we closed on purpose from one nats.go
	// gave up on. lost is closed exactly once, in the latter case only.
	closing  atomic.Bool
	lostOnce sync.Once
	lost     chan struct{}
}

// NewClient creates a new NATS client with the specified configuration
func NewClient(cfg *config.NATSConfig, logger *zap.Logger) (*Client, error) {
	// The client exists before the connection does, because the connect and
	// reconnect callbacks below close over it.
	c := &Client{
		logger: logger,
		config: cfg,
		lost:   make(chan struct{}),
	}

	opts := []nats.Option{
		nats.Name("win-agent"),

		// RETRY RATHER THAN REFUSE TO START. Without this, nats.Connect fails
		// immediately when nothing is listening and the agent exits before it has
		// done anything else. That was tolerable while NATS was the agent's only
		// external dependency and the service manager could restart the process.
		//
		// It stops being tolerable as soon as the agent owns something besides
		// itself. An agent whose NATS endpoint lives on a Nebula overlay cannot
		// reach the bus until the overlay is up, and the overlay cannot come up if
		// the process exits first — a deadlock by structure rather than by timing.
		// With retry on, the two are independent: whichever becomes reachable
		// first waits for the other, and neither restarts the process to do it.
		//
		// Configuration errors below (unreadable TLS material, an unknown auth
		// type) still fail fast. Unreachability is not a configuration error.
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			c.jsAvailable.Store(false)
			if err != nil {
				logger.Warn("NATS disconnected", zap.Error(err))
			} else {
				logger.Info("NATS disconnected")
			}
		}),
		nats.ConnectHandler(func(nc *nats.Conn) {
			logger.Info("Connected to NATS",
				zap.String("url", nc.ConnectedUrl()),
				zap.String("server_id", nc.ConnectedServerId()),
				zap.Bool("tls", nc.TLSRequired()))
			go c.checkJetStream(nc)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("NATS reconnected", zap.String("url", nc.ConnectedUrl()))
			go c.checkJetStream(nc)
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			if c.closing.Load() {
				logger.Info("NATS connection closed")
				return
			}

			// nats.go has stopped trying. The usual cause is an authorization
			// error repeated on reconnect — which is what a revoked credential
			// looks like from here — since nats.go aborts reconnection after two
			// consecutive auth failures against the same server.
			//
			// Before RetryOnFailedConnect, a revoked credential failed the initial
			// nats.Connect, which failed agent.New, which exited the process; the
			// service manager restarted it and the startup credential sync healed
			// it on the way back up. Retrying on failed connect removed that exit
			// without removing the need for it, and an agent sitting on a
			// permanently closed connection is a zombie: the scheduler keeps
			// firing, every publish fails, and nothing ever reconnects.
			//
			// So say so, and let Run exit for the same reason it always did.
			logger.Error("NATS connection closed permanently, agent will exit for restart")
			c.lostOnce.Do(func() { close(c.lost) })
		}),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			logger.Error("NATS error",
				zap.Error(err),
				zap.String("subject", sub.Subject))
		}),
	}

	// Configure TLS if enabled
	if cfg.TLS.Enabled {
		tlsConfig, err := createTLSConfig(&cfg.TLS, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create TLS config: %w", err)
		}

		opts = append(opts, nats.Secure(tlsConfig))
		logger.Info("TLS enabled for NATS connection",
			zap.Bool("client_cert", cfg.TLS.CertFile != ""),
			zap.Bool("ca_cert", cfg.TLS.CAFile != ""),
			zap.Bool("skip_verify", cfg.TLS.InsecureSkipVerify))

		// Warn if insecure skip verify is enabled
		if cfg.TLS.InsecureSkipVerify {
			logger.Warn("TLS certificate verification is DISABLED - this is insecure and should only be used in development")
		}
	}

	// Add authentication based on config type
	switch cfg.Auth.Type {
	// stone-age auth is creds auth once the platform has put the file in place:
	// the agent connects with the .creds file either way. Keeping it a distinct
	// case rather than rewriting the config's auth type preserves the fact that
	// this agent manages its credentials through the platform, which is what the
	// rotate command and the creds_sync task key off.
	case "creds", "platform":
		logger.Info("Using credentials file authentication", zap.String("file", cfg.Auth.CredsFile))
		// UserCredentials re-reads the file on every connect and reconnect, so a
		// credential replaced at runtime takes effect on the next reconnect
		opts = append(opts, nats.UserCredentials(cfg.Auth.CredsFile))
	case "token":
		logger.Info("Using token authentication")
		opts = append(opts, nats.Token(cfg.Auth.Token))
	case "userpass":
		logger.Info("Using username/password authentication", zap.String("username", cfg.Auth.Username))
		opts = append(opts, nats.UserInfo(cfg.Auth.Username, cfg.Auth.Password))
	case "none":
		logger.Info("Using no authentication")
	default:
		return nil, fmt.Errorf("invalid auth type: %s", cfg.Auth.Type)
	}

	// Pass all URLs for automatic failover
	serverURLs := strings.Join(cfg.URLs, ",")
	logger.Info("Connecting to NATS", zap.Strings("urls", cfg.URLs))
	// With RetryOnFailedConnect set, this returns a connection that may still be
	// reconnecting in the background; an error here means the options themselves
	// are unusable, not that the server is down. Subscriptions and publishes are
	// both legal on a connection in that state — nats.go buffers them and replays
	// subscriptions once it connects — so nothing below needs to wait.
	conn, err := nats.Connect(serverURLs, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}
	c.conn = conn

	// Creating the context performs no I/O, so it is safe before the connection
	// is established.
	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create JetStream context: %w", err)
	}
	c.js = js

	return c, nil
}

// checkJetStream verifies that JetStream is reachable on the server the client
// has just connected to, and records the answer for the health command.
//
// This used to run once, inline in NewClient, and a failure aborted startup. That
// was the only way to turn "JetStream is not enabled for this account" into a
// loud error rather than a telemetry stream that silently goes nowhere — but it
// also meant an unreachable server prevented the agent from starting at all.
// Running it on every connect keeps the loud error, reports it more than once,
// and costs nothing when the server is fine.
//
// It runs in its own goroutine because nats.go dispatches these callbacks on a
// single shared goroutine, and AccountInfo is a round trip: doing it inline would
// stall every other callback for the length of that request.
func (c *Client) checkJetStream(nc *nats.Conn) {
	js, err := nc.JetStream()
	if err != nil {
		c.jsAvailable.Store(false)
		c.logger.Error("Failed to create JetStream context", zap.Error(err))
		return
	}

	if _, err := js.AccountInfo(); err != nil {
		c.jsAvailable.Store(false)
		c.logger.Error("JetStream not available on NATS server (is JetStream enabled for this account?)",
			zap.Error(err),
			zap.String("url", nc.ConnectedUrl()))
		return
	}

	c.jsAvailable.Store(true)
	c.logger.Info("JetStream validated", zap.String("url", nc.ConnectedUrl()))
}

// IsJetStreamAvailable reports whether the last JetStream check succeeded. It is
// false whenever the client is disconnected.
func (c *Client) IsJetStreamAvailable() bool {
	return c.jsAvailable.Load()
}

// Lost is closed when nats.go gives up on the connection for good. It never
// fires for a connection the agent closed itself, so a receive on it means the
// process has no way back to the bus and should exit to be restarted.
func (c *Client) Lost() <-chan struct{} {
	return c.lost
}

// createTLSConfig creates a TLS configuration based on the provided settings
func createTLSConfig(cfg *config.TLSConfig, logger *zap.Logger) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12, // Enforce TLS 1.2 minimum for security
	}

	// Configure certificate verification
	if cfg.InsecureSkipVerify {
		tlsConfig.InsecureSkipVerify = true
	}

	// Load CA certificate if provided
	// This is used to verify the server's certificate
	if cfg.CAFile != "" {
		logger.Info("Loading CA certificate", zap.String("file", cfg.CAFile))

		caCert, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}

		tlsConfig.RootCAs = caCertPool
		logger.Debug("CA certificate loaded successfully")
	}

	// Load client certificate and key if provided
	// This is used for mutual TLS authentication
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		logger.Info("Loading client certificate",
			zap.String("cert", cfg.CertFile),
			zap.String("key", cfg.KeyFile))

		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %w", err)
		}

		tlsConfig.Certificates = []tls.Certificate{cert}
		logger.Debug("Client certificate loaded successfully")
	}

	return tlsConfig, nil
}

// Publish sends a message over core NATS (no JetStream, fire-and-forget).
// This is used for heartbeats: a missed beat IS the signal consumers care
// about, so durability/replay would be actively harmful — a reconnecting
// agent should not deliver a backlog of stale liveness beacons. This matches
// the heartbeat semantics of the other stone-age.io applications.
func (c *Client) Publish(subject string, data []byte) error {
	if err := c.conn.Publish(subject, data); err != nil {
		c.logger.Warn("Failed to publish message",
			zap.String("subject", subject),
			zap.Error(err))
		return fmt.Errorf("failed to publish to %s: %w", subject, err)
	}

	c.logger.Debug("Published message",
		zap.String("subject", subject),
		zap.Int("bytes", len(data)))
	return nil
}

// PublishTelemetry publishes a message to JetStream asynchronously (fire-and-forget)
// This is used for metrics, service status, and inventory
// Uses PublishAsync for better performance and built-in retry handling
func (c *Client) PublishTelemetry(subject string, data []byte) error {
	// PublishAsync returns a PubAckFuture immediately (non-blocking)
	// The actual publish happens in the background with automatic retries
	pubAckFuture, err := c.js.PublishAsync(subject, data)
	if err != nil {
		// This only fails if we can't queue the message (very rare)
		c.logger.Error("Failed to queue telemetry publish",
			zap.String("subject", subject),
			zap.Error(err))
		return fmt.Errorf("failed to queue publish to %s: %w", subject, err)
	}

	// Handle the acknowledgment asynchronously
	// This doesn't block the caller - it runs in a goroutine managed by NATS
	go func() {
		select {
		case <-pubAckFuture.Ok():
			// Message was acknowledged by JetStream
			c.logger.Debug("Published telemetry",
				zap.String("subject", subject),
				zap.Int("bytes", len(data)))

		case err := <-pubAckFuture.Err():
			// Publication failed after retries
			// Log but don't crash - telemetry is fire-and-forget
			c.logger.Warn("Failed to publish telemetry after retries",
				zap.String("subject", subject),
				zap.Error(err))
		}
	}()

	return nil
}

// PublishTelemetrySync is a synchronous version for cases where you need to know
// if the publish succeeded (e.g., during shutdown or critical operations)
func (c *Client) PublishTelemetrySync(subject string, data []byte, timeout time.Duration) error {
	pubAckFuture, err := c.js.PublishAsync(subject, data)
	if err != nil {
		return fmt.Errorf("failed to queue publish to %s: %w", subject, err)
	}

	// Wait for acknowledgment with timeout
	select {
	case <-pubAckFuture.Ok():
		c.logger.Debug("Published telemetry (sync)",
			zap.String("subject", subject),
			zap.Int("bytes", len(data)))
		return nil

	case err := <-pubAckFuture.Err():
		c.logger.Error("Failed to publish telemetry (sync)",
			zap.String("subject", subject),
			zap.Error(err))
		return fmt.Errorf("failed to publish to %s: %w", subject, err)

	case <-time.After(timeout):
		return fmt.Errorf("publish timeout after %v", timeout)
	}
}

// Subscribe creates a subscription to the specified subject
// This is used for command handlers with Core NATS request/reply
func (c *Client) Subscribe(subject string, handler nats.MsgHandler) (*nats.Subscription, error) {
	sub, err := c.conn.Subscribe(subject, handler)
	if err != nil {
		c.logger.Error("Failed to subscribe",
			zap.String("subject", subject),
			zap.Error(err))
		return nil, fmt.Errorf("failed to subscribe to %s: %w", subject, err)
	}

	c.logger.Info("Subscribed to subject", zap.String("subject", subject))
	return sub, nil
}

// Drain gracefully closes the connection by draining all subscriptions
// and waiting for in-flight messages to complete
// MODIFIED: Now accepts context for cancellation
func (c *Client) Drain(ctx context.Context) error {
	c.logger.Info("Draining NATS connection")
	c.closing.Store(true)

	// Check if connection is already closed
	if c.conn.IsClosed() {
		c.logger.Info("Connection already closed")
		return nil
	}

	// Create a channel to receive drain completion or error
	drainDone := make(chan error, 1)

	// Start drain in goroutine
	go func() {
		drainDone <- c.conn.Drain()
	}()

	// Wait for drain to complete, timeout, or context cancellation
	select {
	case err := <-drainDone:
		if err != nil {
			c.logger.Error("Error during NATS drain", zap.Error(err))
			return err
		}
		c.logger.Info("NATS drain completed successfully")
		return nil

	case <-ctx.Done():
		c.logger.Warn("NATS drain cancelled by context, forcing close")
		c.conn.Close()
		return fmt.Errorf("drain cancelled: %w", ctx.Err())
	}
}

// Close immediately closes the NATS connection
func (c *Client) Close() {
	c.logger.Info("Closing NATS connection")
	c.closing.Store(true)
	c.conn.Close()
}

// IsConnected returns true if the NATS connection is currently active
func (c *Client) IsConnected() bool {
	return c.conn.IsConnected()
}

// Flush waits for the server to acknowledge everything already published.
// Callers that are about to drop the connection use this to make sure a reply
// they just sent actually left.
func (c *Client) Flush() error {
	return c.conn.Flush()
}

// ForceReconnect drops the current connection and reconnects, keeping existing
// subscriptions (the client re-sends them on reconnect).
//
// This is how a freshly written .creds file takes effect without restarting the
// agent: UserCredentials reads the file on every connect, so the reconnect picks
// up whatever is on disk now.
func (c *Client) ForceReconnect() error {
	c.logger.Info("Forcing NATS reconnect to pick up new credentials")
	return c.conn.ForceReconnect()
}

// Stats returns connection statistics
func (c *Client) Stats() nats.Statistics {
	return c.conn.Stats()
}
