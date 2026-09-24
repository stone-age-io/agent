package tasks

import (
	"fmt"
	"time"

	"github.com/stone-age-io/agent/internal/utils"
)

// Maximum age for metrics cache before reset
const maxMetricsCacheAge = 10 * time.Minute

// SystemMetrics represents the system metrics the builtin collector reads.
// Code/Location are stamped by the scheduler before publishing so the
// message is self-describing for any direct subscriber.
type SystemMetrics struct {
	Code            string        `json:"code"`
	Location        string        `json:"location"`
	CPUUsagePercent float64       `json:"cpu_usage_percent"`
	MemoryFreeGB    float64       `json:"memory_free_gb"`
	Disks           []DiskMetrics `json:"disks"` // All drives detected on system
	TS              string        `json:"ts"`

	// MemoryTotalGB and MemoryUsedPercent exist because free alone cannot be
	// alerted on. "memory_free_gb below 2" means something different on a 4 GB
	// gateway and a 64 GB server, so every consumer had to know the box's size
	// out of band to write a rule. DiskMetrics has carried total and percent
	// since the beginning; this is memory catching up.
	//
	// Omitted rather than zeroed if no total was read: 0 GB installed and 0%
	// used reads as an idle machine rather than as an unanswered question.
	// Same rule as the edge collector's server-derived series.
	MemoryTotalGB     float64 `json:"memory_total_gb,omitempty"`
	MemoryUsedPercent float64 `json:"memory_used_percent,omitempty"`
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

// bytesToGB converts bytes to GB, rounded the same way as every other figure
// in the payload.
func bytesToGB(b float64) float64 {
	return utils.Round(b / 1024 / 1024 / 1024)
}

// deriveMemoryUsed fills MemoryUsedPercent from the two figures the collector
// actually reads. "Used" means total minus available, where available is the
// OS's own idea of what a new process could get (MemAvailable on Linux, not
// MemFree).
//
// A zero or missing total leaves both derived fields alone, so they stay absent
// from the payload instead of claiming 0%.
func (m *SystemMetrics) deriveMemoryUsed() {
	if m.MemoryTotalGB <= 0 {
		return
	}
	used := m.MemoryTotalGB - m.MemoryFreeGB
	if used < 0 {
		used = 0
	}
	m.MemoryUsedPercent = utils.Round(used / m.MemoryTotalGB * 100)
}

// TelemetryError is the error message published on a telemetry subject when
// collection fails (metrics scrape, service check). Code/Location are stamped
// by the scheduler before publishing.
type TelemetryError struct {
	Code     string `json:"code"`
	Location string `json:"location"`
	Status   string `json:"status"`
	Error    string `json:"error"`
	TS       string `json:"ts"`
}

// CreateTelemetryError creates an error message for telemetry failures
func CreateTelemetryError(err error) *TelemetryError {
	return &TelemetryError{
		Status: "error",
		Error:  err.Error(),
		TS:     utils.NowRFC3339(),
	}
}

// validateMetrics performs sanity checks on metrics values
func validateMetrics(m *SystemMetrics) error {
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
	if m.MemoryTotalGB < 0 {
		return fmt.Errorf("invalid memory total: %.2f GB (cannot be negative)", m.MemoryTotalGB)
	}
	if m.MemoryUsedPercent < 0 || m.MemoryUsedPercent > 100 {
		return fmt.Errorf("invalid memory used: %.2f%% (must be 0-100)", m.MemoryUsedPercent)
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
