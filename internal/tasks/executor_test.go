package tasks

import (
	"errors"
	"testing"
	"time"
)

// TestNewExecutor tests executor creation
func TestNewExecutor(t *testing.T) {
	timeout := 30 * time.Second
	executor := NewExecutor(nil, timeout)

	if executor == nil {
		t.Fatal("NewExecutor() returned nil")
	}

	if executor.commandTimeout != timeout {
		t.Errorf("NewExecutor() timeout = %v, want %v", executor.commandTimeout, timeout)
	}

	if executor.stats == nil {
		t.Error("NewExecutor() stats is nil")
	}

	if executor.metricsCache == nil {
		t.Error("NewExecutor() metricsCache is nil")
	}

	if executor.httpClient == nil {
		t.Error("NewExecutor() httpClient is nil")
	}

	// Verify stats are initialized
	if executor.stats.startTime.IsZero() {
		t.Error("NewExecutor() stats.startTime not initialized")
	}
}

// TestRecordCommandSuccess tests success counter
func TestRecordCommandSuccess(t *testing.T) {
	executor := NewExecutor(nil, 0)

	// Initial state
	metrics := executor.GetAgentMetrics()
	if metrics.CommandsProcessed != 0 {
		t.Errorf("Initial CommandsProcessed = %d, want 0", metrics.CommandsProcessed)
	}

	// Record success
	executor.RecordCommandSuccess()

	// Check counter incremented
	metrics = executor.GetAgentMetrics()
	if metrics.CommandsProcessed != 1 {
		t.Errorf("After success, CommandsProcessed = %d, want 1", metrics.CommandsProcessed)
	}

	// Record multiple successes
	executor.RecordCommandSuccess()
	executor.RecordCommandSuccess()

	metrics = executor.GetAgentMetrics()
	if metrics.CommandsProcessed != 3 {
		t.Errorf("After 3 successes, CommandsProcessed = %d, want 3", metrics.CommandsProcessed)
	}

	// Error count should still be zero
	if metrics.CommandsErrored != 0 {
		t.Errorf("CommandsErrored = %d, want 0", metrics.CommandsErrored)
	}
}

// TestRecordCommandError tests error counter and tracking
func TestRecordCommandError(t *testing.T) {
	executor := NewExecutor(nil, 0)

	// Initial state
	metrics := executor.GetAgentMetrics()
	if metrics.CommandsErrored != 0 {
		t.Errorf("Initial CommandsErrored = %d, want 0", metrics.CommandsErrored)
	}
	if metrics.LastError != "" {
		t.Errorf("Initial LastError = %q, want empty", metrics.LastError)
	}

	// Record error
	testErr := errors.New("test error")
	executor.RecordCommandError(testErr)

	// Check counters
	metrics = executor.GetAgentMetrics()
	if metrics.CommandsErrored != 1 {
		t.Errorf("After error, CommandsErrored = %d, want 1", metrics.CommandsErrored)
	}
	if metrics.CommandsProcessed != 1 {
		t.Errorf("After error, CommandsProcessed = %d, want 1 (errors count as processed)", metrics.CommandsProcessed)
	}

	// Check error details
	if metrics.LastError != testErr.Error() {
		t.Errorf("LastError = %q, want %q", metrics.LastError, testErr.Error())
	}
	if metrics.LastErrorTime == "" {
		t.Error("LastErrorTime is empty")
	}

	// Parse timestamp
	_, err := time.Parse(time.RFC3339, metrics.LastErrorTime)
	if err != nil {
		t.Errorf("LastErrorTime parse error: %v", err)
	}

	// Record another error
	testErr2 := errors.New("second error")
	time.Sleep(10 * time.Millisecond) // Ensure time difference
	executor.RecordCommandError(testErr2)

	metrics = executor.GetAgentMetrics()
	if metrics.CommandsErrored != 2 {
		t.Errorf("After 2 errors, CommandsErrored = %d, want 2", metrics.CommandsErrored)
	}

	// Last error should be updated
	if metrics.LastError != testErr2.Error() {
		t.Errorf("LastError = %q, want %q", metrics.LastError, testErr2.Error())
	}
}

