//go:build linux || freebsd

package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap"
)

// The shell allowlisted commands run through must exist on the platform
// running the test. /bin/bash does not on a stock FreeBSD, which is how every
// allowlisted command there came to fail unnoticed.
func TestUnixShellExists(t *testing.T) {
	if _, err := os.Stat(unixShell()); err != nil {
		t.Fatalf("allowlisted commands would run through %s: %v", unixShell(), err)
	}
}

// TestExecuteCommandScripts runs real processes: the refusal is only worth
// something if nothing ran, so the injection case checks for its side effect.
func TestExecuteCommandScripts(t *testing.T) {
	executor := NewExecutor(zap.NewNop(), 0, context.Background())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.sh"), []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("script runs", func(t *testing.T) {
		out, code, err := executor.ExecuteCommand("deploy.sh", nil, dir, 5*time.Second)
		if err != nil || code != 0 || strings.TrimSpace(out) != "ok" {
			t.Fatalf("got (%q, %d, %v), want (\"ok\", 0, nil)", out, code, err)
		}
	})

	t.Run("injection before a real script name is refused and does not run", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "pwned")
		_, _, err := executor.ExecuteCommand("$(touch "+marker+")/deploy.sh", nil, dir, 5*time.Second)
		if err == nil || !strings.Contains(err.Error(), "not in allowed list") {
			t.Fatalf("err = %v, want a refusal", err)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatal("the injected command ran")
		}
	})

	t.Run("failing script returns its output and exit code", func(t *testing.T) {
		script := "#!/bin/sh\necho partial\necho boom >&2\nexit 3\n"
		if err := os.WriteFile(filepath.Join(dir, "fail.sh"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		out, code, err := executor.ExecuteCommand("fail.sh", nil, dir, 5*time.Second)
		if err == nil || code != 3 {
			t.Fatalf("got (%d, %v), want exit code 3 and an error", code, err)
		}
		if !strings.Contains(out, "partial") || !strings.Contains(out, "boom") {
			t.Errorf("output = %q, want both stdout and stderr", out)
		}
	})

	t.Run("timeout returns what was printed, on time, and kills the children", func(t *testing.T) {
		// `sleep` is a child of bash, not bash itself. Killing only bash left
		// it running and holding the output pipe: the reply took the full
		// sleep, and after WaitDelay the sleep still outlived the reply.
		pidFile := filepath.Join(t.TempDir(), "child.pid")
		cmd := "echo started; sleep 30 & echo $! > " + pidFile + "; wait"
		start := time.Now()
		out, code, err := executor.ExecuteCommand(cmd, []string{cmd}, "", 500*time.Millisecond)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("took %v for a 500ms timeout", elapsed)
		}
		if err == nil || !strings.Contains(err.Error(), "timeout") || code != -1 {
			t.Fatalf("got (%d, %v), want a timeout with exit code -1", code, err)
		}
		if !strings.Contains(out, "started") {
			t.Errorf("output = %q, want the partial output", out)
		}
		if pid := readPID(t, pidFile); !eventuallyDead(pid) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("child %d survived the timeout", pid)
		}
	})

	t.Run("a process left behind does not hold the reply, and is left alone", func(t *testing.T) {
		// The shell exits at once; the backgrounded sleep keeps stdout open.
		// Nothing timed out, so nothing is killed: this is the shape of a
		// script that starts a daemon.
		cmd := "sleep 30 & echo $!"
		start := time.Now()
		out, code, err := executor.ExecuteCommand(cmd, []string{cmd}, "", 30*time.Second)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("took %v; the reply waited for the background process", elapsed)
		}
		if err != nil || code != 0 {
			t.Fatalf("got (%q, %d, %v), want success", out, code, err)
		}
		pid, convErr := strconv.Atoi(strings.TrimSpace(out))
		if convErr != nil {
			t.Fatalf("output = %q, want the background pid", out)
		}
		defer syscall.Kill(pid, syscall.SIGKILL)
		if syscall.Kill(pid, 0) != nil {
			t.Error("the background process was killed; only a timeout should do that")
		}
	})

	t.Run("allowlisted command runs the entry, not the request", func(t *testing.T) {
		// A newline in the request would make bash run `echo one` and then
		// try a command called `two`.
		out, code, err := executor.ExecuteCommand("echo one\ntwo", []string{"echo one two"}, "", 5*time.Second)
		if err != nil || code != 0 || strings.TrimSpace(out) != "one two" {
			t.Fatalf("got (%q, %d, %v), want (\"one two\", 0, nil)", out, code, err)
		}
	})
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the command never wrote its child's pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pid file = %q", raw)
	}
	return pid
}

// eventuallyDead polls, because a killed child is reaped by init, not by us.
func eventuallyDead(pid int) bool {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
	}
	return false
}
