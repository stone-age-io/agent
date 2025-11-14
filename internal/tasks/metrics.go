package tasks

import (
	"fmt"
	"io"
	"net/http"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"go.uber.org/zap"
	"win-agent/internal/utils"
)

// SystemMetrics represents system metrics collected from windows_exporter
type SystemMetrics struct {
	CPUUsagePercent      float64 `json:"cpu_usage_percent"`
	MemoryFreeGB         float64 `json:"memory_free_gb"`
	DiskFreePercent      float64 `json:"disk_free_percent"`
	DiskReadBytesPerSec  float64 `json:"disk_read_bytes_per_sec"`
	DiskWriteBytesPerSec float64 `json:"disk_write_bytes_per_sec"`
	Timestamp            string  `json:"timestamp"`
}

// ScrapeMetrics fetches and parses metrics from windows_exporter
func (e *Executor) ScrapeMetrics(exporterURL string) (*SystemMetrics, error) {
	// Fetch metrics from windows_exporter
	resp, err := http.Get(exporterURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Parse metrics using expfmt
	metrics, err := e.parsePrometheusMetrics(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metrics: %w", err)
	}

	// Validate metrics before returning
	if err := e.validateMetrics(metrics); err != nil {
		return nil, fmt.Errorf("invalid metrics: %w", err)
	}

	metrics.Timestamp = time.Now().UTC().Format(time.RFC3339)
	return metrics, nil
}

// parsePrometheusMetrics parses Prometheus format metrics using expfmt
func (e *Executor) parsePrometheusMetrics(reader io.Reader) (*SystemMetrics, error) {
	// Use NewDecoder with FmtText format for proper initialization
	// This ensures validation scheme is properly set
	decoder := expfmt.NewDecoder(reader, expfmt.FmtText)

	metricFamilies := make(map[string]*dto.MetricFamily)

	for {
		mf := &dto.MetricFamily{}
		err := decoder.Decode(mf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to decode metric family: %w", err)
		}
		metricFamilies[mf.GetName()] = mf
	}

	// Debug: Log available metric families (only first time)
	e.metricsCache.mu.RLock()
	isFirstScrape := e.metricsCache.lastTimestamp.IsZero()
	e.metricsCache.mu.RUnlock()

	if isFirstScrape {
		e.logger.Debug("Available metric families",
			zap.Int("count", len(metricFamilies)),
			zap.Strings("names", getMetricNames(metricFamilies)))
	}

	metrics := &SystemMetrics{}

	// Extract CPU usage
	// HOW THIS WORKS (grug brain version):
	// - windows_cpu_time_total is a counter = total seconds CPU spent in each mode
	// - Each core reports time in different modes: idle, user, privileged, dpc, interrupt
	// - We sum ALL time across ALL cores and ALL modes = total available CPU seconds
	// - We sum IDLE time across ALL cores = wasted CPU seconds
	// - CPU usage % = (total - idle) / total * 100

	cpuFound := false

	if family, ok := metricFamilies["windows_cpu_time_total"]; ok {
		var totalTime, idleTime float64

		// Sum across ALL cores and ALL modes
		for _, m := range family.Metric {
			mode := getLabelValue(m.Label, "mode")

			if m.Counter != nil {
				value := m.Counter.GetValue()
				totalTime += value // Add all time from all modes and cores

				if mode == "idle" {
					idleTime += value // Add idle time from all cores
				}
			}
		}

		// Lock the cache for reading and writing
		e.metricsCache.mu.Lock()
		defer e.metricsCache.mu.Unlock()

		// Only calculate if we have a previous measurement
		if !e.metricsCache.lastTimestamp.IsZero() && totalTime > 0 && e.metricsCache.lastCPUTotal > 0 {
			// How much CPU time passed between measurements
			totalDelta := totalTime - e.metricsCache.lastCPUTotal
			idleDelta := idleTime - e.metricsCache.lastCPUIdle

			if totalDelta > 0 {
				// Usage = time spent NOT idle / total time
				idlePercent := (idleDelta / totalDelta) * 100
				metrics.CPUUsagePercent = utils.Round(100 - idlePercent)
				cpuFound = true

				e.logger.Debug("CPU calculated",
					zap.Float64("total_delta", totalDelta),
					zap.Float64("idle_delta", idleDelta),
					zap.Float64("idle_percent", idlePercent),
					zap.Float64("usage_percent", metrics.CPUUsagePercent))
			}
		} else if e.metricsCache.lastTimestamp.IsZero() {
			e.logger.Debug("CPU baseline stored (first scrape)",
				zap.Float64("total_time", totalTime),
				zap.Float64("idle_time", idleTime))
		}

		// Store current values for next time
		e.metricsCache.lastCPUTotal = totalTime
		e.metricsCache.lastCPUIdle = idleTime

		// Note: defer unlock will happen at end of function
	}

	// Extract memory free bytes and convert to GB
	// Try multiple possible metric names
	memoryFound := false

	// Primary metric: available bytes (includes cache that can be freed)
	if family, ok := metricFamilies["windows_memory_available_bytes"]; ok {
		if len(family.Metric) > 0 && family.Metric[0].Gauge != nil {
			bytes := family.Metric[0].Gauge.GetValue()
			metrics.MemoryFreeGB = utils.Round(bytes / 1024 / 1024 / 1024)
			memoryFound = true
		}
	}

	// Fallback: try physical free bytes
	if !memoryFound {
		if family, ok := metricFamilies["windows_memory_physical_free_bytes"]; ok {
			if len(family.Metric) > 0 && family.Metric[0].Gauge != nil {
				bytes := family.Metric[0].Gauge.GetValue()
				metrics.MemoryFreeGB = utils.Round(bytes / 1024 / 1024 / 1024)
				memoryFound = true
				e.logger.Debug("Using physical_free_bytes fallback for memory metric")
			}
		}
	}

	// Extract disk free space for C: drive
	if family, ok := metricFamilies["windows_logical_disk_free_bytes"]; ok {
		var freeBytes, totalBytes float64
		foundFree, foundTotal := false, false

		for _, m := range family.Metric {
			volume := getLabelValue(m.Label, "volume")
			if volume == "C:" && m.Gauge != nil {
				freeBytes = m.Gauge.GetValue()
				foundFree = true
				break
			}
		}

		// Get total size to calculate percentage
		if totalFamily, ok := metricFamilies["windows_logical_disk_size_bytes"]; ok {
			for _, m := range totalFamily.Metric {
				volume := getLabelValue(m.Label, "volume")
				if volume == "C:" && m.Gauge != nil {
					totalBytes = m.Gauge.GetValue()
					foundTotal = true
					break
				}
			}
		}

		if foundFree && foundTotal && totalBytes > 0 {
			metrics.DiskFreePercent = utils.Round((freeBytes / totalBytes) * 100)
		}
	}

	// Extract disk I/O rates (read and write)
	// Same concept as CPU - counters need two measurements to calculate rate
	now := time.Now()

	// Lock for disk I/O cache operations
	e.metricsCache.mu.Lock()
	
	if !e.metricsCache.lastTimestamp.IsZero() {
		timeDelta := now.Sub(e.metricsCache.lastTimestamp).Seconds()

		if timeDelta > 0 {
			// Read bytes
			if family, ok := metricFamilies["windows_logical_disk_read_bytes_total"]; ok {
				for _, m := range family.Metric {
					volume := getLabelValue(m.Label, "volume")
					if volume == "C:" && m.Counter != nil {
						currentRead := m.Counter.GetValue()
						if e.metricsCache.lastDiskReadBytes > 0 {
							delta := currentRead - e.metricsCache.lastDiskReadBytes
							metrics.DiskReadBytesPerSec = utils.Round(delta / timeDelta)
						}
						e.metricsCache.lastDiskReadBytes = currentRead
						break
					}
				}
			}

			// Write bytes
			if family, ok := metricFamilies["windows_logical_disk_write_bytes_total"]; ok {
				for _, m := range family.Metric {
					volume := getLabelValue(m.Label, "volume")
					if volume == "C:" && m.Counter != nil {
						currentWrite := m.Counter.GetValue()
						if e.metricsCache.lastDiskWriteBytes > 0 {
							delta := currentWrite - e.metricsCache.lastDiskWriteBytes
							metrics.DiskWriteBytesPerSec = utils.Round(delta / timeDelta)
						}
						e.metricsCache.lastDiskWriteBytes = currentWrite
						break
					}
				}
			}
		}
	} else {
		// First scrape - just store baseline values
		if family, ok := metricFamilies["windows_logical_disk_read_bytes_total"]; ok {
			for _, m := range family.Metric {
				volume := getLabelValue(m.Label, "volume")
				if volume == "C:" && m.Counter != nil {
					e.metricsCache.lastDiskReadBytes = m.Counter.GetValue()
					break
				}
			}
		}

		if family, ok := metricFamilies["windows_logical_disk_write_bytes_total"]; ok {
			for _, m := range family.Metric {
				volume := getLabelValue(m.Label, "volume")
				if volume == "C:" && m.Counter != nil {
					e.metricsCache.lastDiskWriteBytes = m.Counter.GetValue()
					break
				}
			}
		}

		e.logger.Debug("Disk I/O baseline stored, will calculate on next scrape")
	}

	e.metricsCache.lastTimestamp = now
	e.metricsCache.mu.Unlock()

	// Log warnings if metrics weren't found
	if !cpuFound && !isFirstScrape {
		e.logger.Warn("CPU metric not found or could not be calculated",
			zap.Bool("has_cpu_time_total", metricFamilies["windows_cpu_time_total"] != nil),
			zap.Bool("is_first_scrape", isFirstScrape))
	}
	if !memoryFound {
		e.logger.Warn("Memory metric not found",
			zap.Bool("has_memory_available_bytes", metricFamilies["windows_memory_available_bytes"] != nil),
			zap.Bool("has_physical_free_bytes", metricFamilies["windows_memory_physical_free_bytes"] != nil))
	}

	return metrics, nil
}

// validateMetrics performs sanity checks on metrics values
func (e *Executor) validateMetrics(m *SystemMetrics) error {
	// Check if this is the first scrape
	e.metricsCache.mu.RLock()
	isFirstScrape := e.metricsCache.lastTimestamp.IsZero()
	e.metricsCache.mu.RUnlock()

	if isFirstScrape {
		// First scrape - no CPU value yet, skip CPU validation
		return nil
	}

	// Validate CPU percentage (only if we have a value)
	if m.CPUUsagePercent < 0 || m.CPUUsagePercent > 100 {
		return fmt.Errorf("invalid CPU usage: %.2f%% (must be 0-100)", m.CPUUsagePercent)
	}

	// Validate memory (should be positive)
	if m.MemoryFreeGB < 0 {
		return fmt.Errorf("invalid memory free: %.2f GB (cannot be negative)", m.MemoryFreeGB)
	}

	// Validate disk percentage
	if m.DiskFreePercent < 0 || m.DiskFreePercent > 100 {
		return fmt.Errorf("invalid disk free: %.2f%% (must be 0-100)", m.DiskFreePercent)
	}

	// Validate disk I/O rates (should not be negative)
	if m.DiskReadBytesPerSec < 0 {
		return fmt.Errorf("invalid disk read rate: %.2f bytes/sec (cannot be negative)", m.DiskReadBytesPerSec)
	}
	if m.DiskWriteBytesPerSec < 0 {
		return fmt.Errorf("invalid disk write rate: %.2f bytes/sec (cannot be negative)", m.DiskWriteBytesPerSec)
	}

	return nil
}

// getLabelValue extracts a label value from a metric's label pairs
func getLabelValue(labels []*dto.LabelPair, name string) string {
	for _, label := range labels {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}

// MetricsError represents an error that occurred during metrics collection
type MetricsError struct {
	Status    string `json:"status"`
	Error     string `json:"error"`
	Timestamp string `json:"timestamp"`
}

// CreateMetricsError creates an error message for metrics failures
func CreateMetricsError(err error) *MetricsError {
	return &MetricsError{
		Status:    "error",
		Error:     err.Error(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// getMetricNames extracts metric names from metric families for logging
func getMetricNames(families map[string]*dto.MetricFamily) []string {
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	return names
}
