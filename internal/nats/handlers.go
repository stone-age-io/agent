package nats

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/stone-age-io/agent/internal/config"
	"github.com/stone-age-io/agent/internal/health"
	"github.com/stone-age-io/agent/internal/nebula"
	"github.com/stone-age-io/agent/internal/tasks"
	"github.com/stone-age-io/agent/internal/utils"
	"go.uber.org/zap"
)

// CredsRotator re-mints this agent's NATS credential on the platform and writes
// the result, reporting whether the credential on disk changed.
//
// Declared here rather than imported so this package stays clear of the
// platform's HTTP concerns. Implemented by *platform.Client.
type CredsRotator interface {
	Rotate() (bool, error)
}

// HealthReporter is the agent's readiness prober, as the health command needs
// it. Implemented by *observe.Server.
//
// Declared here rather than imported for the same reason as CredsRotator: this
// package should not grow a dependency on how the endpoint is served. What it
// wants is the latest answer, not the machinery that produced it.
type HealthReporter interface {
	Report() *health.Report
}

// CommandHandlers manages all command subscriptions and handlers
type CommandHandlers struct {
	logger        *zap.Logger
	config        *config.Config
	code          string
	subjectPrefix string
	version       string
	taskExecutor  *tasks.Executor
	natsClient    *Client
	credsRotator  CredsRotator     // nil unless this agent gets its credentials from the platform
	nebulaCtl     NebulaController // nil unless the embedded overlay is enabled
	reporter      HealthReporter   // the readiness registry, shared with /ready
}

// NebulaController is the embedded Nebula overlay, as the command handlers need
// it. Implemented by *nebula.Manager.
//
// There is deliberately no Stop or Start. Stopping the overlay from a command
// would sever the channel the command arrived on whenever NATS rides the mesh,
// and nothing could turn it back on; "turn Nebula off" is nebula.enabled: false,
// which survives a restart and leaves a trace. See docs/nebula-design.md.
type NebulaController interface {
	Sync() (bool, error)
	Restart() error
	Health() *nebula.Health
}

// NewCommandHandlers creates a new command handler manager.
// credsRotator may be nil, in which case the rotate_creds command reports that
// it is not available on this agent.
// nebulaCtl may be nil, in which case the nebula command reports that the
// overlay is not enabled on this agent.
// reporter supplies the readiness report that cmd.health answers with.
func NewCommandHandlers(logger *zap.Logger, cfg *config.Config, executor *tasks.Executor, natsClient *Client, version string, credsRotator CredsRotator, nebulaCtl NebulaController, reporter HealthReporter) *CommandHandlers {
	return &CommandHandlers{
		logger:        logger,
		config:        cfg,
		code:          cfg.Code,
		subjectPrefix: cfg.SubjectPrefix,
		version:       version,
		taskExecutor:  executor,
		natsClient:    natsClient,
		credsRotator:  credsRotator,
		nebulaCtl:     nebulaCtl,
		reporter:      reporter,
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
					Status: "error",
					Error:  fmt.Sprintf("Internal error: handler panicked: %v", r),
					TS:     utils.NowRFC3339(),
				}
				responseBytes, err := json.Marshal(response)
				if err != nil {
					h.logger.Error("Failed to marshal panic response", zap.Error(err))
					msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
					return
				}
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
		fmt.Sprintf("%s.%s.cmd.ping", h.subjectPrefix, h.code),
		h.handleWithRecovery("ping", h.handlePing),
	); err != nil {
		return err
	}

	// Subscribe to service control command with recovery
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.service", h.subjectPrefix, h.code),
		h.handleWithRecovery("service", h.handleServiceControl),
	); err != nil {
		return err
	}

	// Subscribe to log fetch command with recovery
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.logs", h.subjectPrefix, h.code),
		h.handleWithRecovery("logs", h.handleLogFetch),
	); err != nil {
		return err
	}

	// Subscribe to custom exec command with recovery
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.exec", h.subjectPrefix, h.code),
		h.handleWithRecovery("exec", h.handleCustomExec),
	); err != nil {
		return err
	}

	// Subscribe to health check command with recovery
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.health", h.subjectPrefix, h.code),
		h.handleWithRecovery("health", h.handleHealth),
	); err != nil {
		return err
	}

	// Subscribe to credential rotation command with recovery.
	// Subscribed unconditionally so an agent that is not platform-managed answers
	// with a reason instead of timing out.
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.rotate_creds", h.subjectPrefix, h.code),
		h.handleWithRecovery("rotate_creds", h.handleRotateCreds),
	); err != nil {
		return err
	}

	// Subscribe to the Nebula overlay command with recovery. Subscribed
	// unconditionally, for the same reason as rotate_creds: an agent without the
	// overlay enabled answers with a reason instead of timing out.
	if _, err := client.Subscribe(
		fmt.Sprintf("%s.%s.cmd.nebula", h.subjectPrefix, h.code),
		h.handleWithRecovery("nebula", h.handleNebula),
	); err != nil {
		return err
	}

	return nil
}

