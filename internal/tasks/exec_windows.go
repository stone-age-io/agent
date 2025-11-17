//go:build windows

package tasks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ExecuteCommand executes a PowerShell command or script if it's in the whitelist
// Commands must match exactly - no parameter substitution is allowed
// Scripts must exist in the configured scripts_directory
func (e *Executor) ExecuteCommand(command string, allowedCommands []string, scriptsDir string, timeout time.Duration) (string, int, error) {
	// Validate command is allowed (either in whitelist or scripts directory)
	if !isCommandAllowed(command, allowedCommands, scriptsDir) {
		return "", -1, fmt.Errorf("command not in allowed list or scripts directory")
	}

	// Resolve full command path if this is a script
	fullCommand := command
	if scriptsDir != "" && isScript(command) {
		resolvedPath, err := resolveScriptPath(command, scriptsDir)
		if err != nil {
			e.logger.Error("Failed to resolve script path",
				zap.String("command", command),
				zap.Error(err))
			return "", -1, fmt.Errorf("failed to resolve script path: %w", err)
		}
		fullCommand = resolvedPath
	}

	e.logger.Info("Executing whitelisted command",
		zap.String("command", command),
		zap.String("resolved", fullCommand),
		zap.Duration("timeout", timeout))

	// MODIFIED: Execute via PowerShell with context and configured timeout
	output, exitCode, err := executePowerShell(e.ctx, fullCommand, timeout)
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

// isCommandAllowed checks if a command is allowed via:
// 1. Exact match in allowedCommands list
// 2. Script file in scripts directory
func isCommandAllowed(command string, allowedCommands []string, scriptsDir string) bool {
	normalized := normalizeWhitespace(command)

	// Check exact match in allowed commands list
	for _, allowed := range allowedCommands {
		if normalized == normalizeWhitespace(allowed) {
			return true
		}
	}

	// Check if it's a script in the scripts directory
	if scriptsDir != "" && isScript(command) {
		return isScriptAllowed(command, scriptsDir)
	}

	return false
}

// isScript checks if a command looks like a script (.ps1 extension)
func isScript(command string) bool {
	// Extract filename from command
	// Handles both "script.ps1" and "C:\Path\script.ps1"
	filename := filepath.Base(command)
	return filepath.Ext(filename) == ".ps1"
}

// isScriptAllowed validates that a script exists in the scripts directory
// and prevents path traversal attacks
func isScriptAllowed(command string, scriptsDir string) bool {
	// Clean the scripts directory path
	cleanScriptsDir := filepath.Clean(scriptsDir)

	// Extract just the filename if a full path was provided
	// This allows control plane to send either "script.ps1" or full path
	commandFilename := filepath.Base(command)

	// Double-check it's a .ps1 file (should be caught by isScript, but be defensive)
	if filepath.Ext(commandFilename) != ".ps1" {
		return false
	}

	// Construct the expected script path
	scriptPath := filepath.Join(cleanScriptsDir, commandFilename)

	// Clean the path and verify it stays within scripts directory
	// This prevents path traversal attacks like "..\..\evil.ps1"
	cleanScriptPath := filepath.Clean(scriptPath)
	if !strings.HasPrefix(cleanScriptPath, cleanScriptsDir+string(filepath.Separator)) &&
		cleanScriptPath != cleanScriptsDir {
		return false
	}

	// Verify the file exists
	info, err := os.Stat(cleanScriptPath)
	if err != nil {
		return false
	}

	// Ensure it's a file, not a directory
	if info.IsDir() {
		return false
	}

	return true
}

// resolveScriptPath resolves a script reference to its full path
// Handles both "script.ps1" and full paths
func resolveScriptPath(command string, scriptsDir string) (string, error) {
	// If command is just a filename, prepend scripts directory
	if filepath.Base(command) == command {
		return filepath.Join(scriptsDir, command), nil
	}

	// If it's already a full path that's been validated, use it
	// (validation happens in isScriptAllowed)
	return command, nil
}

// normalizeWhitespace normalizes whitespace in a command for comparison
func normalizeWhitespace(s string) string {
	// Replace multiple spaces with single space and trim
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// executePowerShell executes a PowerShell command and returns output and exit code
// MODIFIED: Now accepts context for cancellation
func executePowerShell(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
	// MODIFIED: Create context with timeout from parent context
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// MODIFIED: Use CommandContext instead of Command
	cmd := exec.CommandContext(cmdCtx,
		"powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", command)

	// Capture stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Execute command
	err := cmd.Run()
	
	// Handle context cancellation
	if cmdCtx.Err() == context.DeadlineExceeded {
		return "", -1, fmt.Errorf("command execution timeout (%v)", timeout)
	}
	if cmdCtx.Err() == context.Canceled {
		return "", -1, fmt.Errorf("command execution cancelled")
	}

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
}
