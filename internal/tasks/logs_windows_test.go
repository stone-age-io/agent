//go:build windows

package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsPathAllowedWindowsForms covers the path forms only Windows has. The
// allowlist itself, traversal and the line-count checks are tested on every
// platform in logs_path_test.go.
func TestIsPathAllowedWindowsForms(t *testing.T) {
	logsDir := t.TempDir()
	appLog := filepath.Join(logsDir, "app.log")
	if err := os.WriteFile(appLog, []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	allowed := []string{filepath.Join(logsDir, "*.log")}

	tests := []struct {
		name          string
		requestedPath string
		want          bool
	}{
		{"backslashes", appLog, true},
		{"forward slashes name the same file", strings.ReplaceAll(appLog, `\`, "/"), true},
		{"drive-relative", filepath.VolumeName(logsDir) + "app.log", false},
		{"relative with backslash", `Logs\app.log`, false},
		{"UNC path", `\\server\share\app.log`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPathAllowed(tt.requestedPath, allowed); got != tt.want {
				t.Errorf("isPathAllowed(%q) = %v, want %v", tt.requestedPath, got, tt.want)
			}
		})
	}
}