// Response structures

type pingResponse struct {
	Status string `json:"status"`
	TS     string `json:"ts"`
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
	TS          string `json:"ts"`
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
	TS         string   `json:"ts"`
}

type customExecRequest struct {
	Command string `json:"command"`
}

type customExecResponse struct {
	Status   string          `json:"status"`
	Command  string          `json:"command,omitempty"`
	Output   json.RawMessage `json:"output,omitempty"`
	ExitCode int             `json:"exit_code,omitempty"`
	Error    string          `json:"error,omitempty"`
	TS       string          `json:"ts"`
}

// Enhanced health response structures
type healthResponse struct {
	Status string                   `json:"status"` // "healthy", "degraded", "unhealthy"
	TS     string                   `json:"ts"`
	Agent  *tasks.AgentMetrics      `json:"agent"`
	NATS   *NATSHealth              `json:"nats"`
	Tasks  *tasks.TaskHealthMetrics `json:"tasks"`
	Config *ConfigInfo              `json:"config"`
	OS     *tasks.OSInfo            `json:"os"` // Operating system information

	// Nebula is absent unless the embedded overlay is enabled, so an agent
	// without it emits exactly the response it did before the feature existed.
	Nebula *nebula.Health `json:"nebula,omitempty"`

	// Checks is the readiness report — the same one /ready serves, from the same
	// registry, probed on the same schedule.
	//
	// THIS IS WHY THERE IS NO `edge` BLOCK HERE. Everything a gateway knows
	// first-hand (is the local leaf up, is the hub uplink attached, is each
	// declared bucket syncing and why not) is a registered check, so it arrives
	// in this field without a second struct to define, fill and keep in step
	// with the edge's own state. The same goes for the platform conversation and
	// the overlay: one registry decides what is wrong, and both channels report
	// it. Adding a fact to cmd.health means registering a check, which also puts
	// it on /ready and in agent_check_state — rather than writing it into three
	// places and watching them drift.
	//
	// It can be up to one probe interval stale; Checked carries the timestamp so
	// a reader can see that rather than having to know the interval.
	Checks *health.Report `json:"checks,omitempty"`
}

type NATSHealth struct {
	Connected bool `json:"connected"`

	// JetStream reports whether the last check against the connected server found
	// JetStream usable. It used to be enforced once at startup, where a failure
	// aborted the process; now that an unreachable bus no longer stops the agent
	// from starting, this is where the answer surfaces. False while disconnected.
	JetStream bool `json:"jetstream"`

	ServerURL  string `json:"server_url,omitempty"`
	ServerID   string `json:"server_id,omitempty"`
	Reconnects uint64 `json:"reconnects"`
	InMsgs     uint64 `json:"in_msgs"`
	OutMsgs    uint64 `json:"out_msgs"`
	InBytes    uint64 `json:"in_bytes"`
	OutBytes   uint64 `json:"out_bytes"`
}

// ConfigInfo is the part of the configuration worth answering questions about
// remotely.
//
// The three allowlists are here because they are the three gates, and a
// rejected command is otherwise undiagnosable without shell access to the box:
// cmd.exec, cmd.service and cmd.logs each refuse anything not named in one of
// them, and "not allowed" is the same answer whether the entry is missing or
// merely spelled differently. They are configuration, not secrets — they are
// the list of things an authenticated caller was already permitted to do.
//
// This is deliberately NOT a general config dump. A whole-config command would
// mean owning a redaction policy for every field added afterwards, where the
// cost of forgetting once is a leaked credential.
type ConfigInfo struct {
	Code          string   `json:"code"`
	Location      string   `json:"location"`
	SubjectPrefix string   `json:"subject_prefix"`
	Version       string   `json:"version"`
	EnabledTasks  []string `json:"enabled_tasks"`

	AllowedCommands []string `json:"allowed_commands"`
	AllowedServices []string `json:"allowed_services"`
	AllowedLogPaths []string `json:"allowed_log_paths"`
}

