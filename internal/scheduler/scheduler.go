package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/stone-age-io/agent/internal/config"
	natsclient "github.com/stone-age-io/agent/internal/nats"
	"github.com/stone-age-io/agent/internal/tasks"
	"github.com/stone-age-io/agent/internal/utils"
	"go.uber.org/zap"
)

// CredsSyncer renews the platform session and adopts the credential the platform
// currently holds for this agent, reporting whether the file on disk changed.
//
// Declared here rather than imported so this package stays clear of the
// platform's HTTP concerns. Implemented by *platform.Client.
type CredsSyncer interface {
	Sync() (bool, error)
}

// NebulaSyncer adopts the Nebula config the platform currently holds for this
// agent, reporting whether anything was applied.
//
// Declared here for the same reason as CredsSyncer. Implemented by
// *nebula.Manager.
type NebulaSyncer interface {
	Sync() (bool, error)
}

// Scheduler manages periodic task execution
type Scheduler struct {
	scheduler     gocron.Scheduler
	logger        *zap.Logger
	nats          *natsclient.Client
	executor      *tasks.Executor
	config        *config.Config
	version       string
	subjectPrefix string
	credsSyncer   CredsSyncer     // nil unless this agent gets its credentials from the platform
	nebulaSyncer  NebulaSyncer    // nil unless the embedded overlay is enabled
	ctx           context.Context // ADDED: Context for cancellation
}

// New creates a new scheduler with configured tasks
// MODIFIED: Now accepts context for cancellation
// credsSyncer may be nil, in which case no credential sync task is scheduled.
func New(
	logger *zap.Logger,
	natsClient *natsclient.Client,
	executor *tasks.Executor,
	cfg *config.Config,
	version string,
	credsSyncer CredsSyncer,
	nebulaSyncer NebulaSyncer,
	ctx context.Context,
) (*Scheduler, error) {
	// Create gocron scheduler
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("failed to create scheduler: %w", err)
	}

	scheduler := &Scheduler{
		scheduler:     s,
		logger:        logger,
		nats:          natsClient,
		nebulaSyncer:  nebulaSyncer,
		executor:      executor,
		config:        cfg,
		version:       version,
		subjectPrefix: cfg.SubjectPrefix,
		credsSyncer:   credsSyncer,
		ctx:           ctx, // ADDED: Store context
	}

	// Schedule tasks based on configuration
	if err := scheduler.scheduleTasks(); err != nil {
		return nil, fmt.Errorf("failed to schedule tasks: %w", err)
	}

	return scheduler, nil
}

// wrapTaskWithRecovery wraps a task function with panic recovery AND context checking
// MODIFIED: Now checks context before execution
func (s *Scheduler) wrapTaskWithRecovery(taskName string, taskFunc func()) func() {
	return func() {
		// ADDED: Check if context is cancelled before executing
		select {
		case <-s.ctx.Done():
			s.logger.Debug("Skipping task execution due to shutdown",
				zap.String("task", taskName))
			return
		default:
			// Continue with task execution
		}

		defer func() {
			if r := recover(); r != nil {
				// Log the panic with stack trace
				s.logger.Error("Panic recovered in scheduled task",
					zap.String("task", taskName),
					zap.Any("panic", r),
					zap.String("stack", string(debug.Stack())))
			}
		}()

		// Execute the actual task
		taskFunc()
	}
}

