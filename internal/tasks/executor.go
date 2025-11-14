package tasks

import (
	"runtime"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Executor handles all task execution for both scheduled tasks and commands
type Executor struct {
	logger         *zap.Logger
	commandTimeout time.Duration
	stats          *ExecutorStats
	metricsCache   *metricsCache // Moved from global variable in metrics.go
}

// ExecutorStats tracks executor statistics for self-monitoring
type ExecutorStats struct {
	mu                sync.RWMutex
	startTime         time.Time
	commandsProcessed int64
	commandsErrored   int64
	lastError         string
	lastErrorTime     time.Time
}

// metricsCache stores previous counter values for rate calculation
// Counter-based metrics (CPU, disk I/O) need two measurements to calculate rates
type metricsCache struct {
	mu                 sync.RWMutex
	lastCPUTotal       float64
	lastCPUIdle        float64
	lastDiskReadBytes  float64
	lastDiskWriteBytes float64
	lastTimestamp      time.Time
}

// AgentMetrics represents agent self-monitoring metrics
type AgentMetrics struct {
	MemoryUsageMB     float64 `json:"memory_usage_mb"`
	Goroutines        int     `json:"goroutines"`
	UptimeSeconds     int64   `json:"uptime_seconds"`
	CommandsProcessed int64   `json:"commands_processed"`
	CommandsErrored   int64   `json:"commands_errored"`
	LastError         string  `json:"last_error,omitempty"`
	LastErrorTime     string  `json:"last_error_time,omitempty"`
}

// NewExecutor creates a new task executor
func NewExecutor(logger *zap.Logger, commandTimeout time.Duration) *Executor {
	return &Executor{
		logger:         logger,
		commandTimeout: commandTimeout,
		stats: &ExecutorStats{
			startTime: time.Now(),
		},
		metricsCache: &metricsCache{},
	}
}

// GetAgentMetrics returns current agent performance metrics
func (e *Executor) GetAgentMetrics() *AgentMetrics {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	e.stats.mu.RLock()
	defer e.stats.mu.RUnlock()

	metrics := &AgentMetrics{
		MemoryUsageMB:     float64(mem.Alloc) / 1024 / 1024,
		Goroutines:        runtime.NumGoroutine(),
		UptimeSeconds:     int64(time.Since(e.stats.startTime).Seconds()),
		CommandsProcessed: e.stats.commandsProcessed,
		CommandsErrored:   e.stats.commandsErrored,
	}

	if !e.stats.lastErrorTime.IsZero() {
		metrics.LastError = e.stats.lastError
		metrics.LastErrorTime = e.stats.lastErrorTime.Format(time.RFC3339)
	}

	return metrics
}

// RecordCommandSuccess increments success counter
func (e *Executor) RecordCommandSuccess() {
	e.stats.mu.Lock()
	defer e.stats.mu.Unlock()
	e.stats.commandsProcessed++
}

// RecordCommandError increments error counter and stores last error
func (e *Executor) RecordCommandError(err error) {
	e.stats.mu.Lock()
	defer e.stats.mu.Unlock()
	
	e.stats.commandsErrored++
	e.stats.commandsProcessed++ // Still counts as processed
	e.stats.lastError = err.Error()
	e.stats.lastErrorTime = time.Now()
}
