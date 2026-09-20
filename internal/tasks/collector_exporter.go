package tasks

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/stone-age-io/agent/internal/utils"
	"go.uber.org/zap"
)

// ExporterCollector collects metrics by scraping Prometheus exporters
type ExporterCollector struct {
	exporterURL string
	logger      *zap.Logger
	httpClient  *http.Client

	// Cache for rate calculations
	mu              sync.RWMutex
	lastTimestamp   time.Time
	lastCPUTotal    float64
	lastCPUIdle     float64
	lastDiskMetrics map[string]DiskCounters
}

// NewExporterCollector creates a collector that scrapes Prometheus exporters
func NewExporterCollector(url string, logger *zap.Logger, httpClient *http.Client) *ExporterCollector {
	return &ExporterCollector{
		exporterURL:     url,
		logger:          logger,
		httpClient:      httpClient,
		lastDiskMetrics: make(map[string]DiskCounters),
	}
}

func (c *ExporterCollector) Name() string {
	return fmt.Sprintf("exporter (%s)", c.exporterURL)
}

func (c *ExporterCollector) ResetCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastTimestamp = time.Time{}
	c.lastCPUTotal = 0
	c.lastCPUIdle = 0
	c.lastDiskMetrics = make(map[string]DiskCounters)
}

func (c *ExporterCollector) Collect(ctx context.Context) (*SystemMetrics, error) {
	c.resetCacheIfStale()

	c.logger.Debug("Starting metrics scrape",
		zap.String("url", c.exporterURL),
		zap.String("platform", runtime.GOOS),
		zap.String("exporter", GetExporterName()))

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, "GET", c.exporterURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set User-Agent for identification
	req.Header.Set("User-Agent", "stone-age-agent/1.0")

	// Execute request using HTTP client
	c.logger.Debug("Executing HTTP request", zap.String("url", c.exporterURL))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("metrics scrape timeout: %w", err)
		}
		if ctx.Err() == context.Canceled {
			return nil, fmt.Errorf("metrics scrape cancelled: %w", err)
		}
		return nil, fmt.Errorf("failed to fetch metrics: %w", err)
	}
	defer resp.Body.Close()

	c.logger.Debug("Received HTTP response",
		zap.Int("status_code", resp.StatusCode),
		zap.Int64("content_length", resp.ContentLength))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Read response body with size limit to prevent memory issues
	limitedReader := io.LimitReader(resp.Body, 10*1024*1024) // 10MB limit

	// Parse metrics using expfmt
	c.logger.Debug("Parsing Prometheus metrics")
	metrics, err := c.parsePrometheusMetrics(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metrics: %w", err)
	}

	metrics.TS = utils.NowRFC3339()

	c.logger.Debug("Metrics scrape completed successfully",
		zap.Float64("cpu_percent", metrics.CPUUsagePercent),
		zap.Float64("memory_free_gb", metrics.MemoryFreeGB),
		zap.Float64("memory_used_percent", metrics.MemoryUsedPercent),
		zap.Int("disk_count", len(metrics.Disks)))

	return metrics, nil
}

// firstGauge returns the value of the first gauge present among names, and
// whether any of them was found. An empty name never matches, so a platform
// with no fallback passes one through without a special case.
func firstGauge(families map[string]*dto.MetricFamily, names ...string) (float64, bool) {
	for _, name := range names {
		family, ok := families[name]
		if !ok || len(family.Metric) == 0 || family.Metric[0].Gauge == nil {
			continue
		}
		return family.Metric[0].Gauge.GetValue(), true
	}
	return 0, false
}

