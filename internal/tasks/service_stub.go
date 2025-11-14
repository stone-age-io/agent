//go:build !windows

package tasks

import (
	"fmt"
	"runtime"
)

// ServiceStatus represents the status of a Windows service
type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ControlService is a stub for non-Windows platforms
func (e *Executor) ControlService(name, action string, allowedServices []string) (string, error) {
	// Validate service is in whitelist (for testing)
	if !isServiceAllowed(name, allowedServices) {
		return "", fmt.Errorf("service not in allowed list: %s", name)
	}

	// Validate action (for testing)
	if action != "start" && action != "stop" && action != "restart" {
		return "", fmt.Errorf("invalid action: %s (must be start, stop, or restart)", action)
	}

	return "", fmt.Errorf("service control not supported on %s", runtime.GOOS)
}

// GetServiceStatuses is a stub for non-Windows platforms
func (e *Executor) GetServiceStatuses(services []string) ([]ServiceStatus, error) {
	return nil, fmt.Errorf("service status not supported on %s", runtime.GOOS)
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

// Mock state type for testing on non-Windows platforms
type State uint32

const (
	Stopped State = 1
	StartPending State = 2
	StopPending State = 3
	Running State = 4
	ContinuePending State = 5
	PausePending State = 6
	Paused State = 7
)

// stateToString converts a service state to a human-readable string
func stateToString(state State) string {
	switch state {
	case Stopped:
		return "Stopped"
	case StartPending:
		return "StartPending"
	case StopPending:
		return "StopPending"
	case Running:
		return "Running"
	case ContinuePending:
		return "ContinuePending"
	case PausePending:
		return "PausePending"
	case Paused:
		return "Paused"
	default:
		return "Unknown"
	}
}
