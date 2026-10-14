package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The mode is the reason the helper takes a parameter at all: the leaf config
// is 0644 because a separately supervised nats-server may run as another user,
// and every secret beside it stays 0600. Replacing a file must apply the new
// mode too, not inherit the old one.
func TestWriteFileAtomicAppliesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX permission bits to assert on")
	}

	dir := t.TempDir()
	for _, perm := range []os.FileMode{0o600, 0o644} {
		path := filepath.Join(dir, "file")
		if err := WriteFileAtomic(path, []byte("data"), perm); err != nil {
			t.Fatalf("WriteFileAtomic(%o) error: %v", perm, err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != perm {
			t.Errorf("mode = %o, want %o", got, perm)
		}
	}
}

func TestWriteFileAtomicReplacesAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file")

	for _, body := range []string{"first", "second"} {
		if err := WriteFileAtomic(path, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFileAtomic error: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body {
			t.Errorf("content = %q, want %q", got, body)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want 1: a temp file was left behind", len(entries))
	}
}
