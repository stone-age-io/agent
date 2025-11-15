package nats

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"win-agent/internal/config"
	"win-agent/internal/tasks"
	"go.uber.org/zap"
)

// CommandHandlers manages all command subscriptions and handlers
type CommandHandlers struct {
	logger        *zap.Logger
	config        *config.Config
	deviceID      string
	subjectPrefix string
	version       string
	taskExecutor  *tasks.Executor
	natsClient    *Client
}

// NewCommandHandlers creates a new command handler manager
func NewCommandHandlers(logger *zap.Logger, cfg *config.Config, executor *tasks.Executor, natsClient *Client, version string) *CommandHandlers {
	return &CommandHandlers{
		logger:        logger,
		config:        cfg,
		deviceID:      cfg.DeviceID,
		subjectPrefix: cfg.SubjectPrefix,
		version:       version,
		taskExecutor:  executor,
		natsClient:    natsClient,
	}
}

// handleWithRecovery wraps a command handler with panic recovery
// This prevents a panic in one command handler from crashing the entire agent
func (h *CommandHandlers) handleWithRecovery(name string, handler nats.MsgHandler) nats.MsgHandler {
	return func(msg *nats.Msg) {
		defer func() {
			if r := recover(); r != nil {
				// Log the panic with stack trace
				h.logger.Error("Panic recovered in command handler",
					zap.String("handler", name),
					zap.String("subject", msg.Subject),
					zap.Any("panic", r),
					zap.String("stack", string(debug.Stack())))

				// Send error response to caller
				response := errorResponse{
					Status:    "error",
					Error:     fmt.Sprintf("Internal error: handler panicked: %v", r),
					Timestamp: time.Now().UTC().Format(time.RFC3339),
				}
				responseBytes, _ := json.Marshal(response)
				msg.Respond(responseBytes)
			}
		}()

		// Execute the actual handler
		handler(msg)
	}
}

// SubscribeAll subscribes to all command subjects for this device
func (h *CommandHandlers) SubscribeAll(client *Client) error {
	// Subscribe to ping command with recovery
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.ping", h.subjectPrefix, h.deviceID),
		h.handleWithRecovery("ping", h.handlePing),
	); err != nil {
		return err
	}

	// Subscribe to service control command with recovery
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.service", h.subjectPrefix, h.deviceID),
		h.handleWithRecovery("service", h.handleServiceControl),
	); err != nil {
		return err
	}

	// Subscribe to log fetch command with recovery
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.logs", h.subjectPrefix, h.deviceID),
		h.handleWithRecovery("logs", h.handleLogFetch),
	); err != nil {
		return err
	}

	// Subscribe to custom exec command with recovery
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.exec", h.subjectPrefix, h.deviceID),
		h.handleWithRecovery("exec", h.handleCustomExec),
	); err != nil {
		return err
	}

	// Subscribe to health check command with recovery
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.health", h.subjectPrefix, h.deviceID),
		h.handleWithRecovery("health", h.handleHealth),
	); err != nil {
		return err
	}

	return nil
}

// Response structures

type pingResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

type serviceControlRequest struct {
	Action      string `json:"action"`
	ServiceName string `json:"service_name"`
}

type serviceControlResponse struct {
	Status      string `json:"status"`
	ServiceName string `json:"service_name,omitempty"`
	Action      string `json:"action,omitempty"`
	Result      string `json:"result,omitempty"`
	Error       string `json:"error,omitempty"`
	Timestamp   string `json:"timestamp"`
}

type logFetchRequest struct {
	LogPath string `json:"log_path"`
	Lines   int    `json:"lines"`
}

type logFetchResponse struct {
	Status     string   `json:"status"`
	LogPath    string   `json:"log_path,omitempty"`
	Lines      []string `json:"lines,omitempty"`
	TotalLines int      `json:"total_lines,omitempty"`
	Error      string   `json:"error,omitempty"`
	Timestamp  string   `json:"timestamp"`
}

