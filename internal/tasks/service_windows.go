//go:build windows

package tasks

import (
	"fmt"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// ServiceStatus represents the status of a Windows service
type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ControlService starts, stops, or restarts a Windows service
// Only services in the allowedServices list can be controlled
func (e *Executor) ControlService(name, action string, allowedServices []string) (string, error) {
	// Validate service is in whitelist
	if !isServiceAllowed(name, allowedServices) {
		return "", fmt.Errorf("service not in allowed list: %s", name)
	}

	// Connect to service manager
	m, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	// Open the service
	s, err := m.OpenService(name)
	if err != nil {
		return "", fmt.Errorf("failed to open service %s: %w", name, err)
	}
	defer s.Close()

	// Perform the requested action
	switch action {
	case "start":
		if err := s.Start(); err != nil {
			return "", fmt.Errorf("failed to start service: %w", err)
		}
		return fmt.Sprintf("Service %s started successfully", name), nil

	case "stop":
		status, err := s.Control(svc.Stop)
		if err != nil {
			return "", fmt.Errorf("failed to stop service: %w", err)
		}
		// Wait for service to stop (with timeout)
		if err := waitForServiceState(s, svc.Stopped, 30*time.Second); err != nil {
			return "", fmt.Errorf("service did not stop in time: %w", err)
		}
		return fmt.Sprintf("Service %s stopped successfully (status: %v)", name, status.State), nil

	case "restart":
		// Stop the service first
		if _, err := s.Control(svc.Stop); err != nil {
			return "", fmt.Errorf("failed to stop service for restart: %w", err)
		}
		
		// Wait for service to stop
		if err := waitForServiceState(s, svc.Stopped, 30*time.Second); err != nil {
			return "", fmt.Errorf("service did not stop for restart: %w", err)
		}

		// Start the service
		if err := s.Start(); err != nil {
			return "", fmt.Errorf("failed to start service after restart: %w", err)
		}

		// Wait for service to start
		if err := waitForServiceState(s, svc.Running, 30*time.Second); err != nil {
			return "", fmt.Errorf("service did not start after restart: %w", err)
		}

		return fmt.Sprintf("Service %s restarted successfully", name), nil

	default:
		return "", fmt.Errorf("invalid action: %s (must be start, stop, or restart)", action)
	}
}

// GetServiceStatuses retrieves the status of all configured services
func (e *Executor) GetServiceStatuses(services []string) ([]ServiceStatus, error) {
	// Connect to service manager
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	var statuses []ServiceStatus

	for _, name := range services {
		s, err := m.OpenService(name)
		if err != nil {
			e.logger.Warn("Failed to open service",
				zap.String("service", name),
				zap.Error(err))
			statuses = append(statuses, ServiceStatus{
				Name:   name,
				Status: "Error",
			})
			continue
		}

		status, err := s.Query()
		s.Close()

		if err != nil {
			e.logger.Warn("Failed to query service",
				zap.String("service", name),
				zap.Error(err))
			statuses = append(statuses, ServiceStatus{
				Name:   name,
				Status: "Error",
			})
			continue
		}

		statuses = append(statuses, ServiceStatus{
			Name:   name,
			Status: stateToString(status.State),
		})
	}

	return statuses, nil
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

// waitForServiceState waits for a service to reach a specific state
func waitForServiceState(s *mgr.Service, targetState svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil {
			return err
		}
		
		if status.State == targetState {
			return nil
		}
		
		time.Sleep(500 * time.Millisecond)
	}
	
	return fmt.Errorf("timeout waiting for service state")
}

// stateToString converts a service state to a human-readable string
func stateToString(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "Stopped"
	case svc.StartPending:
		return "StartPending"
	case svc.StopPending:
		return "StopPending"
	case svc.Running:
		return "Running"
	case svc.ContinuePending:
		return "ContinuePending"
	case svc.PausePending:
		return "PausePending"
	case svc.Paused:
		return "Paused"
	default:
		return "Unknown"
	}
}
