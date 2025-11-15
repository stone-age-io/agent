package nats

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/nats-io/nats.go"
	"win-agent/internal/config"
	"win-agent/internal/tasks"
	"go.uber.org/zap"
)

// CommandHandlers manages all command subscriptions and handlers
type CommandHandlers struct {
	logger       *zap.Logger
	config       *config.Config
	deviceID     string
	subjectPrefix string
	taskExecutor *tasks.Executor
}

// NewCommandHandlers creates a new command handler manager
func NewCommandHandlers(logger *zap.Logger, cfg *config.Config, executor *tasks.Executor) *CommandHandlers {
	return &CommandHandlers{
		logger:        logger,
		config:        cfg,
		deviceID:      cfg.DeviceID,
		subjectPrefix: cfg.SubjectPrefix,
		taskExecutor:  executor,
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
	Status    string `json:"status"`
	Command   string `json:"command,omitempty"`
	Output    string `json:"output,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Error     string `json:"error,omitempty"`
	Timestamp string `json:"timestamp"`
}

type healthResponse struct {
	Status      string                 `json:"status"`
	AgentMetrics *tasks.AgentMetrics   `json:"agent_metrics"`
	Timestamp   string                 `json:"timestamp"`
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

	// Success response
	response := customExecResponse{
		Status:    "success",
		Command:   req.Command,
		Output:    output,
		ExitCode:  exitCode,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	responseBytes, _ := json.Marshal(response)
	msg.Respond(responseBytes)

	h.logger.Info("Command execution succeeded",
		zap.String("command", req.Command),
		zap.Int("exit_code", exitCode))
}

// handleHealth returns agent health and performance metrics
func (h *CommandHandlers) handleHealth(msg *nats.Msg) {
	h.logger.Debug("Received health check command")

	// Get agent metrics
	metrics := h.taskExecutor.GetAgentMetrics()

	response := healthResponse{
		Status:       "healthy",
		AgentMetrics: metrics,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}

	responseBytes, _ := json.Marshal(response)
	msg.Respond(responseBytes)

	h.logger.Debug("Sent health response",
		zap.Float64("memory_mb", metrics.MemoryUsageMB),
		zap.Int("goroutines", metrics.Goroutines))
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
