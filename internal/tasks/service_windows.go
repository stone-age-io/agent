//go:build windows

package tasks

import (
	"fmt"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// ControlService manages Windows services using the Windows Service Control Manager API
func (e *Executor) ControlService(name, action string, allowedServices []string) (string, error) {
	// Validate service is in whitelist
	if !isServiceAllowed(name, allowedServices) {
		return "", fmt.Errorf("service not in allowed list: %s", name)
	}

	e.logger.Info("Controlling Windows service",
		zap.String("service", name),
		zap.String("action", action))

	// Connect to service manager
	m, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	// Open service
	s, err := m.OpenService(name)
	if err != nil {
		return "", fmt.Errorf("failed to open service %s: %w", name, err)
	}
	defer s.Close()

	// Execute action
	switch action {
	case "start":
		err = s.Start()
		if err != nil {
			return "", fmt.Errorf("failed to start service: %w", err)
		}
	case "stop":
		// Send stop control and wait for service to stop
		status, err := s.Control(svc.Stop)
		if err != nil {
			return "", fmt.Errorf("failed to stop service: %w", err)
		}
		// Wait for service to stop (with timeout)
		timeout := time.Now().Add(30 * time.Second)
		for status.State != svc.Stopped {
			if time.Now().After(timeout) {
				return "", fmt.Errorf("timeout waiting for service to stop")
			}
			time.Sleep(300 * time.Millisecond)
			status, err = s.Query()
			if err != nil {
				return "", fmt.Errorf("failed to query service status: %w", err)
			}
		}
	case "restart":
		// Stop the service first
		status, err := s.Control(svc.Stop)
		if err != nil {
			return "", fmt.Errorf("failed to stop service for restart: %w", err)
		}
		// Wait for service to stop
		timeout := time.Now().Add(30 * time.Second)
		for status.State != svc.Stopped {
			if time.Now().After(timeout) {
				return "", fmt.Errorf("timeout waiting for service to stop during restart")
			}
			time.Sleep(300 * time.Millisecond)
			status, err = s.Query()
			if err != nil {
				return "", fmt.Errorf("failed to query service status during restart: %w", err)
			}
		}
		// Start the service
		err = s.Start()
		if err != nil {
			return "", fmt.Errorf("failed to start service after stop: %w", err)
		}
	default:
		return "", fmt.Errorf("invalid action: %s (must be start, stop, or restart)", action)
	}

	return fmt.Sprintf("Service %s %s successfully", name, action), nil
}

// GetServiceStatuses retrieves status for all configured services
func (e *Executor) GetServiceStatuses(services []string) ([]ServiceStatus, error) {
	var statuses []ServiceStatus

	// Connect to service manager
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	for _, name := range services {
		status, err := e.getServiceStatus(m, name)
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

// getServiceStatus queries Windows Service Control Manager for service status
func (e *Executor) getServiceStatus(m *mgr.Mgr, name string) (*ServiceStatus, error) {
	s, err := m.OpenService(name)
	if err != nil {
		// Service doesn't exist
		return &ServiceStatus{
			Name:   name,
			Status: ServiceStatusNotInstalled,
		}, nil
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return nil, fmt.Errorf("failed to query service: %w", err)
	}

	// Map Windows service state to our standard status
	return &ServiceStatus{
		Name:   name,
		Status: mapWindowsServiceState(status.State),
	}, nil
}

// mapWindowsServiceState converts Windows service state to standard status string
func mapWindowsServiceState(state svc.State) string {
	switch state {
	case svc.Running:
		return ServiceStatusRunning
	case svc.Stopped:
		return ServiceStatusStopped
	case svc.StartPending:
		return ServiceStatusStarting
	case svc.StopPending:
		return ServiceStatusStopping
	case svc.Paused, svc.PausePending, svc.ContinuePending:
		// Treat paused services as stopped for simplicity
		return ServiceStatusStopped
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
