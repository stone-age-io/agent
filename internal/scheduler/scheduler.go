package scheduler

import (
	"encoding/json"
	"fmt"

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

// scheduleTasks sets up all periodic tasks
func (s *Scheduler) scheduleTasks() error {
	deviceID := s.config.DeviceID

	// If metrics are enabled, do an initial baseline scrape (don't publish)
	// This establishes counter baselines so the first real publish has valid data
	if s.config.Tasks.SystemMetrics.Enabled {
		s.logger.Info("Establishing metrics baseline (not publishing)")
		_, err := s.executor.ScrapeMetrics(s.config.Tasks.SystemMetrics.ExporterURL)
		if err != nil {
			s.logger.Warn("Failed to establish metrics baseline", zap.Error(err))
			// Continue anyway - first publish will just skip rate-based metrics
		} else {
			s.logger.Info("Metrics baseline established")
		}
	}

	// Schedule heartbeat task
	if s.config.Tasks.Heartbeat.Enabled {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.Heartbeat.Interval),
			gocron.NewTask(func() {
				s.publishHeartbeat(deviceID)
			}),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule heartbeat: %w", err)
		}
		s.logger.Info("Scheduled heartbeat task",
			zap.Duration("interval", s.config.Tasks.Heartbeat.Interval))
	}

	// Schedule system metrics task
	if s.config.Tasks.SystemMetrics.Enabled {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.SystemMetrics.Interval),
			gocron.NewTask(func() {
				s.publishMetrics(deviceID)
			}),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule metrics: %w", err)
		}
		s.logger.Info("Scheduled metrics task",
			zap.Duration("interval", s.config.Tasks.SystemMetrics.Interval))
	}

	// Schedule service check task
	if s.config.Tasks.ServiceCheck.Enabled {
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.ServiceCheck.Interval),
			gocron.NewTask(func() {
				s.publishServiceStatus(deviceID)
			}),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule service check: %w", err)
		}
		s.logger.Info("Scheduled service check task",
			zap.Duration("interval", s.config.Tasks.ServiceCheck.Interval))
	}

	// Schedule inventory task (but run it once immediately first)
	if s.config.Tasks.Inventory.Enabled {
		// Run immediately on startup
		go s.publishInventory(deviceID)

		// Then schedule for periodic execution
		_, err := s.scheduler.NewJob(
			gocron.DurationJob(s.config.Tasks.Inventory.Interval),
			gocron.NewTask(func() {
				s.publishInventory(deviceID)
			}),
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
func (s *Scheduler) publishHeartbeat(deviceID string) {
	subject := fmt.Sprintf("agents.%s.heartbeat", deviceID)

	heartbeat := s.executor.CreateHeartbeat(s.version)
	data, err := json.Marshal(heartbeat)
	if err != nil {
		s.logger.Error("Failed to marshal heartbeat", zap.Error(err))
		return
	}

	if err := s.nats.PublishTelemetry(subject, data); err != nil {
		s.logger.Error("Failed to publish heartbeat",
			zap.String("subject", subject),
			zap.Error(err))
	} else {
		s.logger.Debug("Published heartbeat", zap.String("subject", subject))
	}
}

// publishMetrics scrapes and publishes system metrics
func (s *Scheduler) publishMetrics(deviceID string) {
	subject := fmt.Sprintf("agents.%s.telemetry.system", deviceID)

	metrics, err := s.executor.ScrapeMetrics(s.config.Tasks.SystemMetrics.ExporterURL)
	if err != nil {
		s.logger.Error("Failed to scrape metrics", zap.Error(err))

		// Publish error message
		errorMsg := tasks.CreateMetricsError(err)
		data, _ := json.Marshal(errorMsg)
		if err := s.nats.PublishTelemetry(subject, data); err != nil {
			s.logger.Error("Failed to publish metrics error",
				zap.String("subject", subject),
				zap.Error(err))
		}
		return
	}

	data, err := json.Marshal(metrics)
	if err != nil {
		s.logger.Error("Failed to marshal metrics", zap.Error(err))
		return
	}

	if err := s.nats.PublishTelemetry(subject, data); err != nil {
		s.logger.Error("Failed to publish metrics",
			zap.String("subject", subject),
			zap.Error(err))
	} else {
		s.logger.Info("Published metrics",
			zap.String("subject", subject),
			zap.Float64("cpu_percent", metrics.CPUUsagePercent),
			zap.Float64("memory_free_gb", metrics.MemoryFreeGB),
			zap.Float64("disk_free_percent", metrics.DiskFreePercent))
	}
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
			s.logger.Error("Failed to publish service status error",
				zap.String("subject", subject),
				zap.Error(err))
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
		s.logger.Error("Failed to publish service statuses",
			zap.String("subject", subject),
			zap.Error(err))
	} else {
		s.logger.Debug("Published service statuses",
			zap.String("subject", subject),
			zap.Int("count", len(statuses)))
	}
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
		s.logger.Error("Failed to publish inventory",
			zap.String("subject", subject),
			zap.Error(err))
	} else {
		s.logger.Info("Published inventory",
			zap.String("subject", subject),
			zap.String("os", inventory.OS.Name))
	}
}
