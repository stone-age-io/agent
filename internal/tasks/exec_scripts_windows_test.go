//go:build windows

package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sys/windows"
)

// TestAllowedCommand tests command allowlist validation
// This is CRITICAL for security - prevents arbitrary command execution
func TestAllowedCommand(t *testing.T) {
	tests := []struct {
		name            string
		command         string
		allowedCommands []string
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
			want:   true,
			reason: "exact command match should be allowed",
		},
		{
			name:    "exact match with parameters",
			command: "Get-Process | Sort-Object CPU -Descending | Select-Object -First 5",
			allowedCommands: []string{
				"Get-Process | Sort-Object CPU -Descending | Select-Object -First 5",
			},
			want:   true,
			reason: "exact command with parameters should be allowed",
		},
		{
			name:    "match from multiple allowed",
			command: "Get-NetIPAddress",
			allowedCommands: []string{
				"Get-Process",
				"Get-NetIPAddress",
				"Get-Service",
			},
			want:   true,
			reason: "should match one of multiple allowed commands",
		},

		// Whitespace normalization
		{
			name:    "extra spaces normalized",
			command: "Get-Process  |  Sort-Object   CPU",
			allowedCommands: []string{
				"Get-Process | Sort-Object CPU",
			},
			want:   true,
			reason: "extra whitespace should be normalized",
		},
		{
			name:    "leading/trailing spaces",
			command: "  Get-Process  ",
			allowedCommands: []string{
				"Get-Process",
			},
			want:   true,
			reason: "leading and trailing spaces should be trimmed",
		},
		{
			name:    "tabs converted to spaces",
			command: "Get-Process\t|\tSort-Object\tCPU",
			allowedCommands: []string{
				"Get-Process | Sort-Object CPU",
			},
			want:   true,
			reason: "tabs should be normalized to spaces",
		},

		// Invalid cases - security critical
		{
			name:    "not in whitelist",
			command: "Remove-Item -Force",
			allowedCommands: []string{
				"Get-Process",
				"Get-Service",
			},
			want:   false,
			reason: "command not in whitelist must be rejected",
		},
		{
			name:    "partial match",
			command: "Get-Process | Sort-Object CPU",
			allowedCommands: []string{
				"Get-Process",
			},
			want:   false,
			reason: "partial match must be rejected - exact match required",
		},
		{
			name:    "extra parameters",
			command: "Get-Process -Name chrome",
			allowedCommands: []string{
				"Get-Process",
			},
			want:   false,
			reason: "additional parameters must be rejected",
		},
		{
			name:    "prefix match attempt",
			command: "Get-Process; Remove-Item",
			allowedCommands: []string{
				"Get-Process",
			},
			want:   false,
			reason: "command chaining attempt must be rejected",
		},
		{
			name:    "similar but different command",
			command: "Get-Processes",
			allowedCommands: []string{
				"Get-Process",
			},
			want:   false,
			reason: "similar command name must be rejected",
		},
		{
			name:    "case difference",
			command: "get-process",
			allowedCommands: []string{
				"Get-Process",
			},
			want:   false,
			reason: "case differences must be rejected - exact match required",
		},
		{
			name:            "empty allowed list",
			command:         "Get-Process",
			allowedCommands: []string{},
			want:            false,
			reason:          "empty whitelist means nothing allowed",
		},
		{
			name:    "command injection attempt",
			command: "Get-Process && malicious-command",
			allowedCommands: []string{
				"Get-Process",
			},
			want:   false,
			reason: "command injection attempt must be rejected",
		},
		{
			name:    "pipe to dangerous command",
			command: "Get-Process | Remove-Item",
			allowedCommands: []string{
				"Get-Process",
			},
			want:   false,
			reason: "piping to non-whitelisted command must be rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := allowedCommand(tt.command, tt.allowedCommands)
			if got != tt.want {
				t.Errorf("allowedCommand() = %v, want %v: %s", got, tt.want, tt.reason)
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

	// Create executor with builtin metrics source for tests
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
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("ExecuteCommand() error = %v, want error containing %q", err, tt.errContains)
				}
			}
		})
	}
}

// TestExecuteCommandScripts is the Windows side of the unix test of the same
// name: the refusal only counts if nothing ran.
func TestExecuteCommandScripts(t *testing.T) {
	executor := NewExecutor(zap.NewNop(), 0, context.Background())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.ps1"), []byte("Write-Output ok\r\nexit 3\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("script runs and its exit code survives", func(t *testing.T) {
		// -File passes the script's own exit code through; -Command did not.
		out, code, _ := executor.ExecuteCommand("deploy.ps1", nil, dir, 30*time.Second)
		if code != 3 || !strings.Contains(out, "ok") {
			t.Fatalf("got (%q, %d), want output containing \"ok\" and exit code 3", out, code)
		}
	})

	t.Run("injection before a real script name is refused and does not run", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "pwned")
		_, _, err := executor.ExecuteCommand("$(New-Item -Path '"+marker+"')\\deploy.ps1", nil, dir, 30*time.Second)
		if err == nil || !strings.Contains(err.Error(), "not in allowed list") {
			t.Fatalf("err = %v, want a refusal", err)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatal("the injected command ran")
		}
	})
}

// TestTimeoutKillsTheProcessTree: the timeout kills what powershell.exe
// started, not only powershell.exe. The child is a ping, started with
// Start-Process so it is a separate process, and it writes its pid to a file
// because partial stdout from PowerShell may still be buffered when the kill
// lands.
func TestTimeoutKillsTheProcessTree(t *testing.T) {
	executor := NewExecutor(zap.NewNop(), 0, context.Background())

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := "$p = Start-Process -FilePath ping.exe -ArgumentList '-n','60','127.0.0.1' -PassThru -NoNewWindow; " +
		"Set-Content -Path '" + pidFile + "' -Value $p.Id; Wait-Process -Id $p.Id"

	_, code, err := executor.ExecuteCommand(cmd, []string{cmd}, "", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "timeout") || code != -1 {
		t.Fatalf("got (%d, %v), want a timeout with exit code -1", code, err)
	}

	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("the command never wrote its child's pid: %v", readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if convErr != nil {
		t.Fatalf("pid file = %q", raw)
	}

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if !processAlive(pid) {
			return
		}
	}
	t.Errorf("child %d survived the timeout", pid)
}

// processAlive asks Windows directly: a process that has exited reports an
// exit code other than STILL_ACTIVE (259) even while a handle keeps it listed.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259
}
