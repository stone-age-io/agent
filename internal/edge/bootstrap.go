package edge

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stone-age-io/agent/internal/platform"
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
func WriteLeafConfig(lc *platform.LeafConfig, outputDir, credsFile string) (confPath, credsPath string, err error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", "", err
	}

	// The conf references the creds by BASE NAME, and nats-server resolves it
	// relative to the conf's own directory. Keeping both in outputDir is what
	// makes the pair relocatable — copy the directory to an appliance and it
	// still loads.
	credsName := filepath.Base(credsFile)
	credsPath = filepath.Join(outputDir, credsName)
	if err := os.WriteFile(credsPath, []byte(lc.Creds), 0o600); err != nil {
		return "", "", fmt.Errorf("write creds: %w", err)
	}

	conf := buildLeafConf(leafConfParams{
		OperatorJWT:   lc.OperatorJWT,
		AccountPub:    lc.AccountPub,
		AccountJWT:    lc.AccountJWT,
		SysAccountPub: lc.SysAccountPub,
		SysAccountJWT: lc.SysAccountJWT,
		Domain:        lc.Domain,
		HubLeafURL:    lc.HubLeafURL,
		CredsName:     credsName,
	})

	confPath = filepath.Join(outputDir, LeafConfName)
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", LeafConfName, err)
	}

	return confPath, credsPath, nil
}
