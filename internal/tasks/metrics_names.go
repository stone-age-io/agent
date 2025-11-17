package tasks

import (
	"runtime"
)

// MetricNames defines platform-specific Prometheus metric names
type MetricNames struct {
	CPUTime        string // Counter: total CPU time
	CPUIdleLabel   string // Label value for idle mode
	MemoryFree     string // Gauge: available memory bytes
	DiskFreeBytes  string // Gauge: disk free bytes
	DiskSizeBytes  string // Gauge: disk total size
	DiskReadBytes  string // Counter: disk read bytes
	DiskWriteBytes string // Counter: disk write bytes
	VolumeLabel    string // Label name for disk identifier
}

// GetMetricNames returns platform-specific metric names for Prometheus exporters
func GetMetricNames() MetricNames {
	switch runtime.GOOS {
	case "windows":
		return MetricNames{
			CPUTime:        "windows_cpu_time_total",
			CPUIdleLabel:   "idle",
			MemoryFree:     "windows_memory_available_bytes",
			DiskFreeBytes:  "windows_logical_disk_free_bytes",
			DiskSizeBytes:  "windows_logical_disk_size_bytes",
			DiskReadBytes:  "windows_logical_disk_read_bytes_total",
			DiskWriteBytes: "windows_logical_disk_write_bytes_total",
			VolumeLabel:    "volume", // "C:", "D:", etc.
		}
	case "linux", "freebsd":
		return MetricNames{
			CPUTime:        "node_cpu_seconds_total",
			CPUIdleLabel:   "idle",
			MemoryFree:     "node_memory_MemAvailable_bytes",
			DiskFreeBytes:  "node_filesystem_avail_bytes",
			DiskSizeBytes:  "node_filesystem_size_bytes",
			DiskReadBytes:  "node_disk_read_bytes_total",
			DiskWriteBytes: "node_disk_written_bytes_total",
			VolumeLabel:    "mountpoint", // "/", "/home", etc.
		}
	default:
		// Fallback to Linux naming for unknown platforms
		return MetricNames{
			CPUTime:        "node_cpu_seconds_total",
			CPUIdleLabel:   "idle",
			MemoryFree:     "node_memory_MemAvailable_bytes",
			DiskFreeBytes:  "node_filesystem_avail_bytes",
			DiskSizeBytes:  "node_filesystem_size_bytes",
			DiskReadBytes:  "node_disk_read_bytes_total",
			DiskWriteBytes: "node_disk_written_bytes_total",
			VolumeLabel:    "mountpoint",
		}
	}
}

// GetExporterName returns the name of the metrics exporter for documentation
func GetExporterName() string {
	switch runtime.GOOS {
	case "windows":
		return "windows_exporter"
	case "linux", "freebsd":
		return "node_exporter"
	default:
		return "prometheus_exporter"
	}
}

// GetDefaultExporterURL returns the default exporter URL
func GetDefaultExporterURL() string {
	switch runtime.GOOS {
	case "windows":
		return "http://localhost:9182/metrics" // windows_exporter default
	case "linux", "freebsd":
		return "http://localhost:9100/metrics" // node_exporter default
	default:
		return "http://localhost:9100/metrics"
	}
}
