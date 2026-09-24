//go:build windows

package tasks

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

// scriptExt is the extension a cmd.exec request must carry to name a script.
const scriptExt = ".ps1"

// runShell runs an allowlisted command line through PowerShell.
func runShell(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
	return runProcess(ctx, timeout, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", command)
}

// runScript runs a script file with -File, so PowerShell treats the path as a
// path rather than as code, and the script's own `exit N` becomes the exit
// code. Under -Command a script's exit code collapsed to 0 or 1.
func runScript(ctx context.Context, path string, timeout time.Duration) (string, int, error) {
	return runProcess(ctx, timeout, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", path)
}

// killTreeOnCancel makes a timeout (or shutdown) kill everything the command
// started, not just powershell.exe: taskkill /T walks the tree by parent
// process. Only cancellation does this; a command that exits on its own and
// leaves a process behind is left alone. If taskkill cannot run, WaitDelay
// still kills powershell.exe itself.
func killTreeOnCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}
