package tasks

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ExecuteCommand executes a PowerShell command if it's in the whitelist
// Commands must match exactly - no parameter substitution is allowed
func (e *Executor) ExecuteCommand(command string, allowedCommands []string, timeout time.Duration) (string, int, error) {
	// Validate command is in whitelist (exact match)
	if !isCommandAllowed(command, allowedCommands) {
		return "", -1, fmt.Errorf("command not in allowed list")
	}

	e.logger.Info("Executing whitelisted command",
		zap.String("command", command),
		zap.Duration("timeout", timeout))

	// Execute via PowerShell with configured timeout
	output, exitCode, err := executePowerShell(command, timeout)
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

// isCommandAllowed checks if a command exactly matches an entry in the whitelist
func isCommandAllowed(command string, allowedCommands []string) bool {
	// Normalize whitespace for comparison
	normalized := normalizeWhitespace(command)

	for _, allowed := range allowedCommands {
		if normalized == normalizeWhitespace(allowed) {
			return true
		}
	}

	return false
}

// normalizeWhitespace normalizes whitespace in a command for comparison
func normalizeWhitespace(s string) string {
	// Replace multiple spaces with single space and trim
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// executePowerShell executes a PowerShell command and returns output and exit code
func executePowerShell(command string, timeout time.Duration) (string, int, error) {
	// Create the PowerShell command with proper escaping
	// Use -NoProfile for faster startup and -NonInteractive for non-interactive mode
	cmd := exec.Command("powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", command)

	// Capture stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Set timeout for command execution (from config)
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	// Wait for command or timeout
	select {
	case err := <-done:
		// Get exit code
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				// Non-exit error (e.g., command not found)
				return "", -1, fmt.Errorf("failed to execute command: %w", err)
			}
		}

		// Combine stdout and stderr
		output := stdout.String()
		if stderr.Len() > 0 {
			if output != "" {
				output += "\n"
			}
			output += "STDERR:\n" + stderr.String()
		}

		// Return error if exit code is non-zero
		if exitCode != 0 {
			return output, exitCode, fmt.Errorf("command exited with code %d", exitCode)
		}

		return output, exitCode, nil

	case <-time.After(timeout):
		// Kill the process if it times out
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return "", -1, fmt.Errorf("command execution timeout (%v)", timeout)
	}
}
