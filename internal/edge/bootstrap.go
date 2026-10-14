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
// The conf goes through a temp file and a rename. It used to be a plain
// os.WriteFile, which was harmless while the only reader was a server started
// by hand afterwards; now a running agent rewrites the same file on a timer and
// reloads the leaf from it, and a reload that catches a half-written file is
// refused. The creds write is unchanged: it is a one-shot's in-place write, and
// keeping the credential current afterwards is the credential sync's job.
func WriteLeafConfig(lc *platform.LeafConfig, outputDir, credsFile string) (confPath, credsPath string, err error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
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
	if err := os.WriteFile(credsPath, []byte(lc.Creds), 0o600); err != nil {
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
