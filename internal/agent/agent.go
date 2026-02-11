package agent

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/stone-age-io/agent/internal/bootstrap"
	"github.com/stone-age-io/agent/internal/config"
	natsclient "github.com/stone-age-io/agent/internal/nats"
	"github.com/stone-age-io/agent/internal/scheduler"
	"github.com/stone-age-io/agent/internal/tasks"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Agent represents the main agent
type Agent struct {
	config    *config.Config
	logger    *zap.Logger
	nats      *natsclient.Client
	scheduler *scheduler.Scheduler
	handlers  *natsclient.CommandHandlers
	version   string
	ctx       context.Context    // ADDED: Root context for clean shutdown
	cancel    context.CancelFunc // ADDED: Cancel function for shutdown
}

// New creates a new agent instance
func New(configPath string, version string) (*Agent, error) {
	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize logger
	logger, err := initLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	logger.Info("Starting win-agent",
		zap.String("version", version),
		zap.String("device_id", cfg.DeviceID))

	// Bootstrap NATS credentials from PocketBase if configured
	if cfg.NATS.Auth.Type == "pocketbase" {
		if err := bootstrap.FetchCredentials(cfg, logger); err != nil {
			return nil, fmt.Errorf("failed to bootstrap credentials: %w", err)
		}
		// Switch auth type to creds for the NATS client — .creds file now exists
		cfg.NATS.Auth.Type = "creds"
	}

	// Create root context with cancellation
	ctx, cancel := context.WithCancel(context.Background())

	// Create task executor with command timeout, context, and metrics source config
	executor, err := tasks.NewExecutor(
		logger,
		cfg.Commands.Timeout,
		ctx,
		cfg.Tasks.SystemMetrics.Source,
		cfg.Tasks.SystemMetrics.ExporterURL,
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create executor: %w", err)
	}

	// Connect to NATS
	logger.Info("Connecting to NATS...")
	natsClient, err := natsclient.NewClient(&cfg.NATS, logger)
	if err != nil {
		cancel() // ADDED: Cancel context on error
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	// Create command handlers (now with NATS client for health checks and version)
	handlers := natsclient.NewCommandHandlers(logger, cfg, executor, natsClient, version)

	// Subscribe to commands
	logger.Info("Subscribing to commands...")
	if err := handlers.SubscribeAll(natsClient); err != nil {
		cancel() // ADDED: Cancel context on error
		natsClient.Close()
		return nil, fmt.Errorf("failed to subscribe to commands: %w", err)
	}

	// Create and start scheduler
	logger.Info("Starting scheduler...")
	sched, err := scheduler.New(logger, natsClient, executor, cfg, version, ctx)
	if err != nil {
		cancel() // ADDED: Cancel context on error
		natsClient.Close()
		return nil, fmt.Errorf("failed to create scheduler: %w", err)
	}

	return &Agent{
		config:    cfg,
		logger:    logger,
		nats:      natsClient,
		scheduler: sched,
		handlers:  handlers,
		version:   version,
		ctx:       ctx,    // ADDED: Store context
		cancel:    cancel, // ADDED: Store cancel function
	}, nil
}

// Run starts the agent and blocks until shutdown
func (a *Agent) Run() error {
	// Start the scheduler
	a.scheduler.Start()

	a.logger.Info("Agent running",
		zap.String("device_id", a.config.DeviceID),
		zap.String("version", a.version))

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case <-sigChan:
		a.logger.Info("Received shutdown signal")
	case <-a.ctx.Done():
		a.logger.Info("Context cancelled")
	}

	return a.Shutdown()
}

// Shutdown gracefully shuts down the agent
func (a *Agent) Shutdown() error {
	a.logger.Info("Shutting down agent gracefully")

	// ADDED: Cancel context to signal all operations to stop
	a.cancel()

	// Stop accepting new scheduled tasks
	if err := a.scheduler.Shutdown(); err != nil {
		a.logger.Error("Error shutting down scheduler", zap.Error(err))
	}

	// MODIFIED: Use context for drain timeout
	drainCtx, drainCancel := context.WithTimeout(context.Background(), a.config.NATS.DrainTimeout)
	defer drainCancel()

	// Drain NATS connection (wait for in-flight messages)
	if err := a.nats.Drain(drainCtx); err != nil {
		a.logger.Error("Error draining NATS", zap.Error(err))
	}

	// Sync logger
	a.logger.Sync()

	a.logger.Info("Agent shutdown complete")
	return nil
}

// initLogger creates and configures the logger with log rotation
func initLogger(cfg config.LoggingConfig) (*zap.Logger, error) {
	// Parse log level
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, fmt.Errorf("invalid log level: %w", err)
	}

	// Create encoder config
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	// Create encoder for JSON logging
	fileEncoder := zapcore.NewJSONEncoder(encoderConfig)

	// Setup log rotation with lumberjack
	fileWriter := &lumberjack.Logger{
		Filename:   cfg.File,
		MaxSize:    cfg.MaxSizeMB,  // megabytes
		MaxBackups: cfg.MaxBackups,
		MaxAge:     28, // days
		Compress:   true,
	}

	// Create console encoder for stdout (during development/debugging)
	consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)

	// Create multi-writer core (file with rotation + console)
	core := zapcore.NewTee(
		zapcore.NewCore(fileEncoder, zapcore.AddSync(fileWriter), level),
		zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), level),
	)

	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	return logger, nil
}
