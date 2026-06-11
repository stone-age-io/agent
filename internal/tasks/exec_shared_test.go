package tasks

import (
	"bytes"
	"strings"
	"testing"
)

// TestLimitedBuffer tests the command output cap
func TestLimitedBuffer(t *testing.T) {
	t.Run("under cap passes through", func(t *testing.T) {
		var b limitedBuffer
		n, err := b.Write([]byte("hello"))
		if err != nil || n != 5 {
			t.Fatalf("Write() = (%d, %v), want (5, nil)", n, err)
		}
		if b.String() != "hello" || b.truncated {
			t.Errorf("got %q (truncated=%v), want %q untruncated", b.String(), b.truncated, "hello")
		}
	})

	t.Run("over cap truncates without failing", func(t *testing.T) {
		var b limitedBuffer
		chunk := bytes.Repeat([]byte("x"), 1024*1024) // 1MB
		for i := 0; i < 11; i++ {
			n, err := b.Write(chunk)
			if err != nil || n != len(chunk) {
				t.Fatalf("Write() = (%d, %v), want (%d, nil): writes must never fail", n, err, len(chunk))
			}
		}
		if b.Len() != maxCommandOutputBytes {
			t.Errorf("Len() = %d, want %d", b.Len(), maxCommandOutputBytes)
		}
		if !b.truncated {
			t.Error("truncated = false, want true after exceeding cap")
		}
	})
}

// TestCombineOutput tests stdout/stderr merging and the truncation notice
func TestCombineOutput(t *testing.T) {
	t.Run("stdout only", func(t *testing.T) {
		var stdout, stderr limitedBuffer
		stdout.Write([]byte("out"))
		if got := combineOutput(&stdout, &stderr); got != "out" {
			t.Errorf("combineOutput() = %q, want %q", got, "out")
		}
	})

	t.Run("stdout and stderr", func(t *testing.T) {
		var stdout, stderr limitedBuffer
		stdout.Write([]byte("out"))
		stderr.Write([]byte("err"))
		want := "out\nSTDERR:\nerr"
		if got := combineOutput(&stdout, &stderr); got != want {
			t.Errorf("combineOutput() = %q, want %q", got, want)
		}
	})

	t.Run("truncation notice appended", func(t *testing.T) {
		var stdout, stderr limitedBuffer
		stdout.truncated = true
		stdout.Write([]byte("out"))
		got := combineOutput(&stdout, &stderr)
		if !strings.Contains(got, "output truncated") {
			t.Errorf("combineOutput() = %q, want truncation notice", got)
		}
	})
}
