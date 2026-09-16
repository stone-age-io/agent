package main

import (
	"fmt"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/stone-age-io/agent/internal/config"
	"github.com/stone-age-io/agent/internal/edge"
	"github.com/stone-age-io/agent/internal/platform"
)

// writeLeafConfig implements `agent -leaf-config`: fetch this thing's NATS leaf
// configuration from the platform and write the two files a stock nats-server
// needs.
//
// It authenticates as the same Thing the agent runs as, over the same client,
// and the platform serves it because everything in the response is either public
// trust material or this thing's own credential. There is no gateway flag to set
// first and no separate kind of record to be.
func writeLeafConfig(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.NATS.Auth.Type != "platform" {
		return fmt.Errorf("nats.auth.type is %q: a leaf config comes from the platform, so it needs platform auth",
			cfg.NATS.Auth.Type)
	}
	if cfg.NATS.Auth.CredsFile == "" {
		return fmt.Errorf("nats.auth.creds_file is required: it names the credentials file written beside the generated config")
	}

	// A one-shot on an operator's terminal, so the log goes to the console at
	// info level rather than through the agent's file logger.
	logger, err := zap.NewDevelopment()
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()

	lc, err := platform.NewClient(cfg, logger).LeafConfig()
	if err != nil {
		return err
	}

	outputDir := filepath.Dir(cfg.NATS.Auth.CredsFile)
	confPath, credsPath, err := edge.WriteLeafConfig(lc, outputDir, cfg.NATS.Auth.CredsFile)
	if err != nil {
		return err
	}

	fmt.Printf("✅ Wrote %s\n✅ Wrote %s\n", confPath, credsPath)
	fmt.Printf("\nJetStream domain: %s (this thing's code)\nHub: %s\n", lc.Domain, lc.HubLeafURL)
	fmt.Printf("\nNext, start the leaf:\n  nats-server -c %s\n", confPath)
	fmt.Printf("\nOr let the agent host it, by setting in %s:\n  nats:\n    server_config: %s\n",
		configPath, confPath)

	return nil
}
