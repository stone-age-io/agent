package scheduler

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/go-co-op/gocron/v2"
	"go.uber.org/zap"
	"win-agent/internal/config"
	natsclient "win-agent/internal/nats"
	"win-agent/internal/tasks"
)

// Scheduler manages periodic task execution
type Scheduler struct {
	scheduler gocron.Scheduler
	logger    *zap.Logger
	nats      *natsclient.Client
	executor  *tasks.Executor
	config    *config.Config
	version   string
}

// New creates a new scheduler with configured tasks
func New(
	logger *zap.Logger,
	natsClient *natsclient.Client,
	executor *tasks.Executor,
	cfg *config.Config,
	version string,
) (*Scheduler, error) {
	// Create gocron scheduler
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("failed to create scheduler: %w", err)
	}

	scheduler := &Scheduler{
		scheduler: s,
		logger:    logger,
		nats:      natsClient,
		executor:  executor,
		config:    cfg,
		version:   version,
	}

	// Schedule tasks based on configuration
	if err := scheduler.scheduleTasks(); err != nil {
		return nil, fmt.Errorf("failed to schedule tasks: %w", err)
	}

	return scheduler, nil
}

// wrapTaskWithRecovery wraps a task function with panic recovery
// This prevents one task's panic from crashing the entire agent
func (s *Scheduler) wrapTaskWithRecovery(taskName string, taskFunc func()) func() {
	return func() {
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
	deviceID := s.config.DeviceID

	// If metrics are enabled, establish baseline with retries
	// This is critical for counter-based metrics (CPU, disk I/O)
	if s.config.Tasks.SystemMetrics.Enabled {
		s.logger.Info("Establishing metrics baseline")

		const maxRetries = 3
		const retryDelay = 2 * time.Second

		var baselineErr error
		for attempt := 1; attempt <= maxRetries; attempt++ {
			_, err := s.executor.ScrapeMetrics(s.config.Tasks.SystemMetrics.ExporterURL)
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
				time.Sleep(retryDelay)
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

	// Schedule heartbeat task WITH PANIC RECOVERY
	if s.config.Tasks.Heartbeat.Enabled {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.Heartbeat.Interval),
			gocron.NewTask(s.wrapTaskWithRecovery("heartbeat", func() {
				s.publishHeartbeat(deviceID)
			})),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule heartbeat: %w", err)
		}
		s.logger.Info("Scheduled heartbeat task",
			zap.Duration("interval", s.config.Tasks.Heartbeat.Interval))
	}

	// Schedule system metrics task WITH PANIC RECOVERY
	if s.config.Tasks.SystemMetrics.Enabled {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.SystemMetrics.Interval),
			gocron.NewTask(s.wrapTaskWithRecovery("metrics", func() {
				s.publishMetrics(deviceID)
			})),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule metrics: %w", err)
		}
		s.logger.Info("Scheduled metrics task",
			zap.Duration("interval", s.config.Tasks.SystemMetrics.Interval))
	}

	// Schedule service check task WITH PANIC RECOVERY
	if s.config.Tasks.ServiceCheck.Enabled {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.ServiceCheck.Interval),
			gocron.NewTask(s.wrapTaskWithRecovery("service_check", func() {
				s.publishServiceStatus(deviceID)
			})),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule service check: %w", err)
		}
		s.logger.Info("Scheduled service check task",
			zap.Duration("interval", s.config.Tasks.ServiceCheck.Interval))
	}

	// Schedule inventory task WITH PANIC RECOVERY (but run it once immediately first)
	if s.config.Tasks.Inventory.Enabled {
		// Run immediately on startup (wrapped with panic recovery)
		go s.wrapTaskWithRecovery("inventory_startup", func() {
			s.publishInventory(deviceID)
		})()

		// Then schedule for periodic execution
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.Inventory.Interval),
			gocron.NewTask(s.wrapTaskWithRecovery("inventory", func() {
				s.publishInventory(deviceID)
			})),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule inventory: %w", err)
		}
		s.logger.Info("Scheduled inventory task",
			zap.Duration("interval", s.config.Tasks.Inventory.Interval))
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