type rotateCredsResponse struct {
	Status  string `json:"status"`
	Changed bool   `json:"changed"` // false means the platform handed back the credential we already had
	Error   string `json:"error,omitempty"`
	TS      string `json:"ts"`
}

type nebulaRequest struct {
	Action string `json:"action"` // "sync" or "restart"
}

// nebulaResponse reports only that the request was accepted. See handleNebula for
// why the result is not in here.
type nebulaResponse struct {
	Status string `json:"status"`
	Action string `json:"action,omitempty"`
	Error  string `json:"error,omitempty"`
	TS     string `json:"ts"`
}

type errorResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	TS     string `json:"ts"`
}

// handlePing responds to ping commands
func (h *CommandHandlers) handlePing(msg *nats.Msg) {
	h.logger.Debug("Received ping command")

	response := pingResponse{
		Status: "pong",
		TS:     utils.NowRFC3339(),
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		h.logger.Error("Failed to marshal ping response", zap.Error(err))
		msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
		return
	}
	msg.Respond(responseBytes)

	h.logger.Debug("Sent pong response")
}

// handleRotateCreds asks the platform to re-mint this agent's NATS credential,
// writes it, and reconnects so it takes effect.
//
// The reply is sent and flushed BEFORE the reconnect: ForceReconnect drops the
// connection this reply is travelling on, so reversing the order would cost the
// caller their answer.
func (h *CommandHandlers) handleRotateCreds(msg *nats.Msg) {
	h.logger.Info("Received credential rotation command")

	if h.credsRotator == nil {
		h.logger.Warn("Credential rotation requested but this agent is not platform-managed",
			zap.String("auth_type", h.config.NATS.Auth.Type))
		h.respondError(msg, "credential rotation requires stone-age auth (this agent uses "+h.config.NATS.Auth.Type+")")
		return
	}

	changed, err := h.credsRotator.Rotate()
	if err != nil {
		h.logger.Error("Credential rotation failed", zap.Error(err))
		h.taskExecutor.RecordCommandError(err)
		h.respondRotateCreds(msg, rotateCredsResponse{
			Status: "error",
			Error:  err.Error(),
			TS:     utils.NowRFC3339(),
		})
		return
	}

	h.taskExecutor.RecordCommandSuccess()

	h.respondRotateCreds(msg, rotateCredsResponse{
		Status:  "success",
		Changed: changed,
		TS:      utils.NowRFC3339(),
	})

	if !changed {
		// The platform re-minted nothing, so the live connection is already using
		// the current credential and there is nothing to reconnect for
		h.logger.Warn("Credential rotation returned the credential already on disk")
		return
	}

	h.logger.Info("Credentials rotated, reconnecting to NATS")
	if err := h.natsClient.Flush(); err != nil {
		h.logger.Warn("Failed to flush rotation response before reconnect", zap.Error(err))
	}
	if err := h.natsClient.ForceReconnect(); err != nil {
		h.logger.Error("Failed to reconnect with rotated credentials", zap.Error(err))
	}
}

