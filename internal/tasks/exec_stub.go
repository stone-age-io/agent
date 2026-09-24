//go:build !windows && !linux && !freebsd

package tasks

import (
	"context"
	"fmt"
	"time"
)

// scriptExt is unused here in practice: both runners below refuse.
const scriptExt = ".sh"

func runShell(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
	return "", -1, fmt.Errorf("command execution not supported on this platform")
}

func runScript(ctx context.Context, path string, timeout time.Duration) (string, int, error) {
	return "", -1, fmt.Errorf("command execution not supported on this platform")
}
