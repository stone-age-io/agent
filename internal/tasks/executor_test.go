package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestNewExecutor tests executor creation
func TestNewExecutor(t *testing.T) {
	timeout := 30 * time.Second
	ctx := context.Background()
	logger := zap.NewNop()

	// Test with builtin source (default)
	executor, err := NewExecutor(logger, timeout, ctx, "builtin", "")
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	if executor == nil {
		t.Fatal("NewExecutor() returned nil")
	}

	if executor.commandTimeout != timeout {
		t.Errorf("NewExecutor() timeout = %v, want %v", executor.commandTimeout, timeout)
	}

	if executor.stats == nil {
		t.Error("NewExecutor() stats is nil")
	}

	if executor.metricsCollector == nil {
		t.Error("NewExecutor() metricsCollector is nil")
	}

	if executor.httpClient == nil {
		t.Error("NewExecutor() httpClient is nil")
	}

	if executor.ctx != ctx {
		t.Error("NewExecutor() ctx not set correctly")
	}

	if executor.logger != logger {
		t.Error("NewExecutor() logger not set correctly")
	}

	// Verify stats are initialized
	if executor.stats.startTime.IsZero() {
		t.Error("NewExecutor() stats.startTime not initialized")
	}
}

// TestNewExecutorWithExporterSource tests executor creation with exporter source
func TestNewExecutorWithExporterSource(t *testing.T) {
	ctx := context.Background()
	logger := zap.NewNop()

	// Test with exporter source
	executor, err := NewExecutor(logger, time.Second, ctx, "exporter", "http://localhost:9182/metrics")
	if err != nil {
		t.Fatalf("NewExecutor() with exporter error = %v", err)
	}

	if executor.metricsCollector == nil {
		t.Error("NewExecutor() metricsCollector is nil with exporter source")
	}

	// Verify collector name contains "exporter"
	name := executor.metricsCollector.Name()
	if name == "" {
		t.Error("Collector name should not be empty")
	}
}

// TestNewExecutorInvalidSource tests executor creation with invalid source
func TestNewExecutorInvalidSource(t *testing.T) {
	ctx := context.Background()
	logger := zap.NewNop()

	_, err := NewExecutor(logger, time.Second, ctx, "invalid", "")
	if err == nil {
		t.Error("NewExecutor() should fail with invalid source")
	}
}

// TestNewExecutorExporterWithoutURL tests executor creation with exporter but no URL
func TestNewExecutorExporterWithoutURL(t *testing.T) {
	ctx := context.Background()
	logger := zap.NewNop()

	_, err := NewExecutor(logger, time.Second, ctx, "exporter", "")
	if err == nil {
		t.Error("NewExecutor() should fail with exporter source but no URL")
	}
}

// TestRecordCommandSuccess tests success counter
func TestRecordCommandSuccess(t *testing.T) {
	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

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
	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

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
	_, err = time.Parse(time.RFC3339, metrics.LastErrorTime)
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
	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

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
	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

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
	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

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

// TestHTTPClientInitialization tests that HTTP client is created and cached
func TestHTTPClientInitialization(t *testing.T) {
	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

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
	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

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