// scheduleTasks sets up all periodic tasks
func (s *Scheduler) scheduleTasks() error {
	code := s.config.Code

	// If metrics are enabled, establish baseline with retries
	// This is critical for counter-based metrics (CPU, disk I/O)
	if s.config.Tasks.SystemMetrics.Enabled {
		s.logger.Info("Establishing metrics baseline")

		const maxRetries = 3
		const retryDelay = 2 * time.Second

		var baselineErr error
		for attempt := 1; attempt <= maxRetries; attempt++ {
			// ADDED: Check context before retry
			select {
			case <-s.ctx.Done():
				return fmt.Errorf("shutdown during baseline establishment")
			default:
			}

			_, err := s.executor.ScrapeMetrics()
			if err == nil {
				s.logger.Info("Metrics baseline established successfully")
				baselineErr = nil
				break
			}

			baselineErr = err
			s.logger.Warn("Failed to establish metrics baseline",
				zap.Error(err),
				zap.Int("attempt", attempt),
				zap.Int("max_retries", maxRetries))

			// Don't sleep after last attempt
			if attempt < maxRetries {
				s.logger.Info("Retrying baseline in 2 seconds...",
					zap.Int("attempt", attempt),
					zap.Int("max", maxRetries))

				// ADDED: Use context-aware sleep
				select {
				case <-s.ctx.Done():
					return fmt.Errorf("shutdown during baseline retry delay")
				case <-time.After(retryDelay):
				}
			}
		}

		// Warn if baseline failed after all retries
		if baselineErr != nil {
			s.logger.Warn("Could not establish metrics baseline after retries",
				zap.Error(baselineErr),
				zap.String("impact", "First metrics publish will be incomplete (no CPU or disk I/O rates)"))
			// Continue anyway - subsequent scrapes will establish the baseline
		}
	}

	// Schedule heartbeat task WITH PANIC RECOVERY AND CONTEXT CHECK
	if s.config.Tasks.Heartbeat.Enabled {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.Heartbeat.Interval),
			gocron.NewTask(s.wrapTaskWithRecovery("heartbeat", func() {
				s.publishHeartbeat(code)
			})),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule heartbeat: %w", err)
		}
		s.logger.Info("Scheduled heartbeat task",
			zap.Duration("interval", s.config.Tasks.Heartbeat.Interval))
	}

	// Schedule system metrics task WITH PANIC RECOVERY AND CONTEXT CHECK
	if s.config.Tasks.SystemMetrics.Enabled {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.SystemMetrics.Interval),
			gocron.NewTask(s.wrapTaskWithRecovery("metrics", func() {
				s.publishMetrics(code)
			})),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule metrics: %w", err)
		}
		s.logger.Info("Scheduled metrics task",
			zap.Duration("interval", s.config.Tasks.SystemMetrics.Interval))
	}

	// Schedule service check task WITH PANIC RECOVERY AND CONTEXT CHECK
	if s.config.Tasks.ServiceCheck.Enabled {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.ServiceCheck.Interval),
			gocron.NewTask(s.wrapTaskWithRecovery("service_check", func() {
				s.publishServiceStatus(code)
			})),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule service check: %w", err)
		}
		s.logger.Info("Scheduled service check task",
			zap.Duration("interval", s.config.Tasks.ServiceCheck.Interval))
	}

	// Schedule inventory task WITH PANIC RECOVERY AND CONTEXT CHECK (but run it once immediately first)
	if s.config.Tasks.Inventory.Enabled {
		// Run immediately on startup (wrapped with panic recovery)
		startupTask := s.wrapTaskWithRecovery("inventory_startup", func() {
			s.publishInventory(code)
		})
		go startupTask()

		// Then schedule for periodic execution
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.Inventory.Interval),
			gocron.NewTask(s.wrapTaskWithRecovery("inventory", func() {
				s.publishInventory(code)
			})),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule inventory: %w", err)
		}
		s.logger.Info("Scheduled inventory task",
			zap.Duration("interval", s.config.Tasks.Inventory.Interval))
	}

	// Schedule credential sync WITH PANIC RECOVERY AND CONTEXT CHECK.
	// Only for platform-managed agents — credsSyncer is nil for every other auth
	// type, and the startup sync in agent.New has already run one pass.
	if s.credsSyncer != nil && s.config.Platform.SyncInterval > 0 {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Platform.SyncInterval),
			gocron.NewTask(s.wrapTaskWithRecovery("creds_sync", s.syncCredentials)),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule credential sync: %w", err)
		}
		s.logger.Info("Scheduled credential sync task",
			zap.Duration("interval", s.config.Platform.SyncInterval))
	}

	// Schedule the Nebula config sync WITH PANIC RECOVERY AND CONTEXT CHECK.
	// Only when the overlay is enabled; nebulaSyncer is nil otherwise.
	//
	// This interval is the revocation latency for this device: Nebula refuses a
	// blocklisted certificate only once the peer has re-read its config, and this
	// is what re-reads it. See config.validateNebula for the bounds.
	if s.nebulaSyncer != nil {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Nebula.SyncInterval),
			gocron.NewTask(s.wrapTaskWithRecovery("nebula_sync", s.syncNebula)),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule nebula sync: %w", err)
		}
		s.logger.Info("Scheduled nebula config sync task",
			zap.Duration("interval", s.config.Nebula.SyncInterval))
	}

	return nil
}

