package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExampleConfigsParse loads every shipped example config through the real
// loader.
//
// These files go out in the release archive and are the first thing a new
// install copies, so a typo in one is a broken first boot for somebody. The
// Windows examples are the reason this exists: its paths are double-quoted YAML
// containing backslashes, where a single backslash is an invalid escape and the
// file stops parsing.
//
// The examples are not expected to be *valid* — they carry placeholder codes and
// commented-out sections — so this asserts only that they parse. A validation
// error means the YAML was read.
func TestExampleConfigsParse(t *testing.T) {
	for _, goos := range []string{"linux", "freebsd", "windows"} {
		t.Run(goos, func(t *testing.T) {
			path := filepath.Join("..", "..", "configs", goos, "config.yaml.example")

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read example config: %v", err)
			}

			// Load reads by path, and the loader keys off the file name, so give it
			// a real config.yaml in a temp directory.
			dir := t.TempDir()
			dst := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(dst, raw, 0600); err != nil {
				t.Fatalf("write temp config: %v", err)
			}

			_, err = Load(dst)
			if err == nil {
				return // parsed and validated
			}

			// A YAML problem is what this test is for; a validation complaint means
			// the file was read, which is all the examples promise.
			msg := err.Error()
			for _, marker := range []string{"yaml:", "cannot unmarshal", "unmarshal", "did not find", "found character"} {
				if strings.Contains(strings.ToLower(msg), marker) {
					t.Fatalf("example config does not parse: %v", err)
				}
			}
			t.Logf("parsed; validation reported: %v", err)
		})
	}
}
