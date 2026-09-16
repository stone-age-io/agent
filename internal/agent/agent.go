package agent

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/stone-age-io/agent/internal/config"
	"github.com/stone-age-io/agent/internal/edge"
	natsclient "github.com/stone-age-io/agent/internal/nats"
	"github.com/stone-age-io/agent/internal/nebula"
	"github.com/stone-age-io/agent/internal/platform"
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
	nebula    *nebula.Manager // nil unless the overlay is enabled
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

	logger.Info("Starting agent",
		zap.String("version", version),
		zap.String("code", cfg.Code),
		zap.String("location", cfg.Location))

	// Manage NATS credentials through the stone-age.io platform if configured.
	// The nil-able interface values below are assigned only inside this block:
	// handing a typed nil *platform.Client to an interface parameter would make
	// it non-nil, and the rotate command and sync task both test for nil.
	var (
		credsRotator   natsclient.CredsRotator
		credsSyncer    scheduler.CredsSyncer
		nebulaPlatform *platform.Client
	)
	if cfg.NATS.Auth.Type == "platform" {
		platformClient := platform.NewClient(cfg, logger)

		// First boot: fetch the credential before anything tries to connect
		if err := platformClient.EnsureCredentials(); err != nil {
			return nil, fmt.Errorf("failed to bootstrap credentials: %w", err)
		}

		// Every boot: pick up a credential the platform re-minted while this
		// agent was down, and renew the session token. Best-effort on purpose —
		// the credential on disk is usually still good, an unreachable platform
		// must not stop a working agent from starting, and the scheduled sync
		// will retry. This is also the recovery path for a device whose
		// credential was revoked: it exits when NATS rejects it, the service
		// manager restarts it, and this call heals it on the way back up.
		if changed, err := platformClient.Sync(); err != nil {
			logger.Warn("Platform credential sync failed at startup", zap.Error(err))
		} else if changed {
			logger.Info("Adopted re-minted NATS credentials from platform")
		}

		credsRotator = platformClient
		credsSyncer = platformClient
		nebulaPlatform = platformClient
	}

	// The embedded overlay, if it is enabled. Built before NATS on purpose: with
	// nats.urls pointing at an overlay address, the bus is not reachable until
	// this is up. Neither waits for the other — NATS retries in the background —
	// but there is no reason to make it retry for longer than necessary.
	var nebulaManager *nebula.Manager
	if cfg.Nebula.Enabled {
		source, err := nebulaSource(cfg, nebulaPlatform)
		if err != nil {
			return nil, err
		}

		nebulaManager = nebula.New(nebula.Options{
			Source:        source,
			CacheFile:     cfg.Nebula.CacheFile,
			VerifyTimeout: cfg.Nebula.VerifyTimeout,
			Version:       version,
			Logger:        logger,
		})

		// Best-effort, like the credential sync above. An overlay that cannot come
		// up is a serious problem, but it is not a reason to refuse to start: an
		// agent that still has NATS on the underlay is the one that can be asked
		// what went wrong, and the scheduled sync retries.
		if err := nebulaManager.Start(); err != nil {
			logger.Error("Nebula failed to start", zap.Error(err))
		}
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
	handlers := natsclient.NewCommandHandlers(logger, cfg, executor, natsClient, version, credsRotator, nebulaController(nebulaManager))

	// Subscribe to commands
	logger.Info("Subscribing to commands...")
	if err := handlers.SubscribeAll(natsClient); err != nil {
		cancel() // ADDED: Cancel context on error
		natsClient.Close()
		return nil, fmt.Errorf("failed to subscribe to commands: %w", err)
	}

	// Create and start scheduler
	logger.Info("Starting scheduler...")
	sched, err := scheduler.New(logger, natsClient, executor, cfg, version, credsSyncer, nebulaSyncer(nebulaManager), ctx)
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
		nebula:    nebulaManager,
		version:   version,
		ctx:       ctx,    // ADDED: Store context
		cancel:    cancel, // ADDED: Store cancel function
	}, nil
}

// Run starts the agent and blocks until shutdown
func (a *Agent) Run() error {
	// Start the scheduler
	a.scheduler.Start()

	// The edge subsystems, on a box that hosts a NATS leaf or relays twin state.
	// Started like the Nebula manager and stopped by the same cancel: it owns no
	// signal handling and no ticker of its own, because this function already
	// has both.
	//
	// Best-effort, for the same reason the overlay is. A gateway whose local bus
	// will not come up is a serious problem, and an agent that refused to start
	// over it would be one you could not ask what went wrong.
	if edgeEnabled(a.config) {
		go func() {
			if err := edge.Run(a.ctx, edgeConfig(a.config), a.version); err != nil {
				a.logger.Error("Edge subsystems stopped", zap.Error(err))
			}
		}()
	}

	a.logger.Info("Agent running",
		zap.String("code", a.config.Code),
		zap.String("version", a.version))

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// runErr is non-nil only when the agent is stopping because something went
	// wrong, which is what tells main to exit non-zero and the service manager to
	// restart us. A signal or a cancelled context is an orderly stop.
	var runErr error

	select {
	case <-sigChan:
		a.logger.Info("Received shutdown signal")

	case <-a.ctx.Done():
		a.logger.Info("Context cancelled")

	// nats.go has given up on the connection for good — a revoked credential is
	// the usual way that happens. There is no path back to the bus from here, and
	// a restart is what re-runs the startup credential sync that can heal it. See
	// the ClosedHandler in internal/nats/client.go, and the OnFailure options in
	// cmd/agent/main.go that make the restart actually happen.
	case <-a.nats.Lost():
		a.logger.Error("NATS connection lost permanently, exiting for restart")
		runErr = fmt.Errorf("NATS connection closed permanently")
	}

	if err := a.Shutdown(); err != nil {
		return err
	}

	return runErr
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

	// Stop the overlay last: NATS may have been riding on it, so draining above
	// had to happen while the tunnel was still up.
	if a.nebula != nil {
		a.nebula.Stop()
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
		MaxSize:    cfg.MaxSizeMB, // megabytes
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

// nebulaSource picks where the Nebula config comes from.
//
// config.validate has already established that a platform source implies
// stone-age auth, so a nil client here would be a programming error rather than
// a misconfiguration — but it is cheap to say so plainly instead of panicking.
func nebulaSource(cfg *config.Config, client *platform.Client) (nebula.Source, error) {
	switch cfg.Nebula.Source {
	case "platform":
		if client == nil {
			return nil, fmt.Errorf("nebula.source is \"platform\" but no platform client was built (nats.auth.type must be stone-age)")
		}
		return client.NebulaSource(), nil
	case "file":
		return nebula.FileSource{Path: cfg.Nebula.ConfigFile}, nil
	default:
		return nil, fmt.Errorf("unknown nebula.source %q", cfg.Nebula.Source)
	}
}

// nebulaController and nebulaSyncer convert a possibly-nil *nebula.Manager into
// a genuinely nil interface.
//
// A typed nil pointer assigned to an interface is not nil, and both consumers
// test for nil to decide whether the feature exists at all — the same trap the
// credential interfaces above are arranged to avoid.
func nebulaController(m *nebula.Manager) natsclient.NebulaController {
	if m == nil {
		return nil
	}
	return m
}

func nebulaSyncer(m *nebula.Manager) scheduler.NebulaSyncer {
	if m == nil {
		return nil
	}
	return m
}