// handleNebula runs an action against the embedded overlay.
//
// THIS IS THE ONE HANDLER THAT ANSWERS BEFORE IT ACTS, and it has to be. When an
// agent's NATS endpoint lives on the overlay, both actions below interrupt the
// tunnel the request arrived through — so a reply sent afterwards would never
// land, and the caller would see a timeout on an operation that actually
// succeeded. The obvious reaction to that timeout is to send it again.
//
// So "accepted" means the request was valid and the work has started. The outcome
// is reported through cmd.health, which is the other half of why mesh state lives
// there: it is the completion channel, not just a dashboard.
//
// There is no status action — cmd.health already carries the same block, and a
// second way to ask one question is a second thing to keep in step.
func (h *CommandHandlers) handleNebula(msg *nats.Msg) {
	var req nebulaRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		h.logger.Error("Failed to parse nebula request", zap.Error(err))
		h.respondError(msg, "Invalid request format")
		h.taskExecutor.RecordCommandError(err)
		return
	}

	if h.nebulaCtl == nil {
		h.respondError(msg, "nebula is not enabled on this agent (set nebula.enabled: true)")
		return
	}

	switch req.Action {
	case "sync", "restart":
	default:
		h.respondError(msg, `action must be "sync" or "restart"`)
		return
	}

	h.logger.Info("Accepted nebula command", zap.String("action", req.Action))

	response := nebulaResponse{
		Status: "accepted",
		Action: req.Action,
		TS:     utils.NowRFC3339(),
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		h.logger.Error("Failed to marshal nebula response", zap.Error(err))
		msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
		return
	}
	msg.Respond(responseBytes)

	// Get the acceptance onto the wire before touching the overlay it may be
	// riding on.
	if err := h.natsClient.Flush(); err != nil {
		h.logger.Warn("Failed to flush nebula acceptance before acting", zap.Error(err))
	}

	// handleWithRecovery wraps the handler, not anything the handler spawns, so
	// this goroutine needs its own: a panic in a detached goroutine takes the
	// process down.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				h.logger.Error("Panic in nebula command",
					zap.String("action", req.Action),
					zap.Any("panic", r))
			}
		}()

		h.runNebulaAction(req.Action)
	}()
}

// runNebulaAction performs the work behind an accepted nebula command. Its
// outcome reaches the caller through cmd.health, so everything here is logged
// rather than returned.
func (h *CommandHandlers) runNebulaAction(action string) {
	switch action {
	case "sync":
		changed, err := h.nebulaCtl.Sync()
		if err != nil {
			h.logger.Error("Nebula sync failed", zap.Error(err))
			h.taskExecutor.RecordCommandError(err)
			return
		}
		h.taskExecutor.RecordCommandSuccess()
		h.logger.Info("Nebula sync complete", zap.Bool("changed", changed))

	case "restart":
		if err := h.nebulaCtl.Restart(); err != nil {
			h.logger.Error("Nebula restart failed", zap.Error(err))
			h.taskExecutor.RecordCommandError(err)
			return
		}
		h.taskExecutor.RecordCommandSuccess()
		h.logger.Info("Nebula restart complete")
	}
}

// respondRotateCreds marshals and sends a rotation response
func (h *CommandHandlers) respondRotateCreds(msg *nats.Msg, response rotateCredsResponse) {
	responseBytes, err := json.Marshal(response)
	if err != nil {
		h.logger.Error("Failed to marshal rotate_creds response", zap.Error(err))
		msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
		return
	}
	msg.Respond(responseBytes)
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
			Status: "error",
			Error:  err.Error(),
			TS:     utils.NowRFC3339(),
		}
		responseBytes, err := json.Marshal(response)
		if err != nil {
			h.logger.Error("Failed to marshal service control error response", zap.Error(err))
			msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
			return
		}
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
		TS:          utils.NowRFC3339(),
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		h.logger.Error("Failed to marshal service control response", zap.Error(err))
		msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
		return
	}
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
			Status: "error",
			Error:  err.Error(),
			TS:     utils.NowRFC3339(),
		}
		responseBytes, err := json.Marshal(response)
		if err != nil {
			h.logger.Error("Failed to marshal log fetch error response", zap.Error(err))
			msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
			return
		}
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
		TS:         utils.NowRFC3339(),
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		h.logger.Error("Failed to marshal log fetch response", zap.Error(err))
		msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
		return
	}
	msg.Respond(responseBytes)

	h.logger.Info("Log fetch succeeded",
		zap.String("path", req.LogPath),
		zap.Int("lines", len(lines)))
}

