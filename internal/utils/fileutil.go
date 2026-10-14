package utils

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data through a temp file in the same directory and
// renames it into place, so a crash mid-write cannot leave a truncated file.
//
// What that protects is different for each caller, and every one of them
// matters: an agent that cannot parse its own .creds, platform session or Nebula
// cache does not come back on its own, and a nats-server handed a half-written
// nats-leaf.conf on reload or restart refuses it. The rename is what makes a
// reader see either the old file or the new one, never a mixture.
//
// perm is applied to the temp file before anything is written to it, so the
// secrets (0600) are never readable by anyone else even for an instant. It is
// set with Chmod rather than at create time, so the umask does not get a vote:
// a 0644 config is 0644 on every box. os.CreateTemp alone opens 0600, which is
// what every caller wanted before the leaf config needed to be world-readable.
//
// It used to be two identical unexported copies, in platform and in nebula, and
// both were 0600-only.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op once the rename succeeds

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close() //nolint:errcheck // chmod error is the one that matters
		return fmt.Errorf("failed to set mode on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck // write error is the one that matters
		return fmt.Errorf("failed to write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to install %s: %w", path, err)
	}

	return nil
}
