//go:build !windows && !linux && !freebsd

package tasks

import (
	"fmt"
	"runtime"
	"time"
)

// CollectInventory is a stub for unsupported platforms
func (e *Executor) CollectInventory(version string) (*Inventory, error) {
	// Return basic inventory with platform info but warn about limited support
	inv := &Inventory{
		Agent: AgentInfo{Version: version},
		OS: OSInfo{
			Platform: runtime.GOOS,
			Name:     "Unsupported Platform",
			Version:  "Unknown",
			Build:    "Unknown",
		},
		CPU: CPUInfo{
			Cores: runtime.NumCPU(),
			Model: "Unknown",
		},
		Memory: MemoryInfo{
			TotalGB:     0,
			AvailableGB: 0,
		},
		Disks:     []DiskInfo{},
		Network:   NetworkInfo{PrimaryIP: "Unknown"},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	return inv, fmt.Errorf("full inventory collection not supported on platform: %s", runtime.GOOS)
}
