package tasks

import (
	"fmt"
	"time"
)

// serviceCommandTimeout bounds external service-control commands (systemctl,
// rc.d) so a hung service manager cannot block the NATS command handler
// indefinitely. Matches the 30s wait used by the Windows SCM implementation.
const serviceCommandTimeout = 30 * time.Second

// checkServiceRequest is the cmd.service gate: the service must be allowlisted
// and the action one of start, stop or restart. Every platform calls it before
// touching its service manager. The status action never gets here -- the
// handler sends it to QueryService -- but the error names it, because the
// caller is choosing from all four.
//
// Before, Windows checked the action only after connecting to the Service
// Control Manager, which refuses a non-admin connection. So from an ordinary
// shell a bogus action came back as "Access is denied" rather than "invalid
// action", and the answer depended on who ran the agent rather than on what was
// asked. One copy here, like the cmd.exec gate, so the platforms cannot drift
// apart again.
func checkServiceRequest(name, action string, allowedServices []string) error {
	if !isServiceAllowed(name, allowedServices) {
		return fmt.Errorf("service not in allowed list: %s", name)
	}
	switch action {
	case "start", "stop", "restart":
		return nil
	}
	return fmt.Errorf("invalid action: %s (must be start, stop, restart or status)", action)
}

// QueryService answers cmd.service's status action for one service. It uses
// the same allowlist as the control actions, and reads the status through
// GetServiceStatuses -- the call the service_check telemetry makes -- so the
// command and the telemetry cannot describe the same service differently.
//
// A service that does not exist is not an error: it answers NotInstalled,
// exactly as it would in telemetry.
func (e *Executor) QueryService(name string, allowedServices []string) (*ServiceStatus, error) {
	if !isServiceAllowed(name, allowedServices) {
		return nil, fmt.Errorf("service not in allowed list: %s", name)
	}
	statuses, err := e.GetServiceStatuses([]string{name})
	if err != nil {
		return nil, err
	}
	return &statuses[0], nil
}

// isServiceAllowed checks if a service is in the allowed list
func isServiceAllowed(name string, allowedServices []string) bool {
	for _, allowed := range allowedServices {
		if name == allowed {
			return true
		}
	}
	return false
}

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
