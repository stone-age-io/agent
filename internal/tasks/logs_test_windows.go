//go:build windows

package tasks

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

// TestIsPathAllowed tests the log path whitelist validation
// This is CRITICAL for security - prevents unauthorized file access
// Windows-only because it tests Windows path conventions
func TestIsPathAllowed(t *testing.T) {
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
			requestedPath:   `C:\Logs\app.log`,
			allowedPatterns: []string{`C:\Logs\app.log`},
			want:            true,
			reason:          "exact path match should be allowed",
		},
		{
			name:            "wildcard match",
			requestedPath:   `C:\Logs\app.log`,
			allowedPatterns: []string{`C:\Logs\*.log`},
			want:            true,
			reason:          "wildcard pattern should match",
		},
		{
			name:            "multiple patterns",
			requestedPath:   `C:\AppData\service.log`,
			allowedPatterns: []string{`C:\Logs\*.log`, `C:\AppData\*.log`},
			want:            true,
			reason:          "should match second pattern",
		},
		{
			name:            "nested wildcard",
			requestedPath:   `C:\ProgramData\MyApp\logs\error.log`,
			allowedPatterns: []string{`C:\ProgramData\MyApp\logs\*.log`},
			want:            true,
			reason:          "nested path with wildcard should match",
		},

		// Security: Path traversal attacks
		{
			name:            "parent directory traversal",
			requestedPath:   `C:\Logs\..\Windows\System32\config\SAM`,
			allowedPatterns: []string{`C:\Logs\*.log`},
			want:            false,
			reason:          "parent directory traversal must be blocked",
		},
		{
			name:            "relative path with dots",
			requestedPath:   `C:\Logs\..\..\Windows\win.ini`,
			allowedPatterns: []string{`C:\Logs\*.log`},
			want:            false,
			reason:          "path traversal attempt must be blocked",
		},
		{
			name:            "double dots in middle",
			requestedPath:   `C:\App\..\Windows\System32\drivers\etc\hosts`,
			allowedPatterns: []string{`C:\App\*.log`},
			want:            false,
			reason:          "path traversal in middle must be blocked",
		},

		// Security: System directories
		{
			name:            "system32 access",
			requestedPath:   `C:\Windows\System32\config\SAM`,
			allowedPatterns: []string{`C:\Windows\System32\*`},
			want:            false,
			reason:          "system32 directory must be blocked",
		},
		{
			name:            "windows directory",
			requestedPath:   `C:\Windows\win.ini`,
			allowedPatterns: []string{`C:\Windows\*`},
			want:            false,
			reason:          "Windows directory must be blocked",
		},
		{
			name:            "program files access",
			requestedPath:   `C:\Program Files\MyApp\config.ini`,
			allowedPatterns: []string{`C:\Program Files\*`},
			want:            false,
			reason:          "Program Files must be blocked",
		},
		{
			name:            "SAM file access",
			requestedPath:   `C:\Logs\SAM`,
			allowedPatterns: []string{`C:\Logs\*`},
			want:            false,
			reason:          "SAM file access must be blocked",
		},

		// Security: Executable and binary files
		{
			name:            "exe file",
			requestedPath:   `C:\Logs\malicious.exe`,
			allowedPatterns: []string{`C:\Logs\*`},
			want:            false,
			reason:          "executable files must be blocked",
		},
		{
			name:            "dll file",
			requestedPath:   `C:\Logs\library.dll`,
			allowedPatterns: []string{`C:\Logs\*`},
			want:            false,
			reason:          "DLL files must be blocked",
		},
		{
			name:            "sys file",
			requestedPath:   `C:\Logs\driver.sys`,
			allowedPatterns: []string{`C:\Logs\*`},
			want:            false,
			reason:          "system files must be blocked",
		},

		// Security: Case sensitivity
		{
			name:            "case variation in system32",
			requestedPath:   `C:\Logs\SyStEm32\test.log`,
			allowedPatterns: []string{`C:\Logs\*`},
			want:            false,
			reason:          "case variations of system directories must be blocked",
		},
		{
			name:            "uppercase windows",
			requestedPath:   `C:\WINDOWS\test.log`,
			allowedPatterns: []string{`C:\test\*`},
			want:            false,
			reason:          "uppercase Windows directory must be blocked",
		},

		// Invalid cases
		{
			name:            "no match",
			requestedPath:   `C:\Other\app.log`,
			allowedPatterns: []string{`C:\Logs\*.log`},
			want:            false,
			reason:          "path not matching pattern should be rejected",
		},
		{
			name:            "wrong extension",
			requestedPath:   `C:\Logs\app.txt`,
			allowedPatterns: []string{`C:\Logs\*.log`},
			want:            false,
			reason:          "wrong extension should not match",
		},
		{
			name:            "relative path",
			requestedPath:   `Logs\app.log`,
			allowedPatterns: []string{`C:\Logs\*.log`},
			want:            false,
			reason:          "relative paths must be rejected",
		},
		{
			name:            "empty pattern list",
			requestedPath:   `C:\Logs\app.log`,
			allowedPatterns: []string{},
			want:            false,
			reason:          "no patterns means nothing allowed",
		},
		{
			name:            "UNC path",
			requestedPath:   `\\server\share\file.log`,
			allowedPatterns: []string{`C:\Logs\*.log`},
			want:            false,
			reason:          "UNC paths should be rejected (not absolute local)",
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

// TestFetchLogLines tests the log fetching functionality
func TestFetchLogLines(t *testing.T) {
	// Create executor with builtin metrics source for tests
	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	tests := []struct {
		name            string
		logPath         string
		lines           int
		allowedPatterns []string
		wantErr         bool
		errContains     string
	}{
		{
			name:            "path not allowed",
			logPath:         `C:\Windows\System32\test.log`,
			lines:           10,
			allowedPatterns: []string{`C:\Logs\*.log`},
			wantErr:         true,
			errContains:     "not in allowed list",
		},
		{
			name:            "zero lines requested",
			logPath:         `C:\Logs\app.log`,
			lines:           0,
			allowedPatterns: []string{`C:\Logs\*.log`},
			wantErr:         true,
			errContains:     "must be greater than 0",
		},
		{
			name:            "negative lines",
			logPath:         `C:\Logs\app.log`,
			lines:           -5,
			allowedPatterns: []string{`C:\Logs\*.log`},
			wantErr:         true,
			errContains:     "must be greater than 0",
		},
		{
			name:            "too many lines",
			logPath:         `C:\Logs\app.log`,
			lines:           20000,
			allowedPatterns: []string{`C:\Logs\*.log`},
			wantErr:         true,
			errContains:     "cannot exceed 10000",
		},
		{
			name:            "path traversal attempt",
			logPath:         `C:\Logs\..\Windows\win.ini`,
			lines:           10,
			allowedPatterns: []string{`C:\Logs\*.log`},
			wantErr:         true,
			errContains:     "not in allowed list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executor.FetchLogLines(tt.logPath, tt.lines, tt.allowedPatterns)
			if (err != nil) != tt.wantErr {
				t.Errorf("FetchLogLines() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if err == nil || !containsString(err.Error(), tt.errContains) {
					t.Errorf("FetchLogLines() error = %v, want error containing %q", err, tt.errContains)
				}
			}
		})
	}
}
