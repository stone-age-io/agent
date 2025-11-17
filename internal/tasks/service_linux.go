//go:build linux

package tasks

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ControlService manages systemd services on Linux
func (e *Executor) ControlService(name, action string, allowedServices []string) (string, error) {
	// Validate service is in whitelist
	if !isServiceAllowed(name, allowedServices) {
		return "", fmt.Errorf("service not in allowed list: %s", name)
	}

	e.logger.Info("Controlling systemd service",
		zap.String("service", name),
		zap.String("action", action))

	var cmd *exec.Cmd
	switch action {
	case "start":
		cmd = exec.Command("systemctl", "start", name)
	case "stop":
		cmd = exec.Command("systemctl", "stop", name)
	case "restart":
		cmd = exec.Command("systemctl", "restart", name)
	default:
		return "", fmt.Errorf("invalid action: %s (must be start, stop, or restart)", action)
	}

	// Execute systemctl command
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		e.logger.Error("systemctl command failed",
			zap.Error(err),
			zap.String("stderr", stderr.String()))
		return "", fmt.Errorf("systemctl %s %s failed: %w: %s", action, name, err, stderr.String())
	}

	// Wait briefly and verify the service reached desired state
	time.Sleep(500 * time.Millisecond)

	status, statusErr := e.getServiceStatus(name)
	if statusErr != nil {
		e.logger.Warn("Failed to verify service status after action",
			zap.Error(statusErr))
	}

	result := fmt.Sprintf("Service %s %s successfully", name, action)
	if statusErr == nil {
		result += fmt.Sprintf(" (status: %s)", status.Status)
	}

	return result, nil
}

// GetServiceStatuses retrieves status for all configured services
func (e *Executor) GetServiceStatuses(services []string) ([]ServiceStatus, error) {
	var statuses []ServiceStatus

	for _, name := range services {
		status, err := e.getServiceStatus(name)
		if err != nil {
			e.logger.Warn("Failed to get service status",
				zap.String("service", name),
				zap.Error(err))
			statuses = append(statuses, ServiceStatus{
				Name:   name,
				Status: ServiceStatusError,
			})
			continue
		}
		statuses = append(statuses, *status)
	}

	return statuses, nil
}

// getServiceStatus queries systemd for service status
func (e *Executor) getServiceStatus(name string) (*ServiceStatus, error) {
	// Use systemctl show for machine-readable output
	cmd := exec.Command("systemctl", "show", name, "--property=ActiveState,SubState,LoadState")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// Check if service is not installed
		if strings.Contains(stderr.String(), "not loaded") ||
			strings.Contains(stderr.String(), "not found") {
			return &ServiceStatus{
				Name:   name,
				Status: ServiceStatusNotInstalled,
			}, nil
		}
		return nil, fmt.Errorf("systemctl show failed: %w: %s", err, stderr.String())
	}

	// Parse output
	output := stdout.String()
	lines := strings.Split(output, "\n")

	var activeState, subState, loadState string
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "ActiveState":
			activeState = value
		case "SubState":
			subState = value
		case "LoadState":
			loadState = value
		}
	}

	// Check if service is loaded
	if loadState == "not-found" {
		return &ServiceStatus{
			Name:   name,
			Status: ServiceStatusNotInstalled,
		}, nil
	}

	// Map systemd states to our standard status
	status := mapSystemdState(activeState, subState)

	return &ServiceStatus{
		Name:   name,
		Status: status,
	}, nil
}

// mapSystemdState converts systemd ActiveState to standard status
func mapSystemdState(activeState, subState string) string {
	switch activeState {
	case "active":
		if subState == "running" {
			return ServiceStatusRunning
		}
		return ServiceStatusRunning // Other active substates (e.g., exited) still count as running
	case "inactive":
		return ServiceStatusStopped
	case "activating":
		return ServiceStatusStarting
	case "deactivating":
		return ServiceStatusStopping
	case "failed":
		return ServiceStatusError
	default:
		return ServiceStatusUnknown
	}
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
