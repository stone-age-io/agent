package tasks

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"go.uber.org/zap"
	"win-agent/internal/utils"
)

// SystemMetrics represents system metrics collected from windows_exporter
type SystemMetrics struct {
	CPUUsagePercent float64       `json:"cpu_usage_percent"`
	MemoryFreeGB    float64       `json:"memory_free_gb"`
	Disks           []DiskMetrics `json:"disks"` // All drives detected on system
	Timestamp       string        `json:"timestamp"`
}

// DiskMetrics represents metrics for a single disk drive
type DiskMetrics struct {
	Drive            string  `json:"drive"`               // Drive letter (C:, D:, E:, etc.)
	FreePercent      float64 `json:"free_percent"`        // Percentage of free space
	FreeGB           float64 `json:"free_gb"`             // Free space in GB
	TotalGB          float64 `json:"total_gb"`            // Total space in GB
	ReadBytesPerSec  float64 `json:"read_bytes_per_sec"`  // Read rate (requires previous measurement)
	WriteBytesPerSec float64 `json:"write_bytes_per_sec"` // Write rate (requires previous measurement)
}

// createHTTPClient creates an HTTP client with appropriate timeouts for metrics scraping
// These timeouts prevent indefinite hangs when windows_exporter is slow or unreachable
// This client is created ONCE and reused for all scrapes for efficiency
func createHTTPClient() *http.Client {
	return &http.Client{
		// Overall request timeout (connection + headers + body read)
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Time to establish TCP connection
			// FallbackDelay helps with IPv4/IPv6 dual-stack scenarios
			DialContext: (&net.Dialer{
				Timeout:       5 * time.Second,
				KeepAlive:     30 * time.Second,
				FallbackDelay: 300 * time.Millisecond,
			}).DialContext,
			// Time to complete TLS handshake (if HTTPS)
			TLSHandshakeTimeout: 5 * time.Second,
			// Time to receive response headers
			ResponseHeaderTimeout: 10 * time.Second,
			// ENABLE connection reuse for localhost scraping efficiency
			// Since we're scraping the same local endpoint every 5 minutes,
			// reusing the connection saves TCP handshake overhead
			DisableKeepAlives: false,
			// Max idle connections
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// ScrapeMetrics fetches and parses metrics from windows_exporter
func (e *Executor) ScrapeMetrics(exporterURL string) (*SystemMetrics, error) {
	e.logger.Debug("Starting metrics scrape", zap.String("url", exporterURL))

	// Create context with timeout for additional safety
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, "GET", exporterURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set User-Agent for identification
	req.Header.Set("User-Agent", "win-agent/1.0")

	// Execute request using cached HTTP client (reuses connections)
	e.logger.Debug("Executing HTTP request", zap.String("url", exporterURL))
	resp, err := e.httpClient.Do(req)
	if err != nil {
		// Provide more context about the error
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("metrics scrape timeout after 30s: %w", err)
		}
		return nil, fmt.Errorf("failed to fetch metrics: %w", err)
	}
	defer resp.Body.Close()

	e.logger.Debug("Received HTTP response", 
		zap.Int("status_code", resp.StatusCode),
		zap.Int64("content_length", resp.ContentLength))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Read response body with size limit to prevent memory issues
	// windows_exporter typically returns 50-200KB, so 10MB is very safe
	limitedReader := io.LimitReader(resp.Body, 10*1024*1024) // 10MB limit

	// Parse metrics using expfmt
	e.logger.Debug("Parsing Prometheus metrics")
	metrics, err := e.parsePrometheusMetrics(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metrics: %w", err)
	}

	// Validate metrics before returning
	if err := e.validateMetrics(metrics); err != nil {
		return nil, fmt.Errorf("invalid metrics: %w", err)
	}

	metrics.Timestamp = time.Now().UTC().Format(time.RFC3339)
	
	e.logger.Debug("Metrics scrape completed successfully",
		zap.Float64("cpu_percent", metrics.CPUUsagePercent),
		zap.Float64("memory_free_gb", metrics.MemoryFreeGB),
		zap.Int("disk_count", len(metrics.Disks)))

	return metrics, nil
}

// parsePrometheusMetrics parses Prometheus format metrics using expfmt
func (e *Executor) parsePrometheusMetrics(reader io.Reader) (*SystemMetrics, error) {
	// Use NewDecoder with FmtText format for proper initialization
	// This ensures validation scheme is properly set
	decoder := expfmt.NewDecoder(reader, expfmt.FmtText)

	metricFamilies := make(map[string]*dto.MetricFamily)

	// Parse all metric families
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

	e.logger.Debug("Parsed metric families", zap.Int("count", len(metricFamilies)))

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

		e.metricsCache.mu.Unlock()
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

	// Extract disk metrics for ALL drives (automatic discovery)
	// Build a map of drive -> metrics for easier lookup
	diskData := make(map[string]*DiskMetrics)

	// Collect free space for all drives
	if family, ok := metricFamilies["windows_logical_disk_free_bytes"]; ok {
		for _, m := range family.Metric {
			volume := getLabelValue(m.Label, "volume")
			if volume != "" && m.Gauge != nil {
				if diskData[volume] == nil {
					diskData[volume] = &DiskMetrics{Drive: volume}
				}
				diskData[volume].FreeGB = utils.Round(m.Gauge.GetValue() / 1024 / 1024 / 1024)
			}
		}
	}

	// Collect total size for all drives
	if family, ok := metricFamilies["windows_logical_disk_size_bytes"]; ok {
		for _, m := range family.Metric {
			volume := getLabelValue(m.Label, "volume")
			if volume != "" && m.Gauge != nil {
				if diskData[volume] == nil {
					diskData[volume] = &DiskMetrics{Drive: volume}
				}
				totalBytes := m.Gauge.GetValue()
				diskData[volume].TotalGB = utils.Round(totalBytes / 1024 / 1024 / 1024)
				
				// Calculate percentage if we have both free and total
				if diskData[volume].FreeGB > 0 && totalBytes > 0 {
					freeBytes := diskData[volume].FreeGB * 1024 * 1024 * 1024
					diskData[volume].FreePercent = utils.Round((freeBytes / totalBytes) * 100)
				}
			}
		}
	}

	// Extract disk I/O rates for ALL drives (read and write)
	// Same concept as CPU - counters need two measurements to calculate rate
	now := time.Now()

	// Lock for disk I/O cache operations
	e.metricsCache.mu.Lock()
	
	if !e.metricsCache.lastTimestamp.IsZero() {
		timeDelta := now.Sub(e.metricsCache.lastTimestamp).Seconds()

		if timeDelta > 0 {
			// Read bytes for all drives
			if family, ok := metricFamilies["windows_logical_disk_read_bytes_total"]; ok {
				for _, m := range family.Metric {
					volume := getLabelValue(m.Label, "volume")
					if volume != "" && m.Counter != nil {
						currentRead := m.Counter.GetValue()
						
						// Check if we have previous measurement for this drive
						if prevCounters, exists := e.metricsCache.lastDiskMetrics[volume]; exists && prevCounters.ReadBytes > 0 {
							delta := currentRead - prevCounters.ReadBytes
							if diskData[volume] == nil {
								diskData[volume] = &DiskMetrics{Drive: volume}
							}
							diskData[volume].ReadBytesPerSec = utils.Round(delta / timeDelta)
						}
						
						// Store current value
						if e.metricsCache.lastDiskMetrics[volume].ReadBytes == 0 {
							e.metricsCache.lastDiskMetrics[volume] = DiskCounters{}
						}
						counters := e.metricsCache.lastDiskMetrics[volume]
						counters.ReadBytes = currentRead
						e.metricsCache.lastDiskMetrics[volume] = counters
					}
				}
			}

			// Write bytes for all drives
			if family, ok := metricFamilies["windows_logical_disk_write_bytes_total"]; ok {
				for _, m := range family.Metric {
					volume := getLabelValue(m.Label, "volume")
					if volume != "" && m.Counter != nil {
						currentWrite := m.Counter.GetValue()
						
						// Check if we have previous measurement for this drive
						if prevCounters, exists := e.metricsCache.lastDiskMetrics[volume]; exists && prevCounters.WriteBytes > 0 {
							delta := currentWrite - prevCounters.WriteBytes
							if diskData[volume] == nil {
								diskData[volume] = &DiskMetrics{Drive: volume}
							}
							diskData[volume].WriteBytesPerSec = utils.Round(delta / timeDelta)
						}
						
						// Store current value
						counters := e.metricsCache.lastDiskMetrics[volume]
						counters.WriteBytes = currentWrite
						e.metricsCache.lastDiskMetrics[volume] = counters
					}
				}
			}
		}
	} else {
		// First scrape - just store baseline values for all drives
		if family, ok := metricFamilies["windows_logical_disk_read_bytes_total"]; ok {
			for _, m := range family.Metric {
				volume := getLabelValue(m.Label, "volume")
				if volume != "" && m.Counter != nil {
					counters := e.metricsCache.lastDiskMetrics[volume]
					counters.ReadBytes = m.Counter.GetValue()
					e.metricsCache.lastDiskMetrics[volume] = counters
				}
			}
		}

		if family, ok := metricFamilies["windows_logical_disk_write_bytes_total"]; ok {
			for _, m := range family.Metric {
				volume := getLabelValue(m.Label, "volume")
				if volume != "" && m.Counter != nil {
					counters := e.metricsCache.lastDiskMetrics[volume]
					counters.WriteBytes = m.Counter.GetValue()
					e.metricsCache.lastDiskMetrics[volume] = counters
				}
			}
		}

		e.logger.Debug("Disk I/O baseline stored for all drives, will calculate on next scrape")
	}

	e.metricsCache.lastTimestamp = now
	e.metricsCache.mu.Unlock()

	// Convert disk map to sorted array for consistent output
	metrics.Disks = make([]DiskMetrics, 0, len(diskData))
	for _, disk := range diskData {
		// Only include drives with actual data (have either space or I/O metrics)
		if disk.TotalGB > 0 || disk.ReadBytesPerSec > 0 || disk.WriteBytesPerSec > 0 {
			metrics.Disks = append(metrics.Disks, *disk)
		}
	}

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
	if len(metrics.Disks) == 0 {
		e.logger.Warn("No disk metrics found",
			zap.Bool("has_disk_free_bytes", metricFamilies["windows_logical_disk_free_bytes"] != nil),
			zap.Bool("has_disk_size_bytes", metricFamilies["windows_logical_disk_size_bytes"] != nil))
	}

	return metrics, nil
}

// validateMetrics performs sanity checks on metrics values
// FIXED: Now validates gauge metrics (memory, disk) even on first scrape
// Only counter-based metrics (CPU, disk I/O) are skipped on first scrape
func (e *Executor) validateMetrics(m *SystemMetrics) error {
	// Check if this is the first scrape
	e.metricsCache.mu.RLock()
	isFirstScrape := e.metricsCache.lastTimestamp.IsZero()
	e.metricsCache.mu.RUnlock()

	// Validate CPU percentage (only if we have a value - not on first scrape)
	if !isFirstScrape {
		if m.CPUUsagePercent < 0 || m.CPUUsagePercent > 100 {
			return fmt.Errorf("invalid CPU usage: %.2f%% (must be 0-100)", m.CPUUsagePercent)
		}
	}

	// ALWAYS validate memory (gauge metric, not affected by first scrape)
	if m.MemoryFreeGB < 0 {
		return fmt.Errorf("invalid memory free: %.2f GB (cannot be negative)", m.MemoryFreeGB)
	}

	// ALWAYS validate all disk metrics (gauge metrics for space, counters for I/O)
	for _, disk := range m.Disks {
		// Validate space metrics (always available)
		if disk.FreePercent < 0 || disk.FreePercent > 100 {
			return fmt.Errorf("invalid disk free percent for %s: %.2f%% (must be 0-100)", disk.Drive, disk.FreePercent)
		}
		if disk.FreeGB < 0 {
			return fmt.Errorf("invalid disk free space for %s: %.2f GB (cannot be negative)", disk.Drive, disk.FreeGB)
		}
		if disk.TotalGB < 0 {
			return fmt.Errorf("invalid disk total space for %s: %.2f GB (cannot be negative)", disk.Drive, disk.TotalGB)
		}

		// Validate I/O rates (only if we have values - not on first scrape)
		if !isFirstScrape {
			if disk.ReadBytesPerSec < 0 {
				return fmt.Errorf("invalid disk read rate for %s: %.2f bytes/sec (cannot be negative)", disk.Drive, disk.ReadBytesPerSec)
			}
			if disk.WriteBytesPerSec < 0 {
				return fmt.Errorf("invalid disk write rate for %s: %.2f bytes/sec (cannot be negative)", disk.Drive, disk.WriteBytesPerSec)
			}
		}
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