// Start begins executing scheduled tasks
func (s *Scheduler) Start() {
	s.scheduler.Start()
	s.logger.Info("Scheduler started")
}

// Shutdown gracefully stops the scheduler
func (s *Scheduler) Shutdown() error {
	s.logger.Info("Shutting down scheduler")
	return s.scheduler.Shutdown()
}

// publishHeartbeat publishes a heartbeat message over core NATS.
// Heartbeats are deliberately NOT JetStream: a missed beat is the signal
// consumers care about, so last-write-wins semantics are correct and a
// backlog of stale beats after a reconnect would be actively harmful.
func (s *Scheduler) publishHeartbeat(code string) {
	select {
	case <-s.ctx.Done():
		return
	default:
	}

	subject := fmt.Sprintf("%s.%s.heartbeat", s.subjectPrefix, code)

	heartbeat := s.executor.CreateHeartbeat(code, s.config.Location)
	data, err := json.Marshal(heartbeat)
	if err != nil {
		s.logger.Error("Failed to marshal heartbeat", zap.Error(err))
		return
	}

	if err := s.nats.Publish(subject, data); err != nil {
		// Fire-and-forget: log and let the next tick retry
		s.logger.Error("Failed to publish heartbeat", zap.Error(err))
		return
	}

	// Record successful execution
	s.executor.RecordHeartbeat()
}

// syncCredentials renews the platform session token and adopts a credential the
// platform has re-minted — an operator pressing Regenerate, or a rotation
// triggered from elsewhere in the fleet.
//
// It is also how a device with rejected credentials heals itself: this path never
// touches NATS, so it keeps working while the NATS connection does not.
//
// Failures are warnings. The credential on disk is usually still good, the next
// run will retry, and the platform being unreachable is not the agent's problem
// to escalate.
func (s *Scheduler) syncCredentials() {
	select {
	case <-s.ctx.Done():
		return
	default:
	}

	changed, err := s.credsSyncer.Sync()
	if err != nil {
		s.logger.Warn("Platform credential sync failed", zap.Error(err))
		return
	}

	if !changed {
		s.logger.Debug("Platform credential sync: credentials unchanged")
		return
	}

	s.logger.Info("NATS credentials updated from platform, reconnecting")
	if err := s.nats.ForceReconnect(); err != nil {
		s.logger.Error("Failed to reconnect with updated credentials", zap.Error(err))
	}
}

// publishMetrics scrapes and publishes system metrics
func (s *Scheduler) publishMetrics(code string) {
	select {
	case <-s.ctx.Done():
		return
	default:
	}

	subject := fmt.Sprintf("%s.%s.telemetry.system", s.subjectPrefix, code)

	metrics, err := s.executor.ScrapeMetrics()
	if err != nil {
		s.logger.Error("Failed to scrape metrics", zap.Error(err))

		// Record failure
		s.executor.RecordMetricsFailure()

		// Publish error message so control plane knows scraping failed
		errorMsg := tasks.CreateTelemetryError(err)
		errorMsg.Code = code
		errorMsg.Location = s.config.Location
		data, marshalErr := json.Marshal(errorMsg)
		if marshalErr != nil {
			s.logger.Error("Failed to marshal metrics error message", zap.Error(marshalErr))
			return
		}

		// Even errors are published async - fire and forget
		if err := s.nats.PublishTelemetry(subject, data); err != nil {
			s.logger.Error("Failed to queue metrics error publish", zap.Error(err))
		}
		return
	}

	// Stamp identity so the message is self-describing
	metrics.Code = code
	metrics.Location = s.config.Location

	data, err := json.Marshal(metrics)
	if err != nil {
		s.logger.Error("Failed to marshal metrics", zap.Error(err))
		return
	}

	// Fire and forget with async retries
	if err := s.nats.PublishTelemetry(subject, data); err != nil {
		s.logger.Error("Failed to queue metrics publish", zap.Error(err))
		return
	}

	// Record successful execution
	s.executor.RecordMetricsSuccess()

	// Build disk summary for logging
	diskSummary := make([]string, len(metrics.Disks))
	for i, disk := range metrics.Disks {
		diskSummary[i] = fmt.Sprintf("%s:%.1f%%", disk.Drive, disk.FreePercent)
	}

	// Log success immediately (the actual publish happens in background)
	s.logger.Info("Queued metrics publish",
		zap.String("subject", subject),
		zap.Float64("cpu_percent", metrics.CPUUsagePercent),
		zap.Float64("memory_free_gb", metrics.MemoryFreeGB),
		zap.Int("disk_count", len(metrics.Disks)),
		zap.String("disks", strings.Join(diskSummary, ", ")))
}

