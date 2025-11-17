package nats

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
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
}

// NewClient creates a new NATS client with the specified configuration
func NewClient(cfg *config.NATSConfig, logger *zap.Logger) (*Client, error) {
	opts := []nats.Option{
		nats.Name("win-agent"),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			if err != nil {
				logger.Warn("NATS disconnected", zap.Error(err))
			} else {
				logger.Info("NATS disconnected")
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("NATS reconnected", zap.String("url", nc.ConnectedUrl()))
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			logger.Info("NATS connection closed")
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
	case "creds":
		logger.Info("Using credentials file authentication", zap.String("file", cfg.Auth.CredsFile))
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
	conn, err := nats.Connect(serverURLs, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	logger.Info("Connected to NATS",
		zap.String("url", conn.ConnectedUrl()),
		zap.String("server_id", conn.ConnectedServerId()),
		zap.Bool("tls", conn.TLSRequired()))

	// Create JetStream context for telemetry publishing
	logger.Info("Creating JetStream context...")
	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create JetStream context: %w", err)
	}

	// CRITICAL: Validate JetStream is actually enabled on the server
	// This prevents cryptic failures when trying to publish telemetry later
	// Fail fast here rather than silently failing on first heartbeat
	logger.Info("Validating JetStream availability...")
	_, err = js.AccountInfo()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("JetStream not available on NATS server (is JetStream enabled?): %w", err)
	}

	logger.Info("JetStream validated successfully")

	return &Client{
		conn:   conn,
		js:     js,
		logger: logger,
		config: cfg,
	}, nil
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

// PublishTelemetry publishes a message to JetStream asynchronously (fire-and-forget)
// This is used for heartbeats, metrics, service status, and inventory
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
func (c *Client) Drain(timeout time.Duration) error {
	c.logger.Info("Draining NATS connection", zap.Duration("timeout", timeout))

	// Check if connection is already closed
	if !c.conn.IsConnected() && c.conn.IsClosed() {
		c.logger.Info("Connection already closed")
		return nil
	}

	// Create a channel to receive drain completion or error
	drainDone := make(chan error, 1)

	// Start drain in goroutine
	go func() {
		drainDone <- c.conn.Drain()
	}()

	// Wait for drain to complete or timeout
	select {
	case err := <-drainDone:
		if err != nil {
			c.logger.Error("Error during NATS drain", zap.Error(err))
			return err
		}
		c.logger.Info("NATS drain completed successfully")
		return nil

	case <-time.After(timeout):
		c.logger.Warn("NATS drain timeout, forcing close")
		c.conn.Close()
		return fmt.Errorf("drain timeout after %v", timeout)
	}
}

// Close immediately closes the NATS connection
func (c *Client) Close() {
	c.logger.Info("Closing NATS connection")
	c.conn.Close()
}

// IsConnected returns true if the NATS connection is currently active
func (c *Client) IsConnected() bool {
	return c.conn.IsConnected()
}

// Stats returns connection statistics
func (c *Client) Stats() nats.Statistics {
	return c.conn.Stats()
}
