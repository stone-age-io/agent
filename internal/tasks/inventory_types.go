package tasks

// Inventory represents a complete system inventory snapshot
// This structure is shared across all platforms
type Inventory struct {
	Agent     AgentInfo   `json:"agent"`
	OS        OSInfo      `json:"os"`
	CPU       CPUInfo     `json:"cpu"`
	Memory    MemoryInfo  `json:"memory"`
	Disks     []DiskInfo  `json:"disks"`
	Network   NetworkInfo `json:"network"`
	Timestamp string      `json:"timestamp"`
}

// AgentInfo contains information about the agent itself
type AgentInfo struct {
	Version string `json:"version"`
}

// OSInfo contains operating system information
type OSInfo struct {
	Platform string `json:"platform"` // "windows", "linux", "freebsd", etc.
	Name     string `json:"name"`     // "Windows Server 2022", "Ubuntu 24.04", "FreeBSD 14.0"
	Version  string `json:"version"`  // OS version/release
	Build    string `json:"build"`    // Build number or kernel version
}

// CPUInfo contains CPU information
type CPUInfo struct {
	Cores int    `json:"cores"` // Number of logical CPU cores
	Model string `json:"model"` // CPU model name
}

// MemoryInfo contains memory information
type MemoryInfo struct {
	TotalGB     float64 `json:"total_gb"`     // Total physical memory in GB
	AvailableGB float64 `json:"available_gb"` // Available memory in GB
}

// DiskInfo contains information about a single disk/volume
type DiskInfo struct {
	Drive   string  `json:"drive"`    // Drive letter (Windows: "C:", "D:") or mount point (Unix: "/", "/home")
	TotalGB float64 `json:"total_gb"` // Total disk space in GB
	FreeGB  float64 `json:"free_gb"`  // Free disk space in GB
}

// NetworkInfo contains network interface information
type NetworkInfo struct {
	PrimaryIP string `json:"primary_ip"` // Primary IPv4 address (non-loopback)
}

// Platform-specific implementations:
// - Windows: internal/tasks/inventory_windows.go
// - Linux:   internal/tasks/inventory_linux.go
// - FreeBSD: internal/tasks/inventory_freebsd.go
// - Stub:    internal/tasks/inventory_stub.go (for unsupported platforms)
