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

// TestIsPathAllowed tests the log path allowlist. It runs on every platform;
// logs_windows_test.go adds only the path forms Windows alone has.
//
// Every case uses real files in a temp directory, because isPathAllowed
// expands the allowed patterns with filepath.Glob against the filesystem. A
// case naming a file that does not exist is refused whatever the logic says,
// which is how the old Windows copy of this test came to "pass" cases like
// `C:\Logs\malicious.exe` without testing anything.
func TestIsPathAllowed(t *testing.T) {
	logsDir := t.TempDir()
	appLog := filepath.Join(logsDir, "app.log")
	sambaLog := filepath.Join(logsDir, "samba", "log.smbd")
	for _, f := range []string{appLog, sambaLog, filepath.Join(logsDir, "a1.log")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("test\n"), 0o644); err != nil {
			t.Fatalf("Failed to create test log file: %v", err)
		}
	}
	a1Log := filepath.Join(logsDir, "a1.log")
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

		// The allowlist is the whole check: nothing else second-guesses it
		{
			name:            "allowlisted path containing a once-blocked word",
			requestedPath:   sambaLog,
			allowedPatterns: []string{filepath.Join(logsDir, "samba", "*")},
			want:            true,
			reason:          "a substring denylist refused anything containing \"sam\"",
		},
		{
			name:            "question-mark pattern",
			requestedPath:   a1Log,
			allowedPatterns: []string{filepath.Join(logsDir, "a?.log")},
			want:            true,
			reason:          "a prefix check that only knew * refused every ? pattern",
		},
		{
			name:            "character-class pattern",
			requestedPath:   a1Log,
			allowedPatterns: []string{filepath.Join(logsDir, "a[0-9].log")},
			want:            true,
			reason:          "a prefix check that only knew * refused every [...] pattern",
		},
		{
			name:            "file outside the pattern in the same directory",
			requestedPath:   a1Log,
			allowedPatterns: []string{filepath.Join(logsDir, "app*.log")},
			want:            false,
			reason:          "only files the pattern names are readable",
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

	executor := NewExecutor(zap.NewNop(), 0, context.Background())

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
