//go:build freebsd

package tasks

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stone-age-io/agent/internal/utils"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// CollectInventory gathers system inventory using sysctl and stdlib
func (e *Executor) CollectInventory(version string) (*Inventory, error) {
	inv := &Inventory{
		Agent:     AgentInfo{Version: version},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// Collect OS information
	osInfo, err := GetOSInfo()
	if err != nil {
		e.logger.Warn("Failed to collect OS info", zap.Error(err))
	} else {
		inv.OS = *osInfo
	}

	// Collect CPU information
	cpuInfo, err := getCPUInfo()
	if err != nil {
		e.logger.Warn("Failed to collect CPU info", zap.Error(err))
	} else {
		inv.CPU = cpuInfo
	}

	// Collect memory information
	memInfo, err := getMemoryInfo()
	if err != nil {
		e.logger.Warn("Failed to collect memory info", zap.Error(err))
	} else {
		inv.Memory = memInfo
	}

	// Collect disk information
	diskInfo, err := getDiskInfo()
	if err != nil {
		e.logger.Warn("Failed to collect disk info", zap.Error(err))
	} else {
		inv.Disks = diskInfo
	}

	// Collect network information
	netInfo, err := getNetworkInfo()
	if err != nil {
		e.logger.Warn("Failed to collect network info", zap.Error(err))
	} else {
		inv.Network = netInfo
	}

	return inv, nil
}

// GetOSInfo retrieves OS information using uname
func GetOSInfo() (*OSInfo, error) {
	info := &OSInfo{
		Platform: runtime.GOOS, // "freebsd"
	}

	var utsname unix.Utsname
	if err := unix.Uname(&utsname); err != nil {
		return nil, fmt.Errorf("uname failed: %w", err)
	}

	// Convert []byte to string (unix.Utsname uses byte arrays)
	bytesToString := func(arr []byte) string {
		// Find null terminator
		n := 0
		for n < len(arr) && arr[n] != 0 {
			n++
		}
		return string(arr[:n])
	}

	info.Name = "FreeBSD"
	info.Version = bytesToString(utsname.Release[:])
	info.Build = bytesToString(utsname.Version[:])

	return info, nil
}

// getCPUInfo retrieves CPU information using sysctl
func getCPUInfo() (CPUInfo, error) {
	info := CPUInfo{
		Cores: runtime.NumCPU(),
		Model: "Unknown",
	}

	// Get CPU model from sysctl
	model, err := sysctlString("hw.model")
	if err == nil {
		info.Model = model
	}

	return info, nil
}

// getMemoryInfo retrieves memory information using sysctl
func getMemoryInfo() (MemoryInfo, error) {
	// Get total physical memory
	physmem, err := sysctlUint64("hw.physmem")
	if err != nil {
		return MemoryInfo{}, fmt.Errorf("failed to get hw.physmem: %w", err)
	}

	// Get page size
	pageSize, err := sysctlUint64("hw.pagesize")
	if err != nil {
		pageSize = 4096 // default
	}

	// Get free pages
	freePages, err := sysctlUint64("vm.stats.vm.v_free_count")
	if err != nil {
		return MemoryInfo{}, fmt.Errorf("failed to get vm.stats.vm.v_free_count: %w", err)
	}

	freeBytes := freePages * pageSize

	return MemoryInfo{
		TotalGB:     utils.Round(float64(physmem) / 1024 / 1024 / 1024),
		AvailableGB: utils.Round(float64(freeBytes) / 1024 / 1024 / 1024),
	}, nil
}

// getDiskInfo retrieves disk information for mounted filesystems
func getDiskInfo() ([]DiskInfo, error) {
	// Use mount command to get mounted filesystems
	cmd := exec.Command("mount")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("mount command failed: %w", err)
	}

	var disks []DiskInfo
	seen := make(map[string]bool)

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		// Format: device on mountpoint (fstype, options)
		if fields[1] != "on" {
			continue
		}

		device := fields[0]
		mountpoint := fields[2]

		// Skip special filesystems and only process /dev/ devices
		if strings.HasPrefix(device, "/dev/") && !seen[mountpoint] {
			var stat syscall.Statfs_t
			if err := syscall.Statfs(mountpoint, &stat); err == nil {
				// FreeBSD: Bavail is int64, need to handle sign and convert properly
				totalBytes := uint64(stat.Blocks) * uint64(stat.Bsize)
				
				// Handle potential negative Bavail (shouldn't happen, but be safe)
				var freeBytes uint64
				if stat.Bavail >= 0 {
					freeBytes = uint64(stat.Bavail) * uint64(stat.Bsize)
				} else {
					freeBytes = 0
				}

				// Only include disks > 1GB
				if totalBytes > 1024*1024*1024 {
					disks = append(disks, DiskInfo{
						Drive:   mountpoint,
						TotalGB: utils.Round(float64(totalBytes) / 1024 / 1024 / 1024),
						FreeGB:  utils.Round(float64(freeBytes) / 1024 / 1024 / 1024),
					})
					seen[mountpoint] = true
				}
			}
		}
	}

	if len(disks) == 0 {
		return nil, fmt.Errorf("no disks found")
	}

	return disks, nil
}

// getNetworkInfo retrieves primary network interface IP
func getNetworkInfo() (NetworkInfo, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return NetworkInfo{}, fmt.Errorf("failed to get network interfaces: %w", err)
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				return NetworkInfo{
					PrimaryIP: ipnet.IP.String(),
				}, nil
			}
		}
	}

	return NetworkInfo{PrimaryIP: "Unknown"}, nil
}

// Helper functions for sysctl

func sysctlString(name string) (string, error) {
	cmd := exec.Command("sysctl", "-n", name)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func sysctlUint64(name string) (uint64, error) {
	str, err := sysctlString(name)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(str, 10, 64)
}
