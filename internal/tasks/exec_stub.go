//go:build !windows && !linux && !freebsd

package tasks

import (
	"fmt"
	"time"
)

// ExecuteCommand is a stub for unsupported platforms
func (e *Executor) ExecuteCommand(command string, allowedCommands []string, scriptsDir string, timeout time.Duration) (string, int, error) {
	return "", -1, fmt.Errorf("command execution not supported on this platform")
}
