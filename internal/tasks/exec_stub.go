//go:build !windows

package tasks

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ExecuteCommand is a stub for non-Windows platforms
func (e *Executor) ExecuteCommand(command string, allowedCommands []string, timeout time.Duration) (string, int, error) {
	// Validate command is in whitelist (for testing)
	if !isCommandAllowed(command, allowedCommands) {
		return "", -1, fmt.Errorf("command not in allowed list")
	}

	if e.logger != nil {
		e.logger.Info("Command execution not supported on this platform",
			zap.String("command", command),
			zap.String("platform", runtime.GOOS))
	}

	return "", -1, fmt.Errorf("command execution not supported on %s", runtime.GOOS)
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
