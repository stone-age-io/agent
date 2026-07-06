package tasks

import "time"

// serviceCommandTimeout bounds external service-control commands (systemctl,
// rc.d) so a hung service manager cannot block the NATS command handler
// indefinitely. Matches the 30s wait used by the Windows SCM implementation.
const serviceCommandTimeout = 30 * time.Second

// ServiceStatus represents the status of a system service
// This structure is shared across all platforms (Windows, Linux, FreeBSD)
type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"` // One of the ServiceStatus* constants below
}

// ServiceStatusMessage is the telemetry payload for a service check.
// Code/Location are stamped by the scheduler before publishing.
type ServiceStatusMessage struct {
	Code     string          `json:"code"`
	Location string          `json:"location"`
	Services []ServiceStatus `json:"services"`
	TS       string          `json:"ts"`
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
