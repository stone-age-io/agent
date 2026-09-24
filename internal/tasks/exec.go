package tasks

// The cmd.exec gate, shared by every platform. The platform files
// (exec_unix.go, exec_windows.go) say only how to run a process: which shell
// runs an allowlisted command line, and how a script file is started.
//
// The gate lives here, once, because it used to be copied into both platform
// files, and the copies shared a hole: a request like `$(anything)/deploy.sh`
// was allowed because a deploy.sh existed in the scripts directory, and then
// the raw request string went to `bash -c`. See ExecuteCommand for the two
// rules that close it.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ExecuteCommand runs a request that names a script in scriptsDir or an entry
// in allowedCommands, and nothing else.
//
// THE CALLER'S STRING NEVER REACHES A SHELL. A script request must be a bare
// filename; the agent builds the path itself and starts that file directly,
// with no shell parsing anything. An allowlisted command runs the operator's
// allowlist ENTRY, not the request, so a request that differs from it only in
// whitespace -- a newline that would split the line into two commands and
// drop a trailing flag, say -- runs exactly what the operator wrote.
//
// Scripts are checked first, so a request naming one is never looked up in the
// allowlist and never handed to a shell.
func (e *Executor) ExecuteCommand(command string, allowedCommands []string, scriptsDir string, timeout time.Duration) (string, int, error) {
	var output string
	var exitCode int
	var err error

	if path, ok := scriptPath(command, scriptsDir); ok {
		e.logger.Info("Executing script",
			zap.String("command", command),
			zap.String("path", path),
			zap.Duration("timeout", timeout))
		output, exitCode, err = runScript(e.ctx, path, timeout)
	} else if entry, ok := allowedCommand(command, allowedCommands); ok {
		e.logger.Info("Executing allowlisted command",
			zap.String("command", entry),
			zap.Duration("timeout", timeout))
		output, exitCode, err = runShell(e.ctx, entry, timeout)
	} else {
		return "", -1, fmt.Errorf("command not in allowed list or scripts directory")
	}

	if err != nil {
		e.logger.Error("Command execution failed",
			zap.String("command", command),
			zap.Error(err),
			zap.Int("exit_code", exitCode))
		return output, exitCode, err
	}

	e.logger.Info("Command executed successfully",
		zap.String("command", command),
		zap.Int("exit_code", exitCode))
	return output, exitCode, nil
}

// scriptPath returns the file to run for a script request, and whether the
// request names one: a bare filename with the platform's script extension, of
// a regular file directly inside scriptsDir.
//
// Bare means bare. Anything filepath.Base would shorten -- a separator, a
// drive letter, a `..` -- is refused rather than reduced to its last element,
// because reducing it is exactly how the old check came to approve a string
// it then did not run.
func scriptPath(command, scriptsDir string) (string, bool) {
	if scriptsDir == "" || filepath.Ext(command) != scriptExt {
		return "", false
	}
	if command != filepath.Base(command) {
		return "", false
	}

	path := filepath.Join(scriptsDir, command)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return path, true
}

// allowedCommand returns the allowlist entry a request names, compared with
// whitespace normalized. It returns the entry rather than a yes/no so that
// the entry is what runs; see ExecuteCommand.
func allowedCommand(command string, allowedCommands []string) (string, bool) {
	normalized := normalizeWhitespace(command)
	for _, allowed := range allowedCommands {
		if normalized == normalizeWhitespace(allowed) {
			return allowed, true
		}
	}
	return "", false
}

// pipeWaitDelay bounds how long runProcess waits for output after the process
// it started has exited or been killed. Something the process left behind --
// a `sleep` bash was waiting on, a daemon a script started without
// redirecting it -- inherits stdout and holds the pipe open, and without a
// bound Run waits for it to close. That made the timeout a suggestion: a
// killed shell still waited out its child, and a script that exited while
// leaving a daemon behind held the handler for as long as the daemon lived.
const pipeWaitDelay = time.Second

// runProcess runs name with args under timeout, returning the combined output
// and the exit code. A non-zero exit is an error that still carries the
// output. A process that never ran, or was killed by the timeout, reports
// exit code -1 -- the timeout with whatever it had printed so far.
func runProcess(ctx context.Context, timeout time.Duration, name string, args ...string) (string, int, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, name, args...)
	cmd.WaitDelay = pipeWaitDelay
	killTreeOnCancel(cmd)

	// Capture stdout and stderr (capped to avoid unbounded memory use)
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	if cmdCtx.Err() == context.DeadlineExceeded {
		return combineOutput(&stdout, &stderr), -1, fmt.Errorf("command execution timeout (%v)", timeout)
	}
	if cmdCtx.Err() == context.Canceled {
		return combineOutput(&stdout, &stderr), -1, fmt.Errorf("command execution cancelled")
	}

	// The process exited 0 and something it left behind still held the pipe
	// when pipeWaitDelay ran out. The command succeeded; report it that way.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// Never started: not found, not executable
			return "", -1, fmt.Errorf("failed to execute command: %w", err)
		}
		exitCode = exitErr.ExitCode()
	}

	output := combineOutput(&stdout, &stderr)
	if exitCode != 0 {
		return output, exitCode, fmt.Errorf("command exited with code %d", exitCode)
	}
	return output, exitCode, nil
}

// maxCommandOutputBytes caps captured stdout/stderr so a runaway command
// cannot exhaust agent memory. Output beyond the cap is discarded.
const maxCommandOutputBytes = 10 * 1024 * 1024 // 10MB

// limitedBuffer is an io.Writer that keeps the first maxCommandOutputBytes
// and silently discards the rest. Writes never fail, so the command keeps
// running to completion even after the cap is hit.
type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := maxCommandOutputBytes - b.buf.Len()
	if remaining >= len(p) {
		return b.buf.Write(p)
	}
	if remaining > 0 {
		b.buf.Write(p[:remaining])
	}
	b.truncated = true
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
func (b *limitedBuffer) Len() int       { return b.buf.Len() }

// combineOutput merges stdout and stderr into the single output string
// returned to the control plane, noting any truncation.
func combineOutput(stdout, stderr *limitedBuffer) string {
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += "STDERR:\n" + stderr.String()
	}
	if stdout.truncated || stderr.truncated {
		output += "\n[output truncated: exceeded 10MB limit]"
	}
	return output
}

// normalizeWhitespace normalizes whitespace in a command for comparison
func normalizeWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
