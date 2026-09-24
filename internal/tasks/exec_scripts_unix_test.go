//go:build linux || freebsd

package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestExecuteCommandScripts runs real processes: the refusal is only worth
// something if nothing ran, so the injection case checks for its side effect.
func TestExecuteCommandScripts(t *testing.T) {
	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatal(err)
	}

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

	t.Run("allowlisted command runs the entry, not the request", func(t *testing.T) {
		// A newline in the request would make bash run `echo one` and then
		// try a command called `two`.
		out, code, err := executor.ExecuteCommand("echo one\ntwo", []string{"echo one two"}, "", 5*time.Second)
		if err != nil || code != 0 || strings.TrimSpace(out) != "one two" {
			t.Fatalf("got (%q, %d, %v), want (\"one two\", 0, nil)", out, code, err)
		}
	})
}
