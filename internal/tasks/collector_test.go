package tasks

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestBuiltinCollector_Collect tests the builtin collector
func TestBuiltinCollector_Collect(t *testing.T) {
	logger := zap.NewNop()
	collector := NewBuiltinCollector(logger)

	ctx := context.Background()

	// First collection - establishes baseline
	metrics1, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("First collect failed: %v", err)
	}

	// CPU should be 0 on first scrape (no delta yet)
	if metrics1.CPUUsagePercent != 0 {
		t.Logf("Note: CPU on first scrape = %.2f (expected 0 for baseline)", metrics1.CPUUsagePercent)
	}

	// Memory should have a value
	if metrics1.MemoryFreeGB <= 0 {
		t.Errorf("MemoryFreeGB = %.2f, expected > 0", metrics1.MemoryFreeGB)
	}

	// Should have at least one disk
	if len(metrics1.Disks) == 0 {
		t.Error("Expected at least one disk")
	}

	// Timestamp should be set
	if metrics1.Timestamp == "" {
		t.Error("Timestamp not set")
	}

	// Wait a bit for CPU delta
	time.Sleep(100 * time.Millisecond)

	// Second collection
	metrics2, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("Second collect failed: %v", err)
	}

	// Now CPU should have a value (0-100)
	if metrics2.CPUUsagePercent < 0 || metrics2.CPUUsagePercent > 100 {
		t.Errorf("CPUUsagePercent = %.2f, expected 0-100", metrics2.CPUUsagePercent)
	}
}

// TestBuiltinCollector_Name tests the collector name
func TestBuiltinCollector_Name(t *testing.T) {
	logger := zap.NewNop()
	collector := NewBuiltinCollector(logger)

	name := collector.Name()
	if !strings.Contains(name, "builtin") {
		t.Errorf("Name() = %s, expected to contain 'builtin'", name)
	}
}

// TestBuiltinCollector_ResetCache tests cache reset
func TestBuiltinCollector_ResetCache(t *testing.T) {
	logger := zap.NewNop()
	collector := NewBuiltinCollector(logger)

	ctx := context.Background()

	// Collect to establish baseline
	_, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("First collect failed: %v", err)
	}

	// Reset cache
	collector.ResetCache()

	// After reset, CPU should be 0 again (baseline re-established)
	metrics, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect after reset failed: %v", err)
	}

	if metrics.CPUUsagePercent != 0 {
		t.Logf("Note: CPU after reset = %.2f (expected 0 for new baseline)", metrics.CPUUsagePercent)
	}
}

// TestExporterCollector_Name tests the exporter collector name
func TestExporterCollector_Name(t *testing.T) {
	logger := zap.NewNop()
	collector := NewExporterCollector("http://localhost:9182/metrics", logger, nil)

	name := collector.Name()
	if !strings.Contains(name, "exporter") {
		t.Errorf("Name() = %s, expected to contain 'exporter'", name)
	}
	if !strings.Contains(name, "localhost:9182") {
		t.Errorf("Name() = %s, expected to contain URL", name)
	}
}

// TestCollectorFactory tests the collector factory function
func TestCollectorFactory(t *testing.T) {
	logger := zap.NewNop()

	tests := []struct {
		name        string
		source      string
		exporterURL string
		expectType  string
		expectError bool
	}{
		{
			name:       "default is builtin",
			source:     "",
			expectType: "builtin",
		},
		{
			name:       "explicit builtin",
			source:     "builtin",
			expectType: "builtin",
		},
		{
			name:        "exporter with URL",
			source:      "exporter",
			exporterURL: "http://localhost:9182/metrics",
			expectType:  "exporter",
		},
		{
			name:        "exporter without URL fails",
			source:      "exporter",
			expectError: true,
		},
		{
			name:        "invalid source fails",
			source:      "invalid",
			expectError: true,
		},
		{
			name:       "case insensitive",
			source:     "BUILTIN",
			expectType: "builtin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector, err := NewMetricsCollector(tt.source, tt.exporterURL, logger, nil)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if !strings.Contains(collector.Name(), tt.expectType) {
				t.Errorf("Name() = %s, expected to contain %s", collector.Name(), tt.expectType)
			}
		})
	}
}

// TestValidateMetrics tests the metrics validation function
func TestValidateMetrics(t *testing.T) {
	logger := zap.NewNop()
	collector := NewBuiltinCollector(logger)

	tests := []struct {
		name        string
		metrics     *SystemMetrics
		expectError bool
	}{
		{
			name: "valid metrics",
			metrics: &SystemMetrics{
				CPUUsagePercent: 50.0,
				MemoryFreeGB:    8.0,
				Disks: []DiskMetrics{
					{Drive: "C:", TotalGB: 500, FreeGB: 250, FreePercent: 50},
				},
			},
			expectError: false,
		},
		{
			name: "first scrape (CPU 0) is valid",
			metrics: &SystemMetrics{
				CPUUsagePercent: 0,
				MemoryFreeGB:    8.0,
				Disks:           []DiskMetrics{},
			},
			expectError: false,
		},
		{
			name: "negative memory is invalid",
			metrics: &SystemMetrics{
				CPUUsagePercent: 0,
				MemoryFreeGB:    -1.0,
			},
			expectError: true,
		},
		{
			name: "CPU over 100 is invalid",
			metrics: &SystemMetrics{
				CPUUsagePercent: 150.0,
				MemoryFreeGB:    8.0,
			},
			expectError: true,
		},
		{
			name: "negative disk free is invalid",
			metrics: &SystemMetrics{
				CPUUsagePercent: 0,
				MemoryFreeGB:    8.0,
				Disks: []DiskMetrics{
					{Drive: "C:", TotalGB: 500, FreeGB: -1, FreePercent: 50},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMetrics(tt.metrics, collector)
			if tt.expectError && err == nil {
				t.Error("Expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}
