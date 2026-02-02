package tasks

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/stone-age-io/agent/internal/utils"
	"go.uber.org/zap"
)

// BuiltinCollector collects metrics using gopsutil
type BuiltinCollector struct {
	logger *zap.Logger

	// Cache for rate calculations
	mu            sync.RWMutex
	lastTimestamp time.Time
	lastCPUTimes  cpu.TimesStat
	hasCPUTimes   bool
	lastDiskIO    map[string]disk.IOCountersStat
}

// NewBuiltinCollector creates a new gopsutil-based collector
func NewBuiltinCollector(logger *zap.Logger) *BuiltinCollector {
	return &BuiltinCollector{
		logger:     logger,
		lastDiskIO: make(map[string]disk.IOCountersStat),
	}
}

func (c *BuiltinCollector) Name() string {
	return "builtin (gopsutil)"
}

func (c *BuiltinCollector) ResetCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastTimestamp = time.Time{}
	c.lastCPUTimes = cpu.TimesStat{}
	c.hasCPUTimes = false
	c.lastDiskIO = make(map[string]disk.IOCountersStat)
}

func (c *BuiltinCollector) Collect(ctx context.Context) (*SystemMetrics, error) {
	c.resetCacheIfStale()

	metrics := &SystemMetrics{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// Collect CPU
	cpuPercent, err := c.collectCPU(ctx)
	if err != nil {
		c.logger.Warn("Failed to collect CPU metrics", zap.Error(err))
	} else {
		metrics.CPUUsagePercent = cpuPercent
	}

	// Collect Memory
	memFreeGB, err := c.collectMemory(ctx)
	if err != nil {
		c.logger.Warn("Failed to collect memory metrics", zap.Error(err))
	} else {
		metrics.MemoryFreeGB = memFreeGB
	}

	// Collect Disks (space + I/O)
	diskMetrics, err := c.collectDisks(ctx)
	if err != nil {
		c.logger.Warn("Failed to collect disk metrics", zap.Error(err))
	} else {
		metrics.Disks = diskMetrics
	}

	return metrics, nil
}

func (c *BuiltinCollector) collectCPU(ctx context.Context) (float64, error) {
	// Get CPU times (all CPUs combined)
	times, err := cpu.TimesWithContext(ctx, false) // false = combined
	if err != nil {
		return 0, err
	}
	if len(times) == 0 {
		return 0, fmt.Errorf("no CPU times returned")
	}

	current := times[0]
	currentTotal := current.User + current.System + current.Idle + current.Nice +
		current.Iowait + current.Irq + current.Softirq + current.Steal
	currentIdle := current.Idle + current.Iowait

	c.mu.Lock()
	defer c.mu.Unlock()

	// First scrape - store baseline
	if !c.hasCPUTimes {
		c.lastCPUTimes = current
		c.hasCPUTimes = true
		c.lastTimestamp = time.Now()
		c.logger.Debug("CPU baseline stored (first scrape)")
		return 0, nil // Will have value on next scrape
	}

	prev := c.lastCPUTimes
	prevTotal := prev.User + prev.System + prev.Idle + prev.Nice +
		prev.Iowait + prev.Irq + prev.Softirq + prev.Steal
	prevIdle := prev.Idle + prev.Iowait

	totalDelta := currentTotal - prevTotal
	idleDelta := currentIdle - prevIdle

	// Update cache
	c.lastCPUTimes = current
	c.lastTimestamp = time.Now()

	if totalDelta <= 0 {
		return 0, nil
	}

	usagePercent := ((totalDelta - idleDelta) / totalDelta) * 100
	return utils.Round(usagePercent), nil
}

func (c *BuiltinCollector) collectMemory(ctx context.Context) (float64, error) {
	vmem, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return 0, err
	}

	// Return available memory in GB (matches existing MemoryFreeGB field)
	return utils.Round(float64(vmem.Available) / 1024 / 1024 / 1024), nil
}