// publishServiceStatus checks and publishes service status
func (s *Scheduler) publishServiceStatus(code string) {
	select {
	case <-s.ctx.Done():
		return
	default:
	}

	subject := fmt.Sprintf("%s.%s.telemetry.service", s.subjectPrefix, code)

	statuses, err := s.executor.GetServiceStatuses(s.config.Tasks.ServiceCheck.Services)
	if err != nil {
		s.logger.Error("Failed to get service statuses", zap.Error(err))

		// Publish error message
		errorMsg := tasks.CreateTelemetryError(err)
		errorMsg.Code = code
		errorMsg.Location = s.config.Location
		data, marshalErr := json.Marshal(errorMsg)
		if marshalErr != nil {
			s.logger.Error("Failed to marshal service status error message", zap.Error(marshalErr))
			return
		}

		if err := s.nats.PublishTelemetry(subject, data); err != nil {
			s.logger.Error("Failed to queue service status error publish", zap.Error(err))
		}
		return
	}

	// Create message with all services
	message := tasks.ServiceStatusMessage{
		Code:     code,
		Location: s.config.Location,
		Services: statuses,
		TS:       utils.NowRFC3339(),
	}

	data, err := json.Marshal(message)
	if err != nil {
		s.logger.Error("Failed to marshal service statuses", zap.Error(err))
		return
	}

	if err := s.nats.PublishTelemetry(subject, data); err != nil {
		s.logger.Error("Failed to queue service status publish", zap.Error(err))
		return
	}

	// Record successful execution
	s.executor.RecordServiceCheck()

	s.logger.Debug("Queued service status publish",
		zap.String("subject", subject),
		zap.Int("count", len(statuses)))
}

// publishInventory collects and publishes system inventory
func (s *Scheduler) publishInventory(code string) {
	select {
	case <-s.ctx.Done():
		return
	default:
	}

	subject := fmt.Sprintf("%s.%s.telemetry.inventory", s.subjectPrefix, code)

	inventory, err := s.executor.CollectInventory(s.version)
	if err != nil {
		s.logger.Error("Failed to collect inventory", zap.Error(err))
		return
	}

	// Stamp identity so the message is self-describing
	inventory.Code = code
	inventory.Location = s.config.Location

	data, err := json.Marshal(inventory)
	if err != nil {
		s.logger.Error("Failed to marshal inventory", zap.Error(err))
		return
	}

	if err := s.nats.PublishTelemetry(subject, data); err != nil {
		s.logger.Error("Failed to queue inventory publish", zap.Error(err))
		return
	}

	// Record successful execution
	s.executor.RecordInventory()

	s.logger.Info("Queued inventory publish",
		zap.String("subject", subject),
		zap.String("os", inventory.OS.Name))
}

// syncNebula pulls the Nebula config the platform currently holds and adopts it
// if it moved.
//
// Failures are warnings, not errors that stop anything: the overlay keeps running
// on the config it has, and the next tick tries again. That is the right shape
// for the common case, which is a platform that is briefly unreachable — quite
// possibly because the overlay this is trying to maintain is down.
func (s *Scheduler) syncNebula() {
	select {
	case <-s.ctx.Done():
		return
	default:
	}

	changed, err := s.nebulaSyncer.Sync()
	if err != nil {
		s.logger.Warn("Nebula config sync failed", zap.Error(err))
		return
	}

	if !changed {
		s.logger.Debug("Nebula config sync: unchanged")
		return
	}

	s.logger.Info("Nebula config updated from platform")
}
