package tasks

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

// TestIsCommandAllowed tests command whitelist validation
// This is CRITICAL for security - prevents arbitrary command execution
func TestIsCommandAllowed(t *testing.T) {
	tests := []struct {
		name            string
		command         string
		allowedCommands []string
		scriptsDir      string
		want            bool
		reason          string
	}{
		// Valid cases - exact matches
		{
			name:    "exact match",
			command: "Get-Process",
			allowedCommands: []string{
				"Get-Process",
				"Get-Service",
			},
			scriptsDir: "",
			want:       true,
			reason:     "exact command match should be allowed",
		},
		{
			name:    "exact match with parameters",
			command: "Get-Process | Sort-Object CPU -Descending | Select-Object -First 5",
			allowedCommands: []string{
				"Get-Process | Sort-Object CPU -Descending | Select-Object -First 5",
			},
			scriptsDir: "",
			want:       true,
			reason:     "exact command with parameters should be allowed",
		},
		{
			name:    "match from multiple allowed",
			command: "Get-NetIPAddress",
			allowedCommands: []string{
				"Get-Process",
				"Get-NetIPAddress",
				"Get-Service",
			},
			scriptsDir: "",
			want:       true,
			reason:     "should match one of multiple allowed commands",
		},

		// Whitespace normalization
		{
			name:    "extra spaces normalized",
			command: "Get-Process  |  Sort-Object   CPU",
			allowedCommands: []string{
				"Get-Process | Sort-Object CPU",
			},
			scriptsDir: "",
			want:       true,
			reason:     "extra whitespace should be normalized",
		},
		{
			name:    "leading/trailing spaces",
			command: "  Get-Process  ",
			allowedCommands: []string{
				"Get-Process",
			},
			scriptsDir: "",
			want:       true,
			reason:     "leading and trailing spaces should be trimmed",
		},
		{
			name:    "tabs converted to spaces",
			command: "Get-Process\t|\tSort-Object\tCPU",
			allowedCommands: []string{
				"Get-Process | Sort-Object CPU",
			},
			scriptsDir: "",
			want:       true,
			reason:     "tabs should be normalized to spaces",
		},

		// Invalid cases - security critical
		{
			name:    "not in whitelist",
			command: "Remove-Item -Force",
			allowedCommands: []string{
				"Get-Process",
				"Get-Service",
			},
			scriptsDir: "",
			want:       false,
			reason:     "command not in whitelist must be rejected",
		},
		{
			name:    "partial match",
			command: "Get-Process | Sort-Object CPU",
			allowedCommands: []string{
				"Get-Process",
			},
			scriptsDir: "",
			want:       false,
			reason:     "partial match must be rejected - exact match required",
		},
		{
			name:    "extra parameters",
			command: "Get-Process -Name chrome",
			allowedCommands: []string{
				"Get-Process",
			},
			scriptsDir: "",
			want:       false,
			reason:     "additional parameters must be rejected",
		},
		{
			name:    "prefix match attempt",
			command: "Get-Process; Remove-Item",
			allowedCommands: []string{
				"Get-Process",
			},
			scriptsDir: "",
			want:       false,
			reason:     "command chaining attempt must be rejected",
		},
		{
			name:    "similar but different command",
			command: "Get-Processes",
			allowedCommands: []string{
				"Get-Process",
			},
			scriptsDir: "",
			want:       false,
			reason:     "similar command name must be rejected",
		},
		{
			name:    "case difference",
			command: "get-process",
			allowedCommands: []string{
				"Get-Process",
			},
			scriptsDir: "",
			want:       false,
			reason:     "case differences must be rejected - exact match required",
		},
		{
			name:            "empty allowed list",
			command:         "Get-Process",
			allowedCommands: []string{},
			scriptsDir:      "",
			want:            false,
			reason:          "empty whitelist means nothing allowed",
		},
		{
			name:    "command injection attempt",
			command: "Get-Process && malicious-command",
			allowedCommands: []string{
				"Get-Process",
			},
			scriptsDir: "",
			want:       false,
			reason:     "command injection attempt must be rejected",
		},
		{
			name:    "pipe to dangerous command",
			command: "Get-Process | Remove-Item",
			allowedCommands: []string{
				"Get-Process",
			},
			scriptsDir: "",
			want:       false,
			reason:     "piping to non-whitelisted command must be rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCommandAllowed(tt.command, tt.allowedCommands, tt.scriptsDir)
			if got != tt.want {
				t.Errorf("isCommandAllowed() = %v, want %v: %s", got, tt.want, tt.reason)
			}
		})
	}
}

// TestNormalizeWhitespace tests whitespace normalization
func TestNormalizeWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single space",
			input: "Get-Process",
			want:  "Get-Process",
		},
		{
			name:  "multiple spaces",
			input: "Get-Process  |  Sort-Object",
			want:  "Get-Process | Sort-Object",
		},
		{
			name:  "leading spaces",
			input: "  Get-Process",
			want:  "Get-Process",
		},
		{
			name:  "trailing spaces",
			input: "Get-Process  ",
			want:  "Get-Process",
		},
		{
			name:  "tabs",
			input: "Get-Process\t|\tSort",
			want:  "Get-Process | Sort",
		},
		{
			name:  "mixed whitespace",
			input: "  Get-Process  \t  |  \t Sort  ",
			want:  "Get-Process | Sort",
		},
		{
			name:  "newlines",
			input: "Get-Process\n|\nSort",
			want:  "Get-Process | Sort",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only spaces",
			input: "    ",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeWhitespace(tt.input)
			if got != tt.want {
				t.Errorf("normalizeWhitespace() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestExecuteCommand tests command execution validation
func TestExecuteCommand(t *testing.T) {
	// Note: These tests validate the whitelist logic, not actual PowerShell execution
	// Actual PowerShell execution tests would require Windows and are integration tests
	
	// FIXED: Use zap.NewNop() instead of nil to avoid panic
	executor := NewExecutor(zap.NewNop(), 0, context.Background())

	tests := []struct {
		name            string
		command         string
		allowedCommands []string
		scriptsDir      string
		wantErr         bool
		errContains     string
	}{
		{
			name:    "not in whitelist",
			command: "Remove-Item -Force",
			allowedCommands: []string{
				"Get-Process",
			},
			scriptsDir:  "",
			wantErr:     true,
			errContains: "not in allowed list",
		},
		{
			name:            "empty whitelist",
			command:         "Get-Process",
			allowedCommands: []string{},
			scriptsDir:      "",
			wantErr:         true,
			errContains:     "not in allowed list",
		},
		{
			name:    "command injection attempt",
			command: "Get-Process; Remove-Item",
			allowedCommands: []string{
				"Get-Process",
			},
			scriptsDir:  "",
			wantErr:     true,
			errContains: "not in allowed list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: This will fail at the PowerShell execution stage if allowed
			// We're just testing whitelist validation here
			_, _, err := executor.ExecuteCommand(tt.command, tt.allowedCommands, tt.scriptsDir, 0)
			
			if (err != nil) != tt.wantErr {
				t.Errorf("ExecuteCommand() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			
			if tt.wantErr && tt.errContains != "" {
				if err == nil || indexOf(err.Error(), tt.errContains) < 0 {
					t.Errorf("ExecuteCommand() error = %v, want error containing %q", err, tt.errContains)
				}
			}
		})
	}
}
