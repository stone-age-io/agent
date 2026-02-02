package tasks

import (
	"fmt"
	"net"
	"net/http"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// Maximum age for metrics cache before reset
const maxMetricsCacheAge = 10 * time.Minute

// SystemMetrics represents system metrics collected from various sources
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

// validateMetrics performs sanity checks on metrics values
// The collector parameter is used to check if it's a first scrape (rate metrics will be 0)
func validateMetrics(m *SystemMetrics, collector MetricsCollector) error {
	// For validation, we check if CPU is 0 which indicates first scrape
	// Rate-based metrics (CPU, disk I/O) are 0 on first collection
	isFirstScrape := m.CPUUsagePercent == 0

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
// Used by the exporter collector for Prometheus metrics parsing
func getLabelValue(labels []*dto.LabelPair, name string) string {
	for _, label := range labels {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}