func (c *BuiltinCollector) collectDisks(ctx context.Context) ([]DiskMetrics, error) {
	// Get partitions
	partitions, err := disk.PartitionsWithContext(ctx, false) // false = physical only
	if err != nil {
		return nil, err
	}

	// Get I/O counters
	ioCounters, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		c.logger.Debug("Could not get disk I/O counters", zap.Error(err))
		// Continue without I/O - we can still get space metrics
	}

	now := time.Now()
	var diskMetrics []DiskMetrics

	c.mu.Lock()
	timeDelta := now.Sub(c.lastTimestamp).Seconds()
	isFirstScrape := c.lastTimestamp.IsZero()
	c.mu.Unlock()

	for _, partition := range partitions {
		// Skip certain filesystem types
		if c.shouldSkipPartition(partition) {
			continue
		}

		// Get usage stats
		usage, err := disk.UsageWithContext(ctx, partition.Mountpoint)
		if err != nil {
			c.logger.Debug("Could not get disk usage",
				zap.String("mountpoint", partition.Mountpoint),
				zap.Error(err))
			continue
		}

		// Skip small partitions (< 1GB)
		if usage.Total < 1024*1024*1024 {
			continue
		}

		dm := DiskMetrics{
			Drive:       c.normalizeDriveName(partition.Mountpoint),
			TotalGB:     utils.Round(float64(usage.Total) / 1024 / 1024 / 1024),
			FreeGB:      utils.Round(float64(usage.Free) / 1024 / 1024 / 1024),
			FreePercent: utils.Round(float64(usage.Free) / float64(usage.Total) * 100),
		}

		// Calculate I/O rates if we have counters and previous measurements
		if ioCounters != nil && !isFirstScrape && timeDelta > 0 {
			// Find matching I/O counter (by device name)
			deviceName := c.getDeviceName(partition)
			if io, ok := ioCounters[deviceName]; ok {
				c.mu.Lock()
				if prev, exists := c.lastDiskIO[deviceName]; exists {
					readDelta := float64(io.ReadBytes - prev.ReadBytes)
					writeDelta := float64(io.WriteBytes - prev.WriteBytes)
					dm.ReadBytesPerSec = utils.Round(readDelta / timeDelta)
					dm.WriteBytesPerSec = utils.Round(writeDelta / timeDelta)
				}
				c.lastDiskIO[deviceName] = io
				c.mu.Unlock()
			}
		} else if ioCounters != nil {
			// First scrape - store baseline
			deviceName := c.getDeviceName(partition)
			if io, ok := ioCounters[deviceName]; ok {
				c.mu.Lock()
				c.lastDiskIO[deviceName] = io
				c.mu.Unlock()
			}
		}

		diskMetrics = append(diskMetrics, dm)
	}

	// Update timestamp
	c.mu.Lock()
	if c.lastTimestamp.IsZero() {
		c.lastTimestamp = now
	}
	c.mu.Unlock()

	return diskMetrics, nil
}

// shouldSkipPartition returns true if the partition should be skipped
func (c *BuiltinCollector) shouldSkipPartition(partition disk.PartitionStat) bool {
	// Skip pseudo filesystems on Linux
	skipFsTypes := map[string]bool{
		"devfs":    true,
		"devtmpfs": true,
		"tmpfs":    true,
		"squashfs": true,
		"overlay":  true,
		"proc":     true,
		"sysfs":    true,
		"cgroup":   true,
		"cgroup2":  true,
	}

	return skipFsTypes[partition.Fstype]
}

// normalizeDriveName returns consistent drive names across platforms
func (c *BuiltinCollector) normalizeDriveName(mountpoint string) string {
	if runtime.GOOS == "windows" {
		// Windows: "C:\\" -> "C:"
		if len(mountpoint) >= 2 && mountpoint[1] == ':' {
			return mountpoint[:2]
		}
	}
	// Linux/FreeBSD: keep mountpoint as-is
	return mountpoint
}

// getDeviceName extracts device name for I/O counter lookup
func (c *BuiltinCollector) getDeviceName(partition disk.PartitionStat) string {
	if runtime.GOOS == "windows" {
		// Windows: use drive letter (C:, D:)
		if len(partition.Mountpoint) >= 2 && partition.Mountpoint[1] == ':' {
			return partition.Mountpoint[:2]
		}
		return partition.Mountpoint
	}
	// Linux/FreeBSD: use device name (e.g., "sda1", "nvme0n1p1")
	// Strip /dev/ prefix
	device := partition.Device
	if strings.HasPrefix(device, "/dev/") {
		device = device[5:]
	}
	return device
}

func (c *BuiltinCollector) resetCacheIfStale() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lastTimestamp.IsZero() {
		return
	}

	age := time.Since(c.lastTimestamp)
	if age > maxMetricsCacheAge {
		c.logger.Warn("Resetting stale metrics cache",
			zap.Duration("cache_age", age),
			zap.Duration("max_age", maxMetricsCacheAge))
		c.lastTimestamp = time.Time{}
		c.lastCPUTimes = cpu.TimesStat{}
		c.hasCPUTimes = false
		c.lastDiskIO = make(map[string]disk.IOCountersStat)
	}
}