type customExecRequest struct {
	Command string `json:"command"`
}

type customExecResponse struct {
	Status    string          `json:"status"`
	Command   string          `json:"command,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	ExitCode  int             `json:"exit_code,omitempty"`
	Error     string          `json:"error,omitempty"`
	Timestamp string          `json:"timestamp"`
}

// Enhanced health response structures
type healthResponse struct {
	Status    string                       `json:"status"` // "healthy", "degraded", "unhealthy"
	Timestamp string                       `json:"timestamp"`
	Agent     *tasks.AgentMetrics          `json:"agent"`
	NATS      *NATSHealth                  `json:"nats"`
	Tasks     *tasks.TaskHealthMetrics     `json:"tasks"`
	Config    *ConfigInfo                  `json:"config"`
	OS        *tasks.OSInfo                `json:"os"` // Operating system information
}

type NATSHealth struct {
	Connected  bool   `json:"connected"`
	ServerURL  string `json:"server_url,omitempty"`
	ServerID   string `json:"server_id,omitempty"`
	Reconnects uint64 `json:"reconnects"`
	InMsgs     uint64 `json:"in_msgs"`
	OutMsgs    uint64 `json:"out_msgs"`
	InBytes    uint64 `json:"in_bytes"`
	OutBytes   uint64 `json:"out_bytes"`
}

type ConfigInfo struct {
	DeviceID      string   `json:"device_id"`
	SubjectPrefix string   `json:"subject_prefix"`
	Version       string   `json:"version"`
	EnabledTasks  []string `json:"enabled_tasks"`
}

type errorResponse struct {
	Status    string `json:"status"`
	Error     string `json:"error"`
	Timestamp string `json:"timestamp"`
}

// handlePing responds to ping commands
func (h *CommandHandlers) handlePing(msg *nats.Msg) {
	h.logger.Debug("Received ping command")

	response := pingResponse{
		Status:    "pong",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	responseBytes, _ := json.Marshal(response)
	msg.Respond(responseBytes)

	h.logger.Debug("Sent pong response")
}

// handleServiceControl processes service start/stop/restart commands
func (h *CommandHandlers) handleServiceControl(msg *nats.Msg) {
	h.logger.Debug("Received service control command")

	// Parse request
	var req serviceControlRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		h.logger.Error("Failed to parse service control request", zap.Error(err))
		h.respondError(msg, "Invalid request format")
		h.taskExecutor.RecordCommandError(err)
		return
	}

	h.logger.Info("Processing service control",
		zap.String("action", req.Action),
		zap.String("service", req.ServiceName))

	// Execute service control
	result, err := h.taskExecutor.ControlService(req.ServiceName, req.Action, h.config.Commands.AllowedServices)
	if err != nil {
		h.logger.Error("Service control failed",
			zap.Error(err),
			zap.String("service", req.ServiceName),
			zap.String("action", req.Action))

		h.taskExecutor.RecordCommandError(err)

		response := serviceControlResponse{
			Status:    "error",
			Error:     err.Error(),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
		responseBytes, _ := json.Marshal(response)
		msg.Respond(responseBytes)
		return
	}

	h.taskExecutor.RecordCommandSuccess()

	// Success response
	response := serviceControlResponse{
		Status:      "success",
		ServiceName: req.ServiceName,
		Action:      req.Action,
		Result:      result,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}

	responseBytes, _ := json.Marshal(response)
	msg.Respond(responseBytes)

	h.logger.Info("Service control succeeded",
		zap.String("service", req.ServiceName),
		zap.String("action", req.Action))
}

// handleLogFetch retrieves log file contents
func (h *CommandHandlers) handleLogFetch(msg *nats.Msg) {
	h.logger.Debug("Received log fetch command")

	// Parse request
	var req logFetchRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		h.logger.Error("Failed to parse log fetch request", zap.Error(err))
		h.respondError(msg, "Invalid request format")
		h.taskExecutor.RecordCommandError(err)
		return
	}

	h.logger.Info("Fetching log file",
		zap.String("path", req.LogPath),
		zap.Int("lines", req.Lines))

	// Fetch log lines
	lines, err := h.taskExecutor.FetchLogLines(req.LogPath, req.Lines, h.config.Commands.AllowedLogPaths)
	if err != nil {
		h.logger.Error("Log fetch failed",
			zap.Error(err),
			zap.String("path", req.LogPath))

		h.taskExecutor.RecordCommandError(err)

		response := logFetchResponse{
			Status:    "error",
			Error:     err.Error(),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
		responseBytes, _ := json.Marshal(response)
		msg.Respond(responseBytes)
		return
	}

	h.taskExecutor.RecordCommandSuccess()

	// Success response
	response := logFetchResponse{
		Status:     "success",
		LogPath:    req.LogPath,
		Lines:      lines,
		TotalLines: len(lines),
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}

	responseBytes, _ := json.Marshal(response)
	msg.Respond(responseBytes)

	h.logger.Info("Log fetch succeeded",
		zap.String("path", req.LogPath),
		zap.Int("lines", len(lines)))
}

// handleCustomExec executes whitelisted PowerShell commands
func (h *CommandHandlers) handleCustomExec(msg *nats.Msg) {
	h.logger.Debug("Received custom exec command")

	// Parse request
	var req customExecRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		h.logger.Error("Failed to parse exec request", zap.Error(err))
		h.respondError(msg, "Invalid request format")
		h.taskExecutor.RecordCommandError(err)
		return
	}

	h.logger.Info("Executing custom command", zap.String("command", req.Command))

	// Execute command with configured timeout
	output, exitCode, err := h.taskExecutor.ExecuteCommand(
		req.Command,
		h.config.Commands.AllowedCommands,
		h.config.Commands.Timeout,
	)
	if err != nil {
		h.logger.Error("Command execution failed",
			zap.Error(err),
			zap.String("command", req.Command))

		h.taskExecutor.RecordCommandError(err)

		response := customExecResponse{
			Status:    "error",
			Error:     err.Error(),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
		responseBytes, _ := json.Marshal(response)
		msg.Respond(responseBytes)
		return
	}

	h.taskExecutor.RecordCommandSuccess()

	// Prepare output for response
	// IMPROVED: Always try to parse as JSON first, regardless of first character
	// This prevents false positives like "[ERROR] message" being treated as JSON
	var outputData json.RawMessage
	trimmedOutput := strings.TrimSpace(output)
	
	// Try to parse as JSON
	var testJSON interface{}
	if len(trimmedOutput) > 0 && json.Unmarshal([]byte(trimmedOutput), &testJSON) == nil {
		// Valid JSON - include as-is (will be parsed object in response)
		outputData = json.RawMessage(trimmedOutput)
		h.logger.Debug("Command output is valid JSON, including as parsed object")
	} else {
		// Not valid JSON (or empty) - encode as string
		jsonStr, _ := json.Marshal(output)
		outputData = json.RawMessage(jsonStr)
		h.logger.Debug("Command output is plain text, encoding as JSON string")
	}

	// Success response
	response := customExecResponse{
		Status:    "success",
		Command:   req.Command,
		Output:    outputData,
		ExitCode:  exitCode,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	responseBytes, _ := json.Marshal(response)
	msg.Respond(responseBytes)

	h.logger.Info("Command execution succeeded",
		zap.String("command", req.Command),
		zap.Int("exit_code", exitCode))
}

// handleHealth returns enhanced agent health information
func (h *CommandHandlers) handleHealth(msg *nats.Msg) {
	h.logger.Debug("Received health check command")

	// Get agent metrics
	agentMetrics := h.taskExecutor.GetAgentMetrics()

	// Get task metrics
	taskMetrics := h.taskExecutor.GetTaskMetrics()

	// Get NATS connection health
	natsHealth := h.getNATSHealth()

	// Get config info
	configInfo := h.getConfigInfo()

	// Get OS information
	osInfo := h.getOSInfo()

	// Determine overall health status
	status := h.determineHealthStatus(natsHealth, taskMetrics)

	response := healthResponse{
		Status:    status,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Agent:     agentMetrics,
		NATS:      natsHealth,
		Tasks:     taskMetrics,
		Config:    configInfo,
		OS:        osInfo,
	}

	responseBytes, _ := json.Marshal(response)
	msg.Respond(responseBytes)

	h.logger.Debug("Sent health response",
		zap.String("status", status),
		zap.Float64("memory_mb", agentMetrics.MemoryUsageMB),
		zap.Int("goroutines", agentMetrics.Goroutines),
		zap.String("platform", osInfo.Platform))
}

// getNATSHealth collects NATS connection health information
func (h *CommandHandlers) getNATSHealth() *NATSHealth {
	stats := h.natsClient.Stats()
	
	health := &NATSHealth{
		Connected:  h.natsClient.IsConnected(),
		Reconnects: uint64(stats.Reconnects),
		InMsgs:     stats.InMsgs,
		OutMsgs:    stats.OutMsgs,
		InBytes:    stats.InBytes,
		OutBytes:   stats.OutBytes,
	}

	// Add server info if connected
	if health.Connected {
		health.ServerURL = h.natsClient.conn.ConnectedUrl()
		health.ServerID = h.natsClient.conn.ConnectedServerId()
	}

	return health
}

// getConfigInfo returns configuration summary
func (h *CommandHandlers) getConfigInfo() *ConfigInfo {
	enabledTasks := []string{}
	
	if h.config.Tasks.Heartbeat.Enabled {
		enabledTasks = append(enabledTasks, "heartbeat")
	}
	if h.config.Tasks.SystemMetrics.Enabled {
		enabledTasks = append(enabledTasks, "system_metrics")
	}
	if h.config.Tasks.ServiceCheck.Enabled {
		enabledTasks = append(enabledTasks, "service_check")
	}
	if h.config.Tasks.Inventory.Enabled {
		enabledTasks = append(enabledTasks, "inventory")
	}

	return &ConfigInfo{
		DeviceID:      h.deviceID,
		SubjectPrefix: h.subjectPrefix,
		Version:       h.version,
		EnabledTasks:  enabledTasks,
	}
}

// getOSInfo returns operating system information
// This provides immediate platform detection without requiring JetStream queries
func (h *CommandHandlers) getOSInfo() *tasks.OSInfo {
	osInfo, err := tasks.GetOSInfo()
	if err != nil {
		h.logger.Warn("Failed to get OS info for health check", zap.Error(err))
		// Return basic info with at least the platform
		return &tasks.OSInfo{
			Name:     "Unknown",
			Version:  "Unknown",
			Build:    "Unknown",
			Platform: runtime.GOOS,
		}
	}
	return osInfo
}

// determineHealthStatus calculates overall health status
func (h *CommandHandlers) determineHealthStatus(natsHealth *NATSHealth, taskMetrics *tasks.TaskHealthMetrics) string {
	// UNHEALTHY: NATS disconnected
	if !natsHealth.Connected {
		return "unhealthy"
	}

	// DEGRADED: High reconnect count (connection unstable)
	if natsHealth.Reconnects > 10 {
		return "degraded"
	}

	// DEGRADED: High metrics failure rate (>50% failures)
	// Only check if we have enough samples to be meaningful
	if taskMetrics.MetricsCount > 0 {
		failureRate := float64(taskMetrics.MetricsFailures) / float64(taskMetrics.MetricsCount)
		if failureRate > 0.5 {
			return "degraded"
		}
	}

	// HEALTHY: NATS connected and stable
	return "healthy"
}

// respondError sends a generic error response
func (h *CommandHandlers) respondError(msg *nats.Msg, errorMsg string) {
	response := errorResponse{
		Status:    "error",
		Error:     errorMsg,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	responseBytes, _ := json.Marshal(response)
	msg.Respond(responseBytes)
}
