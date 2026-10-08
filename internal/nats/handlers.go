package nats

import (
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/nats-io/nats.go/micro"
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

// ServiceName is the NATS micro service every agent registers as. It is a
// constant, not config, because sharing it is the point: one request to
// $SRV.PING.stone-agent is answered by every agent in the account. The prefix
// keeps another vendor's "agent" out of that list.
const ServiceName = "stone-agent"

// Error codes for the Nats-Service-Error-Code header. Two, deliberately: 400 when
// the handler refused the request itself (unparseable, an unknown action, a
// feature this agent does not have), 500 for an error from whatever did the
// work. Telling those two apart is worth a header; finer codes would need error
// types threaded through the executor, and nothing reads them.
const (
	codeBadRequest = "400"
	codeInternal   = "500"
)

// Register makes the command subjects the endpoints of one NATS micro service.
//
// The subjects do not change -- the group is {prefix}.{code}.cmd and each
// endpoint is the old suffix -- and neither do the reply bodies, so nothing that
// already calls the agent can tell. What it adds comes from the library:
// $SRV.PING, $SRV.INFO and $SRV.STATS, which list every agent in the account,
// their code, location and OS, and per-command request and error counts. The
// console cannot scrape /metrics on a box's loopback, but it can ask those.
//
// The queue group is OFF, which keeps the wire behaviour exactly as it was:
// plain subscriptions. Two agents misconfigured with the same code both act on
// a command, as before -- but $SRV.PING now shows both, where nothing did.
//
// Discovery subscribes to nine $SRV subjects. A credential without them is
// refused once and carries on: commands still answer, the agent is just absent
// from discovery, and the nats_permissions check says so.
//
// Every command is registered whether or not this agent can carry it out, so
// that rotate_creds and nebula answer with a reason instead of timing out on an
// agent that is not platform-managed or has no overlay.
func (h *CommandHandlers) Register() error {
	svc, err := micro.AddService(h.natsClient.conn, micro.Config{
		Name:        ServiceName,
		Version:     serviceVersion(h.version),
		Description: "Stone Age agent commands",
		Metadata: map[string]string{
			"code":     h.code,
			"location": h.config.Location,
			"os":       runtime.GOOS,
		},
		QueueGroupDisabled: true,
	})
	if err != nil {
		return fmt.Errorf("failed to register the %s service: %w", ServiceName, err)
	}

	commands := svc.AddGroup(fmt.Sprintf("%s.%s.cmd", h.subjectPrefix, h.code))
	for _, e := range []struct {
		name    string
		handler micro.HandlerFunc
	}{
		{"ping", h.handlePing},
		{"service", h.handleServiceControl},
		{"logs", h.handleLogFetch},
		{"exec", h.handleCustomExec},
		{"health", h.handleHealth},
		{"rotate_creds", h.handleRotateCreds},
		{"nebula", h.handleNebula},
	} {
		if err := commands.AddEndpoint(e.name, h.handleWithRecovery(e.name, e.handler)); err != nil {
			return fmt.Errorf("failed to add the %s command: %w", e.name, err)
		}
	}

	info := svc.Info()
	h.logger.Info("Registered command service",
		zap.String("service", info.Name),
		zap.String("version", info.Version),
		zap.String("id", info.ID),
		zap.String("subjects", fmt.Sprintf("%s.%s.cmd.>", h.subjectPrefix, h.code)))
	return nil
}

// semverPattern is the regular expression semver.org publishes, which is also
// the one micro validates a service version against.
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// serviceVersion is the build version in the form micro accepts.
//
// AddService refuses a version that is not semver, and the agent is not always
// built with one: a plain `go build` and `make build` both stamp "dev". Without
// this, every unreleased build would fail to start. A leading "v" is dropped,
// since a tag written as v0.3.2 is semver once it goes; anything still not
// semver after that reports as 0.0.0-dev. cmd.health carries the stamp exactly
// as it was built.
func serviceVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if semverPattern.MatchString(v) {
		return v
	}
	return "0.0.0-dev"
}

