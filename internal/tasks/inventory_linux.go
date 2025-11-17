//go:build linux

package tasks

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/stone-age-io/agent/internal/utils"
	"go.uber.org/zap"
)

// CollectInventory gathers system inventory using stdlib
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

// GetOSInfo retrieves OS information from /etc/os-release and uname
func GetOSInfo() (*OSInfo, error) {
	info := &OSInfo{
		Platform: runtime.GOOS, // "linux"
	}

	// Parse /etc/os-release for distribution info
	file, err := os.Open("/etc/os-release")
	if err != nil {
		// Fallback to basic info
		info.Name = "Linux"
		info.Version = "Unknown"
	} else {
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}

			key := strings.TrimSpace(parts[0])
			value := strings.Trim(strings.TrimSpace(parts[1]), `"`)

			switch key {
			case "NAME":
				info.Name = value
			case "VERSION":
				info.Version = value
			case "VERSION_ID":
				if info.Version == "" {
					info.Version = value
				}
			}
		}
	}

	// Get kernel version using uname
	var utsname syscall.Utsname
	if err := syscall.Uname(&utsname); err == nil {
		// Convert []int8 to string
		release := make([]byte, 0, len(utsname.Release))
		for _, b := range utsname.Release {
			if b == 0 {
				break
			}
			release = append(release, byte(b))
		}
		info.Build = string(release)
	}

	return info, nil
}

// getCPUInfo retrieves CPU information from /proc/cpuinfo
func getCPUInfo() (CPUInfo, error) {
	info := CPUInfo{
		Cores: runtime.NumCPU(),
		Model: "Unknown",
	}

	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return info, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				info.Model = strings.TrimSpace(parts[1])
				break // Only need one model name
			}
		}
	}

	return info, nil
}

// getMemoryInfo retrieves memory information from /proc/meminfo
func getMemoryInfo() (MemoryInfo, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return MemoryInfo{}, err
	}
	defer file.Close()

	var totalKB, availableKB uint64

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		var value uint64
		fmt.Sscanf(fields[1], "%d", &value)

		switch fields[0] {
		case "MemTotal:":
			totalKB = value
		case "MemAvailable:":
			availableKB = value
		}

		// Stop once we have both values
		if totalKB > 0 && availableKB > 0 {
			break
		}
	}

	return MemoryInfo{
		TotalGB:     utils.Round(float64(totalKB) / 1024 / 1024),
		AvailableGB: utils.Round(float64(availableKB) / 1024 / 1024),
	}, nil
}

// getDiskInfo retrieves disk information for mounted filesystems
func getDiskInfo() ([]DiskInfo, error) {
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var disks []DiskInfo
	seen := make(map[string]bool)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}

		device := fields[0]
		mountpoint := fields[1]
		fstype := fields[2]

		// Skip special filesystems
		if strings.HasPrefix(device, "/dev/") &&
			!strings.Contains(fstype, "tmpfs") &&
			!strings.Contains(fstype, "devtmpfs") &&
			!seen[mountpoint] {

			var stat syscall.Statfs_t
			if err := syscall.Statfs(mountpoint, &stat); err == nil {
				totalBytes := stat.Blocks * uint64(stat.Bsize)
				freeBytes := stat.Bavail * uint64(stat.Bsize)

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
