//go:build freebsd

package tasks

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

// ControlService manages rc.d services on FreeBSD
func (e *Executor) ControlService(name, action string, allowedServices []string) (string, error) {
	// Validate service is in whitelist
	if !isServiceAllowed(name, allowedServices) {
		return "", fmt.Errorf("service not in allowed list: %s", name)
	}

	e.logger.Info("Controlling rc.d service",
		zap.String("service", name),
		zap.String("action", action))

	// Validate action
	switch action {
	case "start", "stop", "restart":
		// Valid actions
	default:
		return "", fmt.Errorf("invalid action: %s (must be start, stop, or restart)", action)
	}

	// Execute service command
	cmd := exec.Command("service", name, action)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		e.logger.Error("service command failed",
			zap.Error(err),
			zap.String("stderr", stderr.String()))
		return "", fmt.Errorf("service %s %s failed: %w: %s", name, action, err, stderr.String())
	}

	return fmt.Sprintf("Service %s %s successfully", name, action), nil
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

// getServiceStatus queries rc.d for service status
func (e *Executor) getServiceStatus(name string) (*ServiceStatus, error) {
	// Use 'service <name> status' for status check
	cmd := exec.Command("service", name, "status")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("service status failed: %w", err)
		}
	}

	output := strings.TrimSpace(stdout.String())

	// Parse FreeBSD service status output
	// Exit code 0 = running, 1 = not running
	var status string
	if exitCode == 0 {
		// Service is running
		status = ServiceStatusRunning
	} else if strings.Contains(output, "not running") || strings.Contains(output, "is not enabled") {
		status = ServiceStatusStopped
	} else if strings.Contains(strings.ToLower(stderr.String()), "not found") ||
		strings.Contains(strings.ToLower(output), "not exist") {
		status = ServiceStatusNotInstalled
	} else {
		status = ServiceStatusUnknown
	}

	return &ServiceStatus{
		Name:   name,
		Status: status,
	}, nil
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