// handleWithRecovery wraps a command handler with panic recovery, so a panic in
// one command answers that command with an error instead of crashing the agent.
// micro does not recover a handler's panics itself.
func (h *CommandHandlers) handleWithRecovery(name string, handler micro.HandlerFunc) micro.HandlerFunc {
	return func(req micro.Request) {
		defer func() {
			if r := recover(); r != nil {
				h.logger.Error("Panic recovered in command handler",
					zap.String("handler", name),
					zap.String("subject", req.Subject()),
					zap.Any("panic", r),
					zap.String("stack", string(debug.Stack())))

				h.respondError(req, codeInternal, fmt.Sprintf("Internal error: handler panicked: %v", r))
			}
		}()

		handler(req)
	}
}

// Response structures

type pingResponse struct {
	Status string `json:"status"`
	TS     string `json:"ts"`
}

type serviceControlRequest struct {
	Action      string `json:"action"` // start, stop, restart or status
	ServiceName string `json:"service_name"`
}

type serviceControlResponse struct {
	Status      string `json:"status"`
	ServiceName string `json:"service_name,omitempty"`
	Action      string `json:"action,omitempty"`
	Result      string `json:"result,omitempty"`

	// ServiceStatus answers the status action, in the service_check
	// telemetry's words (Running, Stopped, NotInstalled, ...). Absent for the
	// control actions.
	ServiceStatus string `json:"service_status,omitempty"`

	Error string `json:"error,omitempty"`
	TS    string `json:"ts"`
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

// customExecResponse is built by execResponse, for every outcome.
type customExecResponse struct {
	Status  string          `json:"status"`
	Command string          `json:"command,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`

	// ExitCode is present exactly when the command ran, 0 included. Absent
	// means it never started: refused, not found, or killed by the timeout.
	ExitCode *int `json:"exit_code,omitempty"`

	Error string `json:"error,omitempty"`
	TS    string `json:"ts"`
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
func (h *CommandHandlers) handlePing(msg micro.Request) {
	h.logger.Debug("Received ping command")

	h.reply(msg, pingResponse{
		Status: "pong",
		TS:     utils.NowRFC3339(),
	})

	h.logger.Debug("Sent pong response")
}

// handleRotateCreds asks the platform to re-mint this agent's NATS credential,
// writes it, and reconnects so it takes effect.
//
// The reply is sent and flushed BEFORE the reconnect: ForceReconnect drops the
// connection this reply is travelling on, so reversing the order would cost the
// caller their answer.
func (h *CommandHandlers) handleRotateCreds(msg micro.Request) {
	h.logger.Info("Received credential rotation command")

	if h.credsRotator == nil {
		h.logger.Warn("Credential rotation requested but this agent is not platform-managed",
			zap.String("auth_type", h.config.NATS.Auth.Type))
		h.respondError(msg, codeBadRequest, "credential rotation requires stone-age auth (this agent uses "+h.config.NATS.Auth.Type+")")
		return
	}

	changed, err := h.credsRotator.Rotate()
	if err != nil {
		h.logger.Error("Credential rotation failed", zap.Error(err))
		h.taskExecutor.RecordCommandError(err)
		h.fail(msg, codeInternal, err.Error(), rotateCredsResponse{
			Status: "error",
			Error:  err.Error(),
			TS:     utils.NowRFC3339(),
		})
		return
	}

	h.taskExecutor.RecordCommandSuccess()

	h.reply(msg, rotateCredsResponse{
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
func (h *CommandHandlers) handleNebula(msg micro.Request) {
	var req nebulaRequest
	if err := json.Unmarshal(msg.Data(), &req); err != nil {
		h.logger.Error("Failed to parse nebula request", zap.Error(err))
		h.respondError(msg, codeBadRequest, "Invalid request format")
		h.taskExecutor.RecordCommandError(err)
		return
	}

	if h.nebulaCtl == nil {
		h.respondError(msg, codeBadRequest, "nebula is not enabled on this agent (set nebula.enabled: true)")
		return
	}

	switch req.Action {
	case "sync", "restart":
	default:
		h.respondError(msg, codeBadRequest, `action must be "sync" or "restart"`)
		return
	}

	h.logger.Info("Accepted nebula command", zap.String("action", req.Action))

	h.reply(msg, nebulaResponse{
		Status: "accepted",
		Action: req.Action,
		TS:     utils.NowRFC3339(),
	})

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

// marshalFailure is the reply when a reply cannot be marshalled. Every response
// type here is plain strings and numbers, so this should never be sent.
var marshalFailure = []byte(`{"status":"error","error":"internal marshal failure"}`)

// reply sends v as a successful command reply.
func (h *CommandHandlers) reply(req micro.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		h.logger.Error("Failed to marshal command reply", zap.String("subject", req.Subject()), zap.Error(err))
		h.send(req, req.Error(codeInternal, "internal marshal failure", marshalFailure))
		return
	}
	h.send(req, req.Respond(body))
}

// fail sends v as an error reply: the JSON body a caller has always received,
// plus the Nats-Service-Error and Nats-Service-Error-Code headers.
//
// EVERY REPLY WHOSE STATUS IS "error" GOES THROUGH HERE, and that is the rule
// to check in review. micro counts an error in $SRV.STATS only when the reply is
// sent with req.Error, so an error body sent through reply() would be a failure
// the service's own stats call a success.
func (h *CommandHandlers) fail(req micro.Request, code, description string, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		h.logger.Error("Failed to marshal command error reply", zap.String("subject", req.Subject()), zap.Error(err))
		code, description, body = codeInternal, "internal marshal failure", marshalFailure
	}
	h.send(req, req.Error(code, headerLine(description), body))
}

// respondError sends the generic error reply, for failures with nothing to say
// beyond the message.
func (h *CommandHandlers) respondError(req micro.Request, code, errorMsg string) {
	h.fail(req, code, errorMsg, errorResponse{
		Status: "error",
		Error:  errorMsg,
		TS:     utils.NowRFC3339(),
	})
}

// send logs a reply that could not be sent. The usual cause is a publish with
// no reply subject -- nobody is waiting -- so it is not worth more than debug.
func (h *CommandHandlers) send(req micro.Request, err error) {
	if err != nil {
		h.logger.Debug("Command reply not sent", zap.String("subject", req.Subject()), zap.Error(err))
	}
}

// headerLine makes an error message fit a header. A header value cannot span
// lines, and micro refuses an empty description; the whole message is in the
// JSON body either way.
func headerLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "error"
	}
	return line
}

// handleServiceControl processes service start/stop/restart commands
func (h *CommandHandlers) handleServiceControl(msg micro.Request) {
	h.logger.Debug("Received service control command")

	// Parse request
	var req serviceControlRequest
	if err := json.Unmarshal(msg.Data(), &req); err != nil {
		h.logger.Error("Failed to parse service control request", zap.Error(err))
		h.respondError(msg, codeBadRequest, "Invalid request format")
		h.taskExecutor.RecordCommandError(err)
		return
	}

	h.logger.Info("Processing service control",
		zap.String("action", req.Action),
		zap.String("service", req.ServiceName))

	var result, serviceStatus string
	var err error
	if req.Action == "status" {
		var status *tasks.ServiceStatus
		status, err = h.taskExecutor.QueryService(req.ServiceName, h.config.Commands.AllowedServices)
		if err == nil {
			serviceStatus = status.Status
			result = fmt.Sprintf("Service %s is %s", req.ServiceName, status.Status)
		}
	} else {
		result, err = h.taskExecutor.ControlService(req.ServiceName, req.Action, h.config.Commands.AllowedServices)
	}
	if err != nil {
		h.logger.Error("Service control failed",
			zap.Error(err),
			zap.String("service", req.ServiceName),
			zap.String("action", req.Action))

		h.taskExecutor.RecordCommandError(err)

		h.fail(msg, codeInternal, err.Error(), serviceControlResponse{
			Status: "error",
			Error:  err.Error(),
			TS:     utils.NowRFC3339(),
		})
		return
	}

	h.taskExecutor.RecordCommandSuccess()

	h.reply(msg, serviceControlResponse{
		Status:        "success",
		ServiceName:   req.ServiceName,
		Action:        req.Action,
		Result:        result,
		ServiceStatus: serviceStatus,
		TS:            utils.NowRFC3339(),
	})

	h.logger.Info("Service control succeeded",
		zap.String("service", req.ServiceName),
		zap.String("action", req.Action))
}

// handleLogFetch retrieves log file contents
func (h *CommandHandlers) handleLogFetch(msg micro.Request) {
	h.logger.Debug("Received log fetch command")

	// Parse request
	var req logFetchRequest
	if err := json.Unmarshal(msg.Data(), &req); err != nil {
		h.logger.Error("Failed to parse log fetch request", zap.Error(err))
		h.respondError(msg, codeBadRequest, "Invalid request format")
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

		h.fail(msg, codeInternal, err.Error(), logFetchResponse{
			Status: "error",
			Error:  err.Error(),
			TS:     utils.NowRFC3339(),
		})
		return
	}

	h.taskExecutor.RecordCommandSuccess()

	h.reply(msg, logFetchResponse{
		Status:     "success",
		LogPath:    req.LogPath,
		Lines:      lines,
		TotalLines: len(lines),
		TS:         utils.NowRFC3339(),
	})

	h.logger.Info("Log fetch succeeded",
		zap.String("path", req.LogPath),
		zap.Int("lines", len(lines)))
}

// handleCustomExec executes whitelisted PowerShell commands or scripts
func (h *CommandHandlers) handleCustomExec(msg micro.Request) {
	h.logger.Debug("Received custom exec command")

	// Parse request
	var req customExecRequest
	if err := json.Unmarshal(msg.Data(), &req); err != nil {
		h.logger.Error("Failed to parse exec request", zap.Error(err))
		h.respondError(msg, codeBadRequest, "Invalid request format")
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
	response := execResponse(req.Command, output, exitCode, err)
	if err != nil {
		h.taskExecutor.RecordCommandError(err)
		h.fail(msg, codeInternal, err.Error(), response)
		return
	}

	h.taskExecutor.RecordCommandSuccess()
	h.reply(msg, response)
}

// execResponse is the cmd.exec reply for every outcome, as the executor
// reported it.
//
// A command that ran and exited non-zero is still "error" -- anything checking
// status keeps working -- but it carries its output and exit code as well.
// That reply used to say only "command exited with code 1", so a failing
// script's stderr, the one thing the operator needed, never left the box.
//
// exitCode is -1 when the command never ran; see customExecResponse.ExitCode.
// Output is included whenever the command ran or left partial output behind
// (a timeout), so an empty success still answers "output": "".
func execResponse(command, output string, exitCode int, err error) customExecResponse {
	resp := customExecResponse{
		Status:  "success",
		Command: command,
		TS:      utils.NowRFC3339(),
	}
	if err != nil {
		resp.Status = "error"
		resp.Error = err.Error()
	}
	if exitCode >= 0 {
		resp.ExitCode = &exitCode
	}
	if exitCode >= 0 || output != "" {
		resp.Output = encodeOutput(output)
	}
	return resp
}

// encodeOutput embeds output that is valid JSON as JSON, so a script that
// prints an object arrives as an object, and anything else as a string.
func encodeOutput(output string) json.RawMessage {
	trimmed := strings.TrimSpace(output)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	encoded, _ := json.Marshal(output) // a string always marshals
	return encoded
}

// handleHealth returns enhanced agent health information
func (h *CommandHandlers) handleHealth(msg micro.Request) {
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

	h.reply(msg, healthResponse{
		Status: status,
		TS:     utils.NowRFC3339(),
		Agent:  agentMetrics,
		NATS:   natsHealth,
		Tasks:  taskMetrics,
		Config: configInfo,
		OS:     osInfo,
		Nebula: nebulaHealth,
		Checks: report,
	})

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
