//go:build windows

package tasks

import (
	"fmt"
	"net"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"go.uber.org/zap"
	"golang.org/x/sys/windows/registry"
	"win-agent/internal/utils"
)

// Inventory represents complete system inventory information
type Inventory struct {
	OS        OSInfo      `json:"os"`
	CPU       CPUInfo     `json:"cpu"`
	Memory    MemoryInfo  `json:"memory"`
	Disks     []DiskInfo  `json:"disks"`
	Network   NetworkInfo `json:"network"`
	Agent     AgentInfo   `json:"agent"`
	Timestamp string      `json:"timestamp"`
}

// OSInfo contains operating system information
type OSInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Build   string `json:"build"`
}

// CPUInfo contains CPU information
type CPUInfo struct {
	Cores int    `json:"cores"`
	Model string `json:"model"`
}

// MemoryInfo contains memory information
type MemoryInfo struct {
	TotalGB     float64 `json:"total_gb"`
	AvailableGB float64 `json:"available_gb"`
}

// DiskInfo contains disk information
type DiskInfo struct {
	Drive   string  `json:"drive"`
	TotalGB float64 `json:"total_gb"`
	FreeGB  float64 `json:"free_gb"`
}

// NetworkInfo contains network information
type NetworkInfo struct {
	PrimaryIP string `json:"primary_ip"`
}

// AgentInfo contains agent version information
type AgentInfo struct {
	Version string `json:"version"`
}

// CollectInventory gathers system inventory using only stdlib (no WMI)
func (e *Executor) CollectInventory(version string) (*Inventory, error) {
	inv := &Inventory{
		Agent:     AgentInfo{Version: version},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// Collect OS information from registry
	osInfo, err := getOSInfo()
	if err != nil {
		e.logger.Warn("Failed to collect OS info", zap.Error(err))
	} else {
		inv.OS = osInfo
	}

	// Collect CPU information
	inv.CPU = getCPUInfo()

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

// getOSInfo retrieves OS information from Windows registry
func getOSInfo() (OSInfo, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`,
		registry.QUERY_VALUE)
	if err != nil {
		return OSInfo{}, fmt.Errorf("failed to open registry key: %w", err)
	}
	defer k.Close()

	productName, _, err := k.GetStringValue("ProductName")
	if err != nil {
		productName = "Unknown"
	}

	currentVersion, _, err := k.GetStringValue("CurrentVersion")
	if err != nil {
		currentVersion = "Unknown"
	}

	currentBuild, _, err := k.GetStringValue("CurrentBuild")
	if err != nil {
		currentBuild = "Unknown"
	}

	return OSInfo{
		Name:    productName,
		Version: currentVersion,
		Build:   currentBuild,
	}, nil
}

// getCPUInfo retrieves CPU information
func getCPUInfo() CPUInfo {
	info := CPUInfo{
		Cores: runtime.NumCPU(),
		Model: "Unknown",
	}

	// Try to get CPU model from registry
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`,
		registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		if model, _, err := k.GetStringValue("ProcessorNameString"); err == nil {
			info.Model = model
		}
	}

	return info
}

// getMemoryInfo retrieves memory information using GlobalMemoryStatusEx
func getMemoryInfo() (MemoryInfo, error) {
	type memoryStatusEx struct {
		dwLength                uint32
		dwMemoryLoad            uint32
		ullTotalPhys            uint64
		ullAvailPhys            uint64
		ullTotalPageFile        uint64
		ullAvailPageFile        uint64
		ullTotalVirtual         uint64
		ullAvailVirtual         uint64
		ullAvailExtendedVirtual uint64
	}

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	globalMemoryStatusEx := kernel32.NewProc("GlobalMemoryStatusEx")

	var memStatus memoryStatusEx
	memStatus.dwLength = uint32(unsafe.Sizeof(memStatus))

	ret, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&memStatus)))
	if ret == 0 {
		return MemoryInfo{}, fmt.Errorf("GlobalMemoryStatusEx failed")
	}

	return MemoryInfo{
		TotalGB:     utils.Round(float64(memStatus.ullTotalPhys) / 1024 / 1024 / 1024),
		AvailableGB: utils.Round(float64(memStatus.ullAvailPhys) / 1024 / 1024 / 1024),
	}, nil
}

// getDiskInfo retrieves disk information for all fixed drives
func getDiskInfo() ([]DiskInfo, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")

	var disks []DiskInfo

	// Check common drive letters
	for drive := 'C'; drive <= 'Z'; drive++ {
		drivePath := fmt.Sprintf("%c:\\", drive)

		var freeBytesAvailable, totalBytes, totalFreeBytes uint64

		drivePathPtr, _ := syscall.UTF16PtrFromString(drivePath)
		ret, _, _ := getDiskFreeSpaceEx.Call(
			uintptr(unsafe.Pointer(drivePathPtr)),
			uintptr(unsafe.Pointer(&freeBytesAvailable)),
			uintptr(unsafe.Pointer(&totalBytes)),
			uintptr(unsafe.Pointer(&totalFreeBytes)),
		)

		if ret != 0 && totalBytes > 0 {
			disks = append(disks, DiskInfo{
				Drive:   fmt.Sprintf("%c:", drive),
				TotalGB: utils.Round(float64(totalBytes) / 1024 / 1024 / 1024),
				FreeGB:  utils.Round(float64(freeBytesAvailable) / 1024 / 1024 / 1024),
			})
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

	// Find first non-loopback IPv4 address
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
