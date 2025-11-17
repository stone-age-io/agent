//go:build linux || freebsd

package tasks

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ExecuteCommand executes a bash/sh script if it's in the whitelist or scripts directory
func (e *Executor) ExecuteCommand(command string, allowedCommands []string, scriptsDir string, timeout time.Duration) (string, int, error) {
	// Validate command is allowed
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

	// Execute via bash with configured timeout
	output, exitCode, err := executeBash(fullCommand, timeout)
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

// isScript checks if a command looks like a shell script (.sh extension)
func isScript(command string) bool {
	filename := filepath.Base(command)
	return filepath.Ext(filename) == ".sh"
}

// isScriptAllowed validates that a script exists in the scripts directory
// and prevents path traversal attacks
func isScriptAllowed(command string, scriptsDir string) bool {
	cleanScriptsDir := filepath.Clean(scriptsDir)
	commandFilename := filepath.Base(command)

	// Verify .sh extension
	if filepath.Ext(commandFilename) != ".sh" {
		return false
	}

	// Construct expected script path
	scriptPath := filepath.Join(cleanScriptsDir, commandFilename)

	// Clean and verify path is within scripts directory
	cleanScriptPath := filepath.Clean(scriptPath)
	if !strings.HasPrefix(cleanScriptPath, cleanScriptsDir+string(filepath.Separator)) &&
		cleanScriptPath != cleanScriptsDir {
		return false
	}

	// Verify file exists and is regular file
	info, err := os.Stat(cleanScriptPath)
	if err != nil {
		return false
	}

	return !info.IsDir()
}

// resolveScriptPath resolves a script reference to its full path
func resolveScriptPath(command string, scriptsDir string) (string, error) {
	if filepath.Base(command) == command {
		return filepath.Join(scriptsDir, command), nil
	}
	return command, nil
}

// normalizeWhitespace normalizes whitespace for comparison
func normalizeWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// executeBash executes a bash command and returns output and exit code
func executeBash(command string, timeout time.Duration) (string, int, error) {
	// Execute via bash
	cmd := exec.Command("/bin/bash", "-c", command)

	// Capture stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Set timeout for command execution
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
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return "", -1, fmt.Errorf("command execution timeout (%v)", timeout)
	}
}