// handleCustomExec executes whitelisted PowerShell commands or scripts
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

	// Execute command with configured timeout and scripts directory
	output, exitCode, err := h.taskExecutor.ExecuteCommand(
		req.Command,
		h.config.Commands.AllowedCommands,
		h.config.Commands.ScriptsDirectory,
		h.config.Commands.Timeout,
	)
	if err != nil {
		h.logger.Error("Command execution failed",
			zap.Error(err),
			zap.String("command", req.Command))

		h.taskExecutor.RecordCommandError(err)

		response := customExecResponse{
			Status: "error",
			Error:  err.Error(),
			TS:     utils.NowRFC3339(),
		}
		responseBytes, err := json.Marshal(response)
		if err != nil {
			h.logger.Error("Failed to marshal exec error response", zap.Error(err))
			msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
			return
		}
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
		jsonStr, err := json.Marshal(output)
		if err != nil {
			h.logger.Error("Failed to marshal command output", zap.Error(err))
			jsonStr = []byte(`"output marshal error"`)
		}
		outputData = json.RawMessage(jsonStr)
		h.logger.Debug("Command output is plain text, encoding as JSON string")
	}

	// Success response
	response := customExecResponse{
		Status:   "success",
		Command:  req.Command,
		Output:   outputData,
		ExitCode: exitCode,
		TS:       utils.NowRFC3339(),
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		h.logger.Error("Failed to marshal exec response", zap.Error(err))
		msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
		return
	}
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

	// Overlay state, when there is an overlay
	var nebulaHealth *nebula.Health
	if h.nebulaCtl != nil {
		nebulaHealth = h.nebulaCtl.Health()
	}

	// The readiness report, which is also what decides the status below
	var report *health.Report
	if h.reporter != nil {
		report = h.reporter.Report()
	}

	status := determineHealthStatus(report)

	response := healthResponse{
		Status: status,
		TS:     utils.NowRFC3339(),
		Agent:  agentMetrics,
		NATS:   natsHealth,
		Tasks:  taskMetrics,
		Config: configInfo,
		OS:     osInfo,
		Nebula: nebulaHealth,
		Checks: report,
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		h.logger.Error("Failed to marshal health response", zap.Error(err))
		msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
		return
	}
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
		JetStream:  h.natsClient.IsJetStreamAvailable(),
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
	// The two internal jobs are reported only when they are actually scheduled.
	// Each condition mirrors the one in scheduler.scheduleTasks, and each is the
	// presence of the interface the scheduler needs rather than a config key —
	// the same test, so this list cannot claim a job that is not running.
	if h.credsRotator != nil && h.config.Platform.SyncInterval > 0 {
		enabledTasks = append(enabledTasks, "creds_sync")
	}
	if h.nebulaCtl != nil {
		enabledTasks = append(enabledTasks, "nebula_sync")
	}

	return &ConfigInfo{
		Code:            h.code,
		Location:        h.config.Location,
		SubjectPrefix:   h.subjectPrefix,
		Version:         h.version,
		EnabledTasks:    enabledTasks,
		AllowedCommands: h.config.Commands.AllowedCommands,
		AllowedServices: h.config.Commands.AllowedServices,
		AllowedLogPaths: h.config.Commands.AllowedLogPaths,
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

// determineHealthStatus maps the readiness report onto the three words this
// command has always answered with.
//
// It is a pure function of the report, and that is the point. It used to be a
// ladder of inline conditions — NATS connected, JetStream usable, reconnect
// count, metrics failure rate, overlay carrying traffic — every one of which
// was also, or should have been, a readiness check. Two lists of what "wrong"
// means drift: the edge's checks were never in this one at all, so a gateway
// with every synced bucket down answered "healthy" over NATS while its own
// /ready endpoint said otherwise.
//
// The mapping is the report's own severity order, which is why warn and fail
// are worth distinguishing in the first place:
//
//	fail  -> unhealthy   something is broken and the agent is not doing its job
//	warn  -> degraded    it works, and someone should look at it
//	ok    -> healthy
//
// A nil report means the prober has not produced one yet, which agent.New's
// ordering makes very nearly impossible — it starts the prober, synchronously,
// before the command subscriptions exist. If it happens anyway, "degraded" is
// the honest answer: a diagnostic that has not run is not a clean bill of
// health. Same rule as metrics.Set.Observe, where a nil report reads as not
// ready.
func determineHealthStatus(rep *health.Report) string {
	if rep == nil {
		return "degraded"
	}

	switch rep.State {
	case health.StateFail:
		return "unhealthy"
	case health.StateWarn:
		return "degraded"
	case health.StateOK:
		return "healthy"
	default:
		// Every check skipped. Not a clean bill of health: nothing was examined.
		return "degraded"
	}
}

// respondError sends a generic error response
func (h *CommandHandlers) respondError(msg *nats.Msg, errorMsg string) {
	response := errorResponse{
		Status: "error",
		Error:  errorMsg,
		TS:     utils.NowRFC3339(),
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		h.logger.Error("Failed to marshal error response", zap.Error(err))
		msg.Respond([]byte(`{"status":"error","error":"internal marshal failure"}`))
		return
	}
	msg.Respond(responseBytes)
}
