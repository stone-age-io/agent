package tasks

// ServiceStatus represents the status of a system service
// This structure is shared across all platforms (Windows, Linux, FreeBSD)
type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"` // One of the ServiceStatus* constants below
}

// Service status constants - platform-agnostic
// All platform-specific implementations should map their native statuses to these constants
const (
	// ServiceStatusRunning indicates the service is currently running
	ServiceStatusRunning = "Running"

	// ServiceStatusStopped indicates the service is stopped
	ServiceStatusStopped = "Stopped"

	// ServiceStatusStarting indicates the service is in the process of starting
	ServiceStatusStarting = "Starting"

	// ServiceStatusStopping indicates the service is in the process of stopping
	ServiceStatusStopping = "Stopping"

	// ServiceStatusError indicates the service is in an error state (e.g., failed to start)
	ServiceStatusError = "Error"

	// ServiceStatusUnknown indicates the service status could not be determined
	ServiceStatusUnknown = "Unknown"

	// ServiceStatusNotInstalled indicates the service is not installed on the system
	ServiceStatusNotInstalled = "NotInstalled"
)

// Platform-specific implementations:
// - Windows: internal/tasks/service_windows.go
// - Linux:   internal/tasks/service_linux.go
// - FreeBSD: internal/tasks/service_freebsd.go
// - Stub:    internal/tasks/service_stub.go (for unsupported platforms)