// TestGetAgentMetrics tests metrics retrieval
func TestGetAgentMetrics(t *testing.T) {
	executor := NewExecutor(nil, 0)

	// Get initial metrics
	metrics := executor.GetAgentMetrics()

	// Verify basic fields are set
	if metrics.MemoryUsageMB < 0 {
		t.Errorf("MemoryUsageMB = %f, should be non-negative", metrics.MemoryUsageMB)
	}

	if metrics.Goroutines <= 0 {
		t.Errorf("Goroutines = %d, should be positive", metrics.Goroutines)
	}

	if metrics.UptimeSeconds < 0 {
		t.Errorf("UptimeSeconds = %d, should be non-negative", metrics.UptimeSeconds)
	}

	// Initial counters should be zero
	if metrics.CommandsProcessed != 0 {
		t.Errorf("Initial CommandsProcessed = %d, want 0", metrics.CommandsProcessed)
	}
	if metrics.CommandsErrored != 0 {
		t.Errorf("Initial CommandsErrored = %d, want 0", metrics.CommandsErrored)
	}

	// LastError should be empty initially
	if metrics.LastError != "" {
		t.Errorf("Initial LastError = %q, want empty", metrics.LastError)
	}
	if metrics.LastErrorTime != "" {
		t.Errorf("Initial LastErrorTime = %q, want empty", metrics.LastErrorTime)
	}
}

// TestUptimeCalculation tests that uptime increases over time
func TestUptimeCalculation(t *testing.T) {
	executor := NewExecutor(nil, 0)

	// Get initial uptime
	metrics1 := executor.GetAgentMetrics()
	uptime1 := metrics1.UptimeSeconds

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Get uptime again
	metrics2 := executor.GetAgentMetrics()
	uptime2 := metrics2.UptimeSeconds

	// Uptime should have increased (or at least stayed same if very fast)
	if uptime2 < uptime1 {
		t.Errorf("Uptime decreased from %d to %d", uptime1, uptime2)
	}
}

