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
	if executor.metricsCache.lastDiskReadBytes != 0 {
		t.Error("Initial lastDiskReadBytes should be zero")
	}
	if executor.metricsCache.lastDiskWriteBytes != 0 {
		t.Error("Initial lastDiskWriteBytes should be zero")
	}
}
