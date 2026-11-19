package edge

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stone-age-io/agent/internal/platform"
	"github.com/stone-age-io/agent/internal/utils"
)

// LeafConfName is the file the bootstrap writes the leaf server config to. It is
// also what the embedded server loads by default, so the one-shot and the daemon
// agree without the operator naming the path twice.
const LeafConfName = "nats-leaf.conf"

// WriteLeafConfig turns a platform leaf config into the two files a stock
// nats-server needs: nats-leaf.conf and the credentials it references.
//
// Nothing here talks to the network — the fetch is platform.Client.LeafConfig,
// and the separation is the point. Generating the file is pure, so
// TestBuildLeafConfIsAcceptedByNATSServer can run the real generator's output
// through nats-server's own loader without a platform or a bus in sight.
//
// The creds file is written 0600: it embeds the nkey seed. The conf is 0644 —
// it carries only public trust material and a path to the creds.
//
// Both files go through a temp file and a rename, like every other secret the
// agent writes. The conf has to: a running agent rewrites it on a timer and
// reloads the leaf from it, and a reload that catches a half-written file is
// refused. The creds were once an in-place os.WriteFile, the one exception to
// that rule; a crash mid-write left a truncated credential where a running
// leaf or agent would re-read it on its next reconnect.
func WriteLeafConfig(lc *platform.LeafConfig, outputDir, credsFile string) (confPath, credsPath string, err error) {
	// 0755 matches the conf's 0644: it holds only public trust material. The
	// creds are the secret, and they are written 0600.
	if err := os.MkdirAll(outputDir, 0o755); err != nil { //nolint:gosec // G301, see above
		return "", "", err
	}

	// The creds go beside the conf, and the conf names them -- and its
	// store_dir and pid_file -- by ABSOLUTE path. nats-server resolves a
	// relative path against its working directory, not the conf's, so the
	// base name this used to write broke whenever the leaf ran from anywhere
	// else. The pair is therefore NOT relocatable: move the directory and
	// re-run `agent -leaf-config` there (or let a running agent's refresh
	// rewrite the conf).
	credsPath = filepath.Join(outputDir, filepath.Base(credsFile))
	if err := utils.WriteFileAtomic(credsPath, []byte(lc.Creds), 0o600); err != nil {
		return "", "", fmt.Errorf("write creds: %w", err)
	}

	conf, err := leafConf(lc, outputDir, credsFile)
	if err != nil {
		return "", "", err
	}
	confPath = filepath.Join(outputDir, LeafConfName)
	if err := utils.WriteFileAtomic(confPath, []byte(conf), 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", LeafConfName, err)
	}

	return confPath, credsPath, nil
}
