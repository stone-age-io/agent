package tasks

// Shared helpers for the platform-specific command execution implementations
// (exec_unix.go, exec_windows.go).

import (
	"bytes"
	"strings"
)

// maxCommandOutputBytes caps captured stdout/stderr so a runaway command
// cannot exhaust agent memory. Output beyond the cap is discarded.
const maxCommandOutputBytes = 10 * 1024 * 1024 // 10MB

// limitedBuffer is an io.Writer that keeps the first maxCommandOutputBytes
// and silently discards the rest. Writes never fail, so the command keeps
// running to completion even after the cap is hit.
type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := maxCommandOutputBytes - b.buf.Len()
	if remaining >= len(p) {
		return b.buf.Write(p)
	}
	if remaining > 0 {
		b.buf.Write(p[:remaining])
	}
	b.truncated = true
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
func (b *limitedBuffer) Len() int       { return b.buf.Len() }

// combineOutput merges stdout and stderr into the single output string
// returned to the control plane, noting any truncation.
func combineOutput(stdout, stderr *limitedBuffer) string {
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += "STDERR:\n" + stderr.String()
	}
	if stdout.truncated || stderr.truncated {
		output += "\n[output truncated: exceeded 10MB limit]"
	}
	return output
}

// normalizeWhitespace normalizes whitespace in a command for comparison
func normalizeWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
