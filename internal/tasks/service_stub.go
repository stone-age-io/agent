//go:build !windows && !linux && !freebsd

package tasks

import "fmt"

// ControlService is a stub for unsupported platforms
func (e *Executor) ControlService(name, action string, allowedServices []string) (string, error) {
	return "", fmt.Errorf("service control not supported on this platform")
}

// GetServiceStatuses is a stub for unsupported platforms
func (e *Executor) GetServiceStatuses(services []string) ([]ServiceStatus, error) {
	return nil, fmt.Errorf("service status not supported on this platform")
}