// publishHeartbeat publishes a heartbeat message
// SIMPLIFIED: No retry loops, PublishAsync handles everything!
func (s *Scheduler) publishHeartbeat(deviceID string) {
	subject := fmt.Sprintf("agents.%s.heartbeat", deviceID)

	heartbeat := s.executor.CreateHeartbeat(s.version)
	data, err := json.Marshal(heartbeat)
	if err != nil {
		s.logger.Error("Failed to marshal heartbeat", zap.Error(err))
		return
	}

	// PublishAsync is fire-and-forget with built-in retries
	// Errors are logged automatically in the async callback
	if err := s.nats.PublishTelemetry(subject, data); err != nil {
		// This only fails if we can't queue (extremely rare)
		s.logger.Error("Failed to queue heartbeat publish", zap.Error(err))
	}

	// Success logging happens in the async callback - no need here
}

// publishMetrics scrapes and publishes system metrics
func (s *Scheduler) publishMetrics(deviceID string) {
	subject := fmt.Sprintf("agents.%s.telemetry.system", deviceID)

	metrics, err := s.executor.ScrapeMetrics(s.config.Tasks.SystemMetrics.ExporterURL)
	if err != nil {
		s.logger.Error("Failed to scrape metrics", zap.Error(err))

		// Publish error message so control plane knows scraping failed
		errorMsg := tasks.CreateMetricsError(err)
		data, _ := json.Marshal(errorMsg)
		
		// Even errors are published async - fire and forget
		if err := s.nats.PublishTelemetry(subject, data); err != nil {
			s.logger.Error("Failed to queue metrics error publish", zap.Error(err))
		}
		return
	}

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

	// Log success immediately (the actual publish happens in background)
	s.logger.Info("Queued metrics publish",
		zap.String("subject", subject),
		zap.Float64("cpu_percent", metrics.CPUUsagePercent),
		zap.Float64("memory_free_gb", metrics.MemoryFreeGB),
		zap.Float64("disk_free_percent", metrics.DiskFreePercent))
}

// publishServiceStatus checks and publishes service status
func (s *Scheduler) publishServiceStatus(deviceID string) {
	subject := fmt.Sprintf("agents.%s.telemetry.service", deviceID)

	statuses, err := s.executor.GetServiceStatuses(s.config.Tasks.ServiceCheck.Services)
	if err != nil {
		s.logger.Error("Failed to get service statuses", zap.Error(err))

		// Publish error message
		errorMsg := map[string]interface{}{
			"status":    "error",
			"error":     err.Error(),
			"timestamp": s.executor.CreateHeartbeat(s.version).Timestamp,
		}
		data, _ := json.Marshal(errorMsg)
		
		if err := s.nats.PublishTelemetry(subject, data); err != nil {
			s.logger.Error("Failed to queue service status error publish", zap.Error(err))
		}
		return
	}

	// Create message with all services
	message := map[string]interface{}{
		"services":  statuses,
		"timestamp": s.executor.CreateHeartbeat(s.version).Timestamp,
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

	s.logger.Debug("Queued service status publish",
		zap.String("subject", subject),
		zap.Int("count", len(statuses)))
}

// publishInventory collects and publishes system inventory
func (s *Scheduler) publishInventory(deviceID string) {
	subject := fmt.Sprintf("agents.%s.telemetry.inventory", deviceID)

	inventory, err := s.executor.CollectInventory(s.version)
	if err != nil {
		s.logger.Error("Failed to collect inventory", zap.Error(err))
		return
	}

	data, err := json.Marshal(inventory)
	if err != nil {
		s.logger.Error("Failed to marshal inventory", zap.Error(err))
		return
	}

	if err := s.nats.PublishTelemetry(subject, data); err != nil {
		s.logger.Error("Failed to queue inventory publish", zap.Error(err))
		return
	}

	s.logger.Info("Queued inventory publish",
		zap.String("subject", subject),
		zap.String("os", inventory.OS.Name))
}
