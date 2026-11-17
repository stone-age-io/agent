package config

import (
	"os"
	"path/filepath"
	"regexp"
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

// TestExampleConfigsAgree keeps the three example configs in step: they may
// differ only inside double-quoted strings (paths, service names, commands).
// Comments, keys and layout must match line for line. They had drifted -- the
// Windows file had comments the others lacked and was missing a port note --
// because nothing said they were meant to be one file.
func TestExampleConfigsAgree(t *testing.T) {
	quoted := regexp.MustCompile(`"[^"]*"`)
	read := func(goos string) []string {
		raw, err := os.ReadFile(filepath.Join("..", "..", "configs", goos, "config.yaml.example"))
		if err != nil {
			t.Fatalf("read %s example: %v", goos, err)
		}
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		for i, l := range lines {
			lines[i] = quoted.ReplaceAllString(l, `""`)
		}
		return lines
	}

	want := read("linux")
	for _, goos := range []string{"freebsd", "windows"} {
		got := read(goos)
		for i := 0; i < len(want) || i < len(got); i++ {
			var w, g string
			if i < len(want) {
				w = want[i]
			}
			if i < len(got) {
				g = got[i]
			}
			if w != g {
				t.Errorf("%s example differs from linux at line %d outside quotes:\n linux: %s\n %s: %s", goos, i+1, w, goos, g)
				break
			}
		}
	}
}
