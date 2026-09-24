//go:build linux || freebsd

package tasks

import (
	"context"
	"time"
)

// scriptExt is the extension a cmd.exec request must carry to name a script.
const scriptExt = ".sh"

// runShell runs an allowlisted command line through bash.
func runShell(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
	return runProcess(ctx, timeout, "/bin/bash", "-c", command)
}

// runScript starts a script as a program: the kernel reads its shebang, so it
// must be executable, and no shell parses the path.
func runScript(ctx context.Context, path string, timeout time.Duration) (string, int, error) {
	return runProcess(ctx, timeout, path)
}
