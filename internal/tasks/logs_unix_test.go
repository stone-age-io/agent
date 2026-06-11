//go:build !windows

package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestIsPathAllowed tests the log path whitelist validation
// This is CRITICAL for security - prevents unauthorized file access
// Unix variant of the Windows test in logs_windows_test.go
//
// Positive cases use real files in a temp directory because isPathAllowed
// expands allowed patterns with filepath.Glob against the filesystem.
func TestIsPathAllowed(t *testing.T) {
	logsDir := t.TempDir()
	appLog := filepath.Join(logsDir, "app.log")
	if err := os.WriteFile(appLog, []byte("test\n"), 0o644); err != nil {
		t.Fatalf("Failed to create test log file: %v", err)
	}
	wildcardPattern := filepath.Join(logsDir, "*.log")

	tests := []struct {
		name            string
		requestedPath   string
		allowedPatterns []string
		want            bool
		reason          string
	}{
		// Valid cases
		{
			name:            "exact match",
			requestedPath:   appLog,
			allowedPatterns: []string{appLog},
			want:            true,
			reason:          "exact path match should be allowed",
		},
		{
			name:            "wildcard match",
			requestedPath:   appLog,
			allowedPatterns: []string{wildcardPattern},
			want:            true,
			reason:          "wildcard pattern should match",
		},
		{
			name:            "multiple patterns",
			requestedPath:   appLog,
			allowedPatterns: []string{"/var/log/nonexistent/*.log", wildcardPattern},
			want:            true,
			reason:          "should match second pattern",
		},

		// Security: Path traversal attacks
		{
			name:            "parent directory traversal",
			requestedPath:   filepath.Join(logsDir, "..", "..", "etc", "passwd"),
			allowedPatterns: []string{wildcardPattern},
			want:            false,
			reason:          "parent directory traversal must be blocked",
		},
		{
			name:            "dots resolving back inside allowed dir",
			requestedPath:   logsDir + "/../" + filepath.Base(logsDir) + "/app.log",
			allowedPatterns: []string{wildcardPattern},
			want:            true,
			reason:          "paths are cleaned before checking, so .. that resolves inside the allowed dir is permitted",
		},
		{
			name:            "traversal to shadow file",
			requestedPath:   "/var/log/../../etc/shadow",
			allowedPatterns: []string{"/var/log/*.log"},
			want:            false,
			reason:          "traversal out of allowed directory must be blocked",
		},

		// Security: Suspicious file types (filter applies on all platforms)
		{
			name:            "exe file",
			requestedPath:   filepath.Join(logsDir, "malicious.exe"),
			allowedPatterns: []string{filepath.Join(logsDir, "*")},
			want:            false,
			reason:          "executable files must be blocked",
		},
		{
			name:            "dll file",
			requestedPath:   filepath.Join(logsDir, "library.dll"),
			allowedPatterns: []string{filepath.Join(logsDir, "*")},
			want:            false,
			reason:          "DLL files must be blocked",
		},

		// Invalid cases
		{
			name:            "no match",
			requestedPath:   "/var/log/other.log",
			allowedPatterns: []string{wildcardPattern},
			want:            false,
			reason:          "path not matching pattern should be rejected",
		},
		{
			name:            "wrong extension",
			requestedPath:   filepath.Join(logsDir, "app.txt"),
			allowedPatterns: []string{wildcardPattern},
			want:            false,
			reason:          "wrong extension should not match",
		},
		{
			name:            "relative path",
			requestedPath:   "logs/app.log",
			allowedPatterns: []string{wildcardPattern},
			want:            false,
			reason:          "relative paths must be rejected",
		},
		{
			name:            "empty pattern list",
			requestedPath:   appLog,
			allowedPatterns: []string{},
			want:            false,
			reason:          "no patterns means nothing allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPathAllowed(tt.requestedPath, tt.allowedPatterns)
			if got != tt.want {
				t.Errorf("isPathAllowed() = %v, want %v: %s", got, tt.want, tt.reason)
			}
		})
	}
}

// TestFetchLogLines tests log fetching end to end: validation failures
// and actually tailing a real file
func TestFetchLogLines(t *testing.T) {
	logsDir := t.TempDir()
	appLog := filepath.Join(logsDir, "app.log")

	var content strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&content, "line %d\n", i)
	}
	if err := os.WriteFile(appLog, []byte(content.String()), 0o644); err != nil {
		t.Fatalf("Failed to create test log file: %v", err)
	}
	wildcardPattern := filepath.Join(logsDir, "*.log")

	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	t.Run("reads last N lines", func(t *testing.T) {
		lines, err := executor.FetchLogLines(appLog, 5, []string{wildcardPattern})
		if err != nil {
			t.Fatalf("FetchLogLines() error = %v", err)
		}
		if len(lines) != 5 {
			t.Fatalf("FetchLogLines() returned %d lines, want 5", len(lines))
		}
		if lines[0] != "line 16" || lines[4] != "line 20" {
			t.Errorf("FetchLogLines() = %v, want lines 16-20 in order", lines)
		}
	})

	errTests := []struct {
		name            string
		logPath         string
		lines           int
		allowedPatterns []string
		errContains     string
	}{
		{
			name:            "path not allowed",
			logPath:         "/etc/passwd",
			lines:           10,
			allowedPatterns: []string{wildcardPattern},
			errContains:     "not in allowed list",
		},
		{
			name:            "path traversal attempt",
			logPath:         logsDir + "/../../etc/passwd",
			lines:           10,
			allowedPatterns: []string{wildcardPattern},
			errContains:     "not in allowed list",
		},
		{
			name:            "zero lines requested",
			logPath:         appLog,
			lines:           0,
			allowedPatterns: []string{wildcardPattern},
			errContains:     "must be greater than 0",
		},
		{
			name:            "negative lines",
			logPath:         appLog,
			lines:           -5,
			allowedPatterns: []string{wildcardPattern},
			errContains:     "must be greater than 0",
		},
		{
			name:            "too many lines",
			logPath:         appLog,
			lines:           20000,
			allowedPatterns: []string{wildcardPattern},
			errContains:     "cannot exceed 10000",
		},
	}

	for _, tt := range errTests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executor.FetchLogLines(tt.logPath, tt.lines, tt.allowedPatterns)
			if err == nil || !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("FetchLogLines() error = %v, want error containing %q", err, tt.errContains)
			}
		})
	}
}
