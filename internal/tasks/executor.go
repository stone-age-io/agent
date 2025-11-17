package tasks

import (
	"net/http"
	"runtime"
	"sync"
	"time"

	"go.uber.org/zap"
	"github.com/stone-age-io/agent/internal/utils"
)

// Executor handles all task execution for both scheduled tasks and commands
type Executor struct {
	logger         *zap.Logger
	commandTimeout time.Duration
	httpClient     *http.Client  // Cached HTTP client for metrics scraping (created once, reused)
	stats          *ExecutorStats
	metricsCache   *metricsCache // Moved from global variable in metrics.go
	taskStats      *TaskStats
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

// TaskStats tracks scheduled task execution for monitoring
type TaskStats struct {
	mu sync.RWMutex
	
	// Execution timestamps
	lastHeartbeat    time.Time
	lastMetrics      time.Time
	lastServiceCheck time.Time
	lastInventory    time.Time
	
	// Execution counters
	heartbeatCount    int64
	metricsCount      int64
	metricsFailures   int64
	serviceCheckCount int64
	inventoryCount    int64
}

// metricsCache stores previous counter values for rate calculation
// Counter-based metrics (CPU, disk I/O) need two measurements to calculate rates
type metricsCache struct {
	mu              sync.RWMutex
	lastCPUTotal    float64
	lastCPUIdle     float64
	lastDiskMetrics map[string]DiskCounters  // Per-drive counters for I/O rate calculation
	lastTimestamp   time.Time
}

// DiskCounters stores previous disk counter values for rate calculation
type DiskCounters struct {
	ReadBytes  float64
	WriteBytes float64
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

// TaskHealthMetrics represents scheduled task health
type TaskHealthMetrics struct {
	LastHeartbeat    string `json:"last_heartbeat,omitempty"`
	LastMetrics      string `json:"last_metrics,omitempty"`
	LastServiceCheck string `json:"last_service_check,omitempty"`
	LastInventory    string `json:"last_inventory,omitempty"`
	
	HeartbeatCount    int64 `json:"heartbeat_count"`
	MetricsCount      int64 `json:"metrics_count"`
	MetricsFailures   int64 `json:"metrics_failures"`
	ServiceCheckCount int64 `json:"service_check_count"`
	InventoryCount    int64 `json:"inventory_count"`
}

// NewExecutor creates a new task executor
func NewExecutor(logger *zap.Logger, commandTimeout time.Duration) *Executor {
	return &Executor{
		logger:         logger,
		commandTimeout: commandTimeout,
		httpClient:     createHTTPClient(), // Create HTTP client ONCE and reuse for all scrapes
		stats: &ExecutorStats{
			startTime: time.Now(),
		},
		metricsCache: &metricsCache{
			lastDiskMetrics: make(map[string]DiskCounters), // Initialize per-drive counters map
		},
		taskStats: &TaskStats{},
	}
}

// GetAgentMetrics returns current agent performance metrics
func (e *Executor) GetAgentMetrics() *AgentMetrics {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	e.stats.mu.RLock()
	defer e.stats.mu.RUnlock()

	metrics := &AgentMetrics{
		// Use mem.Sys for total OS memory (matches Task Manager)
		// This includes heap, stack, runtime overhead - the full process footprint
		// Rounded to 2 decimal places for consistency with other metrics
		MemoryUsageMB:     utils.Round(float64(mem.Sys) / 1024 / 1024),
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

// GetTaskMetrics returns scheduled task execution metrics
func (e *Executor) GetTaskMetrics() *TaskHealthMetrics {
	e.taskStats.mu.RLock()
	defer e.taskStats.mu.RUnlock()

	metrics := &TaskHealthMetrics{
		HeartbeatCount:    e.taskStats.heartbeatCount,
		MetricsCount:      e.taskStats.metricsCount,
		MetricsFailures:   e.taskStats.metricsFailures,
		ServiceCheckCount: e.taskStats.serviceCheckCount,
		InventoryCount:    e.taskStats.inventoryCount,
	}

	// Only include timestamps if tasks have executed
	if !e.taskStats.lastHeartbeat.IsZero() {
		metrics.LastHeartbeat = e.taskStats.lastHeartbeat.Format(time.RFC3339)
	}
	if !e.taskStats.lastMetrics.IsZero() {
		metrics.LastMetrics = e.taskStats.lastMetrics.Format(time.RFC3339)
	}
	if !e.taskStats.lastServiceCheck.IsZero() {
		metrics.LastServiceCheck = e.taskStats.lastServiceCheck.Format(time.RFC3339)
	}
	if !e.taskStats.lastInventory.IsZero() {
		metrics.LastInventory = e.taskStats.lastInventory.Format(time.RFC3339)
	}

	return metrics
}

// RecordHeartbeat records a heartbeat execution
func (e *Executor) RecordHeartbeat() {
	e.taskStats.mu.Lock()
	defer e.taskStats.mu.Unlock()
	e.taskStats.lastHeartbeat = time.Now()
	e.taskStats.heartbeatCount++
}

// RecordMetricsSuccess records a successful metrics scrape
func (e *Executor) RecordMetricsSuccess() {
	e.taskStats.mu.Lock()
	defer e.taskStats.mu.Unlock()
	e.taskStats.lastMetrics = time.Now()
	e.taskStats.metricsCount++
}

// RecordMetricsFailure records a failed metrics scrape
func (e *Executor) RecordMetricsFailure() {
	e.taskStats.mu.Lock()
	defer e.taskStats.mu.Unlock()
	e.taskStats.metricsFailures++
}

// RecordServiceCheck records a service check execution
func (e *Executor) RecordServiceCheck() {
	e.taskStats.mu.Lock()
	defer e.taskStats.mu.Unlock()
	e.taskStats.lastServiceCheck = time.Now()
	e.taskStats.serviceCheckCount++
}

// RecordInventory records an inventory collection
func (e *Executor) RecordInventory() {
	e.taskStats.mu.Lock()
	defer e.taskStats.mu.Unlock()
	e.taskStats.lastInventory = time.Now()
	e.taskStats.inventoryCount++
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
