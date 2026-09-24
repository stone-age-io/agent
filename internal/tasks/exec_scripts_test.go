package tasks

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScriptPath is the cmd.exec script gate. Every refusal here is a string
// the old check approved, or one next to it: the old check reduced the request
// to filepath.Base, confirmed a script of that name existed, and then ran the
// unreduced request through a shell.
//
// Both separators appear on every platform. Where `\` is not a separator the
// string is a bare filename that does not exist, so it is refused either way.
func TestScriptPath(t *testing.T) {
	dir := t.TempDir()
	script := "deploy" + scriptExt
	if err := os.WriteFile(filepath.Join(dir, script), []byte("echo ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", script), []byte("echo ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir"+scriptExt), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		command string
		dir     string
		want    bool
	}{
		{"bare filename", script, dir, true},

		{"command substitution before the name", "$(touch pwned)/" + script, dir, false},
		{"command substitution, backslash", `$(New-Item pwned)\` + script, dir, false},
		{"absolute path elsewhere", "/tmp/elsewhere/" + script, dir, false},
		{"drive path elsewhere", `C:\elsewhere\` + script, dir, false},
		{"drive-relative", "C:" + script, dir, false},
		{"parent traversal", "../" + script, dir, false},
		{"parent traversal, backslash", `..\` + script, dir, false},
		{"subdirectory of the scripts dir", "sub/" + script, dir, false},
		{"full path inside the scripts dir", filepath.Join(dir, script), dir, false},

		{"missing script", "missing" + scriptExt, dir, false},
		{"directory with the script extension", "dir" + scriptExt, dir, false},
		{"wrong extension", "deploy.txt", dir, false},
		{"no scripts directory", script, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, got := scriptPath(tt.command, tt.dir)
			if got != tt.want {
				t.Fatalf("scriptPath(%q) = %v, want %v", tt.command, got, tt.want)
			}
			if got && path != filepath.Join(dir, script) {
				t.Errorf("scriptPath(%q) path = %q, want %q", tt.command, path, filepath.Join(dir, script))
			}
		})
	}
}

// TestAllowedCommandReturnsTheEntry: what runs is the operator's entry, not
// the caller's string, even though the two compare equal.
func TestAllowedCommandReturnsTheEntry(t *testing.T) {
	entry := "echo one two"
	got, ok := allowedCommand("echo one\ntwo", []string{entry})
	if !ok {
		t.Fatal("request differing only in whitespace should match")
	}
	if got != entry {
		t.Errorf("allowedCommand() = %q, want the allowlist entry %q", got, entry)
	}
}
