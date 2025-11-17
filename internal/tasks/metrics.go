package tasks

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"go.uber.org/zap"
	"github.com/stone-age-io/agent/internal/utils"
)

// SystemMetrics represents system metrics collected from Prometheus exporters
type SystemMetrics struct {
	CPUUsagePercent float64       `json:"cpu_usage_percent"`
	MemoryFreeGB    float64       `json:"memory_free_gb"`
	Disks           []DiskMetrics `json:"disks"` // All drives detected on system
	Timestamp       string        `json:"timestamp"`
}

// DiskMetrics represents metrics for a single disk drive
type DiskMetrics struct {
	Drive            string  `json:"drive"`               // Drive letter (C:, D:) or mount point (/, /home)
	FreePercent      float64 `json:"free_percent"`        // Percentage of free space
	FreeGB           float64 `json:"free_gb"`             // Free space in GB
	TotalGB          float64 `json:"total_gb"`            // Total space in GB
	ReadBytesPerSec  float64 `json:"read_bytes_per_sec"`  // Read rate (requires previous measurement)
	WriteBytesPerSec float64 `json:"write_bytes_per_sec"` // Write rate (requires previous measurement)
}

// createHTTPClient creates an HTTP client with appropriate timeouts for metrics scraping
// This client is created ONCE and reused for all scrapes for efficiency
func createHTTPClient() *http.Client {
	return &http.Client{
		// Overall request timeout (connection + headers + body read)
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Time to establish TCP connection
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
			DisableKeepAlives:   false,
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// ScrapeMetrics fetches and parses metrics from the appropriate exporter
func (e *Executor) ScrapeMetrics(exporterURL string) (*SystemMetrics, error) {
	e.logger.Debug("Starting metrics scrape",
		zap.String("url", exporterURL),
		zap.String("platform", runtime.GOOS),
		zap.String("exporter", GetExporterName()))

	// Create context with timeout for additional safety
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, "GET", exporterURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set User-Agent for identification
	req.Header.Set("User-Agent", "stone-age-agent/1.0")

	// Execute request using cached HTTP client
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
// Now platform-agnostic using GetMetricNames()
func (e *Executor) parsePrometheusMetrics(reader io.Reader) (*SystemMetrics, error) {
	// Use NewDecoder with FmtText format for proper initialization
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

	// Get platform-specific metric names
	metricNames := GetMetricNames()

	// Debug: Log available metric families on first scrape
	e.metricsCache.mu.RLock()
	isFirstScrape := e.metricsCache.lastTimestamp.IsZero()
	e.metricsCache.mu.RUnlock()

	if isFirstScrape {
		e.logger.Debug("Available metric families",
			zap.Int("count", len(metricFamilies)),
			zap.Strings("names", getMetricNames(metricFamilies)))
	}

	metrics := &SystemMetrics{}

	// Extract CPU usage using platform-specific metric name
	cpuFound := false

	if family, ok := metricFamilies[metricNames.CPUTime]; ok {
		var totalTime, idleTime float64

		// Sum across ALL cores and ALL modes
		for _, m := range family.Metric {
			mode := getLabelValue(m.Label, "mode")

			if m.Counter != nil {
				value := m.Counter.GetValue()
				totalTime += value // Add all time from all modes and cores

				// Use platform-specific idle label
				if mode == metricNames.CPUIdleLabel {
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

	// Extract memory free bytes using platform-specific metric name
	memoryFound := false

	// Platform-specific memory handling
	switch runtime.GOOS {
	case "linux":
		// Linux: Try MemAvailable first (preferred), then MemFree as fallback
		if family, ok := metricFamilies["node_memory_MemAvailable_bytes"]; ok {
			if len(family.Metric) > 0 && family.Metric[0].Gauge != nil {
				bytes := family.Metric[0].Gauge.GetValue()
				metrics.MemoryFreeGB = utils.Round(bytes / 1024 / 1024 / 1024)
				memoryFound = true
			}
		} else if family, ok := metricFamilies["node_memory_MemFree_bytes"]; ok {
			// Fallback to MemFree
			if len(family.Metric) > 0 && family.Metric[0].Gauge != nil {
				bytes := family.Metric[0].Gauge.GetValue()
				metrics.MemoryFreeGB = utils.Round(bytes / 1024 / 1024 / 1024)
				memoryFound = true
				e.logger.Debug("Using MemFree fallback (MemAvailable not found)")
			}
		}
	default:
		// Windows, FreeBSD use single metric
		if family, ok := metricFamilies[metricNames.MemoryFree]; ok {
			if len(family.Metric) > 0 && family.Metric[0].Gauge != nil {
				bytes := family.Metric[0].Gauge.GetValue()
				metrics.MemoryFreeGB = utils.Round(bytes / 1024 / 1024 / 1024)
				memoryFound = true
			}
		}
	}

	// Extract disk metrics for ALL drives (automatic discovery)
	// Build a map of drive -> metrics for easier lookup
	diskData := make(map[string]*DiskMetrics)

	// Collect free space for all drives using platform-specific metric name
	if family, ok := metricFamilies[metricNames.DiskFreeBytes]; ok {
		for _, m := range family.Metric {
			volume := getLabelValue(m.Label, metricNames.VolumeLabel)
			if volume != "" && m.Gauge != nil {
				if diskData[volume] == nil {
					diskData[volume] = &DiskMetrics{Drive: volume}
				}
				diskData[volume].FreeGB = utils.Round(m.Gauge.GetValue() / 1024 / 1024 / 1024)
			}
		}
	}

	// Collect total size for all drives using platform-specific metric name
	if family, ok := metricFamilies[metricNames.DiskSizeBytes]; ok {
		for _, m := range family.Metric {
			volume := getLabelValue(m.Label, metricNames.VolumeLabel)
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

	// Extract disk I/O rates for ALL drives using platform-specific metric names
	now := time.Now()

	// Lock for disk I/O cache operations
	e.metricsCache.mu.Lock()

	if !e.metricsCache.lastTimestamp.IsZero() {
		timeDelta := now.Sub(e.metricsCache.lastTimestamp).Seconds()

		if timeDelta > 0 {
			// Read bytes for all drives
			if family, ok := metricFamilies[metricNames.DiskReadBytes]; ok {
				for _, m := range family.Metric {
					volume := getLabelValue(m.Label, metricNames.VolumeLabel)
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
						counters := e.metricsCache.lastDiskMetrics[volume]
						counters.ReadBytes = currentRead
						e.metricsCache.lastDiskMetrics[volume] = counters
					}
				}
			}

			// Write bytes for all drives
			if family, ok := metricFamilies[metricNames.DiskWriteBytes]; ok {
				for _, m := range family.Metric {
					volume := getLabelValue(m.Label, metricNames.VolumeLabel)
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
		if family, ok := metricFamilies[metricNames.DiskReadBytes]; ok {
			for _, m := range family.Metric {
				volume := getLabelValue(m.Label, metricNames.VolumeLabel)
				if volume != "" && m.Counter != nil {
					counters := e.metricsCache.lastDiskMetrics[volume]
					counters.ReadBytes = m.Counter.GetValue()
					e.metricsCache.lastDiskMetrics[volume] = counters
				}
			}
		}

		if family, ok := metricFamilies[metricNames.DiskWriteBytes]; ok {
			for _, m := range family.Metric {
				volume := getLabelValue(m.Label, metricNames.VolumeLabel)
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

	// Convert disk map to array for consistent output
	metrics.Disks = make([]DiskMetrics, 0, len(diskData))
	for _, disk := range diskData {
		// Only include drives with actual data
		if disk.TotalGB > 0 || disk.ReadBytesPerSec > 0 || disk.WriteBytesPerSec > 0 {
			metrics.Disks = append(metrics.Disks, *disk)
		}
	}

	// Log warnings if metrics weren't found
	if !cpuFound && !isFirstScrape {
		e.logger.Warn("CPU metric not found or could not be calculated",
			zap.String("expected_metric", metricNames.CPUTime),
			zap.Bool("has_metric", metricFamilies[metricNames.CPUTime] != nil),
			zap.Bool("is_first_scrape", isFirstScrape))
	}
	if !memoryFound {
		e.logger.Warn("Memory metric not found",
			zap.String("expected_metric", metricNames.MemoryFree),
			zap.Bool("has_metric", metricFamilies[metricNames.MemoryFree] != nil))
	}
	if len(metrics.Disks) == 0 {
		e.logger.Warn("No disk metrics found",
			zap.String("expected_free_metric", metricNames.DiskFreeBytes),
			zap.String("expected_size_metric", metricNames.DiskSizeBytes),
			zap.Bool("has_free_metric", metricFamilies[metricNames.DiskFreeBytes] != nil),
			zap.Bool("has_size_metric", metricFamilies[metricNames.DiskSizeBytes] != nil))
	}

	return metrics, nil
}

// validateMetrics performs sanity checks on metrics values
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

	// ALWAYS validate all disk metrics
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
