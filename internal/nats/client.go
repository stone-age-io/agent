package nats

import (
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"win-agent/internal/config"
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

	// FIXED: Pass all URLs for automatic failover
	serverURLs := strings.Join(cfg.URLs, ",")
	logger.Info("Connecting to NATS", zap.Strings("urls", cfg.URLs))
	conn, err := nats.Connect(serverURLs, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	logger.Info("Connected to NATS",
		zap.String("url", conn.ConnectedUrl()),
		zap.String("server_id", conn.ConnectedServerId()))

	// Create JetStream context for telemetry publishing
	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create JetStream context: %w", err)
	}

	return &Client{
		conn:   conn,
		js:     js,
		logger: logger,
		config: cfg,
	}, nil
}

// PublishTelemetry publishes a message to JetStream with retries (fire-and-forget)
// This is used for heartbeats, metrics, service status, and inventory
func (c *Client) PublishTelemetry(subject string, data []byte) error {
	const maxRetries = 3
	const retryDelay = time.Second

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		_, err := c.js.Publish(subject, data)
		if err == nil {
			c.logger.Debug("Published telemetry",
				zap.String("subject", subject),
				zap.Int("bytes", len(data)),
				zap.Int("attempt", attempt))
			return nil
		}

		lastErr = err
		c.logger.Warn("Failed to publish telemetry",
			zap.String("subject", subject),
			zap.Error(err),
			zap.Int("attempt", attempt),
			zap.Int("max_retries", maxRetries))

		// Don't sleep after last attempt
		if attempt < maxRetries {
			time.Sleep(retryDelay * time.Duration(attempt))
		}
	}

	// Log final failure after all retries
	c.logger.Error("Failed to publish telemetry after all retries",
		zap.String("subject", subject),
		zap.Int("retries", maxRetries),
		zap.Error(lastErr))

	return fmt.Errorf("failed to publish to %s after %d attempts: %w", subject, maxRetries, lastErr)
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