// parsePrometheusMetrics parses Prometheus format metrics using expfmt
func (c *ExporterCollector) parsePrometheusMetrics(reader io.Reader) (*SystemMetrics, error) {
	// Use NewDecoder with FmtText format for proper initialization
	decoder := expfmt.NewDecoder(reader, expfmt.NewFormat(expfmt.TypeTextPlain))

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

	c.logger.Debug("Parsed metric families", zap.Int("count", len(metricFamilies)))

	// Get platform-specific metric names
	metricNames := GetMetricNames()

	// Check if first scrape
	c.mu.RLock()
	isFirstScrape := c.lastTimestamp.IsZero()
	c.mu.RUnlock()

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
		c.mu.Lock()

		// Only calculate if we have a previous measurement
		if !c.lastTimestamp.IsZero() && totalTime > 0 && c.lastCPUTotal > 0 {
			// How much CPU time passed between measurements
			totalDelta := totalTime - c.lastCPUTotal
			idleDelta := idleTime - c.lastCPUIdle

			if totalDelta > 0 {
				// Usage = time spent NOT idle / total time
				idlePercent := (idleDelta / totalDelta) * 100
				metrics.CPUUsagePercent = utils.Round(100 - idlePercent)
				cpuFound = true

				c.logger.Debug("CPU calculated",
					zap.Float64("total_delta", totalDelta),
					zap.Float64("idle_delta", idleDelta),
					zap.Float64("idle_percent", idlePercent),
					zap.Float64("usage_percent", metrics.CPUUsagePercent))
			}
		} else if c.lastTimestamp.IsZero() {
			c.logger.Debug("CPU baseline stored (first scrape)",
				zap.Float64("total_time", totalTime),
				zap.Float64("idle_time", idleTime))
		}

		// Store current values for next time
		c.lastCPUTotal = totalTime
		c.lastCPUIdle = idleTime

		c.mu.Unlock()
	}

	// Memory: available, then installed. MemoryFreeAlt is how a platform gets a
	// second choice without a branch here — Linux prefers MemAvailable and
	// settles for MemFree — which keeps every exporter metric name in
	// metrics_names.go rather than half here and half there.
	memFreeBytes, memoryFound := firstGauge(metricFamilies, metricNames.MemoryFree, metricNames.MemoryFreeAlt)
	if memoryFound {
		metrics.MemoryFreeGB = bytesToGB(memFreeBytes)
	}

	// The total is reported when the exporter has it and omitted when it does
	// not, rather than defaulted to 0: see SystemMetrics.MemoryTotalGB.
	if memTotalBytes, ok := firstGauge(metricFamilies, metricNames.MemoryTotal); ok {
		metrics.MemoryTotalGB = bytesToGB(memTotalBytes)
	}
	metrics.deriveMemoryUsed()

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
	c.mu.Lock()

	if !c.lastTimestamp.IsZero() {
		timeDelta := now.Sub(c.lastTimestamp).Seconds()

		if timeDelta > 0 {
			// Read bytes for all drives
			if family, ok := metricFamilies[metricNames.DiskReadBytes]; ok {
				for _, m := range family.Metric {
					volume := getLabelValue(m.Label, metricNames.VolumeLabel)
					if volume != "" && m.Counter != nil {
						currentRead := m.Counter.GetValue()

						// Check if we have previous measurement for this drive
						if prevCounters, exists := c.lastDiskMetrics[volume]; exists && prevCounters.ReadBytes > 0 {
							delta := currentRead - prevCounters.ReadBytes
							if diskData[volume] == nil {
								diskData[volume] = &DiskMetrics{Drive: volume}
							}
							diskData[volume].ReadBytesPerSec = utils.Round(delta / timeDelta)
						}

						// Store current value
						counters := c.lastDiskMetrics[volume]
						counters.ReadBytes = currentRead
						c.lastDiskMetrics[volume] = counters
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
						if prevCounters, exists := c.lastDiskMetrics[volume]; exists && prevCounters.WriteBytes > 0 {
							delta := currentWrite - prevCounters.WriteBytes
							if diskData[volume] == nil {
								diskData[volume] = &DiskMetrics{Drive: volume}
							}
							diskData[volume].WriteBytesPerSec = utils.Round(delta / timeDelta)
						}

						// Store current value
						counters := c.lastDiskMetrics[volume]
						counters.WriteBytes = currentWrite
						c.lastDiskMetrics[volume] = counters
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
					counters := c.lastDiskMetrics[volume]
					counters.ReadBytes = m.Counter.GetValue()
					c.lastDiskMetrics[volume] = counters
				}
			}
		}

		if family, ok := metricFamilies[metricNames.DiskWriteBytes]; ok {
			for _, m := range family.Metric {
				volume := getLabelValue(m.Label, metricNames.VolumeLabel)
				if volume != "" && m.Counter != nil {
					counters := c.lastDiskMetrics[volume]
					counters.WriteBytes = m.Counter.GetValue()
					c.lastDiskMetrics[volume] = counters
				}
			}
		}

		c.logger.Debug("Disk I/O baseline stored for all drives, will calculate on next scrape")
	}

	c.lastTimestamp = now
	c.mu.Unlock()

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
		c.logger.Warn("CPU metric not found or could not be calculated",
			zap.String("expected_metric", metricNames.CPUTime),
			zap.Bool("has_metric", metricFamilies[metricNames.CPUTime] != nil),
			zap.Bool("is_first_scrape", isFirstScrape))
	}
	if !memoryFound {
		c.logger.Warn("Memory metric not found",
			zap.String("expected_metric", metricNames.MemoryFree),
			zap.String("fallback_metric", metricNames.MemoryFreeAlt))
	}
	if metrics.MemoryTotalGB == 0 {
		// Not a failure: the payload omits the total and the percentage rather
		// than publishing zeros. Worth a line, because an alert written against
		// memory_used_percent will silently never fire.
		c.logger.Warn("Memory total metric not found; memory_total_gb and memory_used_percent will be omitted",
			zap.String("expected_metric", metricNames.MemoryTotal))
	}
	if len(metrics.Disks) == 0 {
		c.logger.Warn("No disk metrics found",
			zap.String("expected_free_metric", metricNames.DiskFreeBytes),
			zap.String("expected_size_metric", metricNames.DiskSizeBytes),
			zap.Bool("has_free_metric", metricFamilies[metricNames.DiskFreeBytes] != nil),
			zap.Bool("has_size_metric", metricFamilies[metricNames.DiskSizeBytes] != nil))
	}

	return metrics, nil
}

func (c *ExporterCollector) resetCacheIfStale() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lastTimestamp.IsZero() {
		return
	}

	age := time.Since(c.lastTimestamp)
	if age > maxMetricsCacheAge {
		c.logger.Warn("Resetting stale metrics cache",
			zap.Duration("cache_age", age),
			zap.Duration("max_age", maxMetricsCacheAge),
			zap.String("impact", "Next metrics will have no CPU/disk I/O rates (baseline reset)"))

		c.lastTimestamp = time.Time{}
		c.lastCPUTotal = 0
		c.lastCPUIdle = 0
		c.lastDiskMetrics = make(map[string]DiskCounters)
	}
}
