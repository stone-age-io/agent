//go:build linux || freebsd

package tasks

import (
	"context"
	"runtime"
	"time"
)

// scriptExt is the extension a cmd.exec request must carry to name a script.
const scriptExt = ".sh"

// unixShell runs allowlisted command lines. bash on Linux, as it always has
// been. FreeBSD's base system ships no bash (the port installs it under
// /usr/local), so /bin/bash was "no such file" there and every allowlisted
// command failed on a stock install. FreeBSD entries are sh syntax.
func unixShell() string {
	if runtime.GOOS == "freebsd" {
		return "/bin/sh"
	}
	return "/bin/bash"
}

// runShell runs an allowlisted command line through the platform's shell.
func runShell(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
	return runProcess(ctx, timeout, unixShell(), "-c", command)
}

// runScript starts a script as a program: the kernel reads its shebang, so it
// must be executable, and no shell parses the path.
func runScript(ctx context.Context, path string, timeout time.Duration) (string, int, error) {
	return runProcess(ctx, timeout, path)
}