// TestConcurrentCommandRecording tests thread-safety of command recording
func TestConcurrentCommandRecording(t *testing.T) {
	executor := NewExecutor(nil, 0)

	// Record commands concurrently
	done := make(chan bool)
	numGoroutines := 10
	operationsPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < operationsPerGoroutine; j++ {
				if j%2 == 0 {
					executor.RecordCommandSuccess()
				} else {
					executor.RecordCommandError(errors.New("test error"))
				}
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify counts
	metrics := executor.GetAgentMetrics()
	expectedTotal := int64(numGoroutines * operationsPerGoroutine)
	expectedErrors := int64(numGoroutines * (operationsPerGoroutine / 2))

	if metrics.CommandsProcessed != expectedTotal {
		t.Errorf("CommandsProcessed = %d, want %d", metrics.CommandsProcessed, expectedTotal)
	}
	if metrics.CommandsErrored != expectedErrors {
		t.Errorf("CommandsErrored = %d, want %d", metrics.CommandsErrored, expectedErrors)
	}
}

// TestMetricsCacheInitialization tests that metrics cache is properly initialized
func TestMetricsCacheInitialization(t *testing.T) {
	executor := NewExecutor(nil, 0)

	// Access the cache through the executor
	if executor.metricsCache == nil {
		t.Fatal("metricsCache is nil")
	}

	// Check initial values
	executor.metricsCache.mu.RLock()
	defer executor.metricsCache.mu.RUnlock()

	if !executor.metricsCache.lastTimestamp.IsZero() {
		t.Error("Initial lastTimestamp should be zero")
	}
	if executor.metricsCache.lastCPUTotal != 0 {
		t.Error("Initial lastCPUTotal should be zero")
	}
	if executor.metricsCache.lastCPUIdle != 0 {
		t.Error("Initial lastCPUIdle should be zero")
	}
	
	// NEW: Check per-drive disk metrics map is initialized
	if executor.metricsCache.lastDiskMetrics == nil {
		t.Error("Initial lastDiskMetrics map should be initialized (not nil)")
	}
	if len(executor.metricsCache.lastDiskMetrics) != 0 {
		t.Errorf("Initial lastDiskMetrics should be empty, got %d entries", len(executor.metricsCache.lastDiskMetrics))
	}
}

// TestDiskMetricsCacheStorage tests per-drive disk metrics storage
func TestDiskMetricsCacheStorage(t *testing.T) {
	executor := NewExecutor(nil, 0)

	// Simulate storing metrics for multiple drives
	executor.metricsCache.mu.Lock()
	
	// Store metrics for C: drive
	executor.metricsCache.lastDiskMetrics["C:"] = DiskCounters{
		ReadBytes:  1024000,
		WriteBytes: 512000,
	}
	
	// Store metrics for D: drive
	executor.metricsCache.lastDiskMetrics["D:"] = DiskCounters{
		ReadBytes:  2048000,
		WriteBytes: 1024000,
	}
	
	executor.metricsCache.mu.Unlock()

	// Verify storage
	executor.metricsCache.mu.RLock()
	defer executor.metricsCache.mu.RUnlock()

	// Check C: drive
	cDrive, exists := executor.metricsCache.lastDiskMetrics["C:"]
	if !exists {
		t.Error("C: drive metrics not found in cache")
	}
	if cDrive.ReadBytes != 1024000 {
		t.Errorf("C: ReadBytes = %f, want 1024000", cDrive.ReadBytes)
	}
	if cDrive.WriteBytes != 512000 {
		t.Errorf("C: WriteBytes = %f, want 512000", cDrive.WriteBytes)
	}

	// Check D: drive
	dDrive, exists := executor.metricsCache.lastDiskMetrics["D:"]
	if !exists {
		t.Error("D: drive metrics not found in cache")
	}
	if dDrive.ReadBytes != 2048000 {
		t.Errorf("D: ReadBytes = %f, want 2048000", dDrive.ReadBytes)
	}
	if dDrive.WriteBytes != 1024000 {
		t.Errorf("D: WriteBytes = %f, want 1024000", dDrive.WriteBytes)
	}

	// Verify map size
	if len(executor.metricsCache.lastDiskMetrics) != 2 {
		t.Errorf("lastDiskMetrics should have 2 entries, got %d", len(executor.metricsCache.lastDiskMetrics))
	}
}

// TestDiskMetricsCacheUpdate tests updating disk metrics for existing drive
func TestDiskMetricsCacheUpdate(t *testing.T) {
	executor := NewExecutor(nil, 0)

	executor.metricsCache.mu.Lock()

	// Initial values for C: drive
	executor.metricsCache.lastDiskMetrics["C:"] = DiskCounters{
		ReadBytes:  1000000,
		WriteBytes: 500000,
	}

	// Update values for C: drive (simulating next scrape)
	counters := executor.metricsCache.lastDiskMetrics["C:"]
	counters.ReadBytes = 2000000
	counters.WriteBytes = 1000000
	executor.metricsCache.lastDiskMetrics["C:"] = counters

	executor.metricsCache.mu.Unlock()

	// Verify update
	executor.metricsCache.mu.RLock()
	defer executor.metricsCache.mu.RUnlock()

	updated := executor.metricsCache.lastDiskMetrics["C:"]
	if updated.ReadBytes != 2000000 {
		t.Errorf("Updated ReadBytes = %f, want 2000000", updated.ReadBytes)
	}
	if updated.WriteBytes != 1000000 {
		t.Errorf("Updated WriteBytes = %f, want 1000000", updated.WriteBytes)
	}
}

// TestHTTPClientInitialization tests that HTTP client is created and cached
func TestHTTPClientInitialization(t *testing.T) {
	executor := NewExecutor(nil, 0)

	if executor.httpClient == nil {
		t.Fatal("httpClient should be initialized, got nil")
	}

	// Verify it's the same instance on multiple accesses
	client1 := executor.httpClient
	client2 := executor.httpClient

	if client1 != client2 {
		t.Error("httpClient should be the same instance (cached)")
	}

	// Verify timeout is set (should be 30s from createHTTPClient)
	if executor.httpClient.Timeout != 30*time.Second {
		t.Errorf("httpClient.Timeout = %v, want 30s", executor.httpClient.Timeout)
	}
}

// TestTaskStatsRecording tests task execution tracking
func TestTaskStatsRecording(t *testing.T) {
	executor := NewExecutor(nil, 0)

	// Initial state - all timestamps should be zero
	metrics := executor.GetTaskMetrics()
	if metrics.HeartbeatCount != 0 {
		t.Errorf("Initial HeartbeatCount = %d, want 0", metrics.HeartbeatCount)
	}
	if metrics.MetricsCount != 0 {
		t.Errorf("Initial MetricsCount = %d, want 0", metrics.MetricsCount)
	}

	// Record some task executions
	executor.RecordHeartbeat()
	executor.RecordMetricsSuccess()
	executor.RecordMetricsSuccess()
	executor.RecordMetricsFailure()
	executor.RecordServiceCheck()
	executor.RecordInventory()

	// Check counters
	metrics = executor.GetTaskMetrics()
	if metrics.HeartbeatCount != 1 {
		t.Errorf("HeartbeatCount = %d, want 1", metrics.HeartbeatCount)
	}
	if metrics.MetricsCount != 2 {
		t.Errorf("MetricsCount = %d, want 2", metrics.MetricsCount)
	}
	if metrics.MetricsFailures != 1 {
		t.Errorf("MetricsFailures = %d, want 1", metrics.MetricsFailures)
	}
	if metrics.ServiceCheckCount != 1 {
		t.Errorf("ServiceCheckCount = %d, want 1", metrics.ServiceCheckCount)
	}
	if metrics.InventoryCount != 1 {
		t.Errorf("InventoryCount = %d, want 1", metrics.InventoryCount)
	}

	// Check timestamps are set
	if metrics.LastHeartbeat == "" {
		t.Error("LastHeartbeat should be set")
	}
	if metrics.LastMetrics == "" {
		t.Error("LastMetrics should be set")
	}
	if metrics.LastServiceCheck == "" {
		t.Error("LastServiceCheck should be set")
	}
	if metrics.LastInventory == "" {
		t.Error("LastInventory should be set")
	}
}
