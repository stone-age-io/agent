//go:build !windows

package tasks

import (
	"fmt"
	"runtime"
	"time"
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

// CollectInventory is a stub for non-Windows platforms
func (e *Executor) CollectInventory(version string) (*Inventory, error) {
	return &Inventory{
		OS: OSInfo{
			Name:    runtime.GOOS,
			Version: "N/A",
			Build:   "N/A",
		},
		CPU: CPUInfo{
			Cores: runtime.NumCPU(),
			Model: "N/A",
		},
		Memory: MemoryInfo{
			TotalGB:     0,
			AvailableGB: 0,
		},
		Disks:     []DiskInfo{},
		Network:   NetworkInfo{PrimaryIP: "N/A"},
		Agent:     AgentInfo{Version: version},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}, fmt.Errorf("inventory collection not supported on %s", runtime.GOOS)
}
