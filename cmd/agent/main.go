package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/kardianos/service"
	"github.com/stone-age-io/agent/internal/agent"
	"github.com/stone-age-io/agent/internal/config"
)

var (
	// Stamped at build time via -ldflags "-X main.version=<version>", which is
	// what goreleaser does on a tag. "dev" is deliberate for a plain go build:
	// this string is reported in every heartbeat and command reply, so a default
	// of "1.0.0" meant an unreleased build claiming a release number in
	// telemetry.
	version = "dev"
)

// program implements the service.Interface
type program struct {
	agent      *agent.Agent
	configPath string
	logger     service.Logger
}

func main() {
	// Parse command line flags
	var configPath string
	var svcFlag string

	// Use platform-specific default config path
	defaultConfigPath := config.GetDefaultConfigPath()

	flag.StringVar(&configPath, "config", defaultConfigPath, "Path to configuration file")
	flag.StringVar(&svcFlag, "service", "", "Control the system service: install, uninstall, start, stop, restart")
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "Print the version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println("agent", version)
		return
	}

	// Service configuration
	svcConfig := &service.Config{
		Name:        "agent",
		DisplayName: "Stone Age Agent",
		Description: "Lightweight NATS-native management and observability agent",
		Arguments:   []string{"-config", configPath},

		// Restart on failure. The agent exits rather than limps when it cannot
		// connect to NATS — a rejected credential, for one — and getting restarted
		// is what gives it another chance to re-sync its credentials from the
		// platform and heal. systemd gets Restart=always from this library by
		// default, but Windows configures no recovery action unless asked.
		Option: service.KeyValue{
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "15s",
			"OnFailureResetPeriod":   10,
		},
	}

	prg := &program{
		configPath: configPath,
	}

	// Create service
	s, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatal(err)
	}

	// Setup service logger
	errs := make(chan error, 5)
	logger, err := s.Logger(errs)
	if err != nil {
		log.Fatal(err)
	}
	prg.logger = logger

	// Consume service logger errors in background
	// These are errors from the service framework itself
	go func() {
		for err := range errs {
			log.Printf("Service framework error: %v", err)
		}
	}()

	// Handle service control commands
	if len(svcFlag) != 0 {
		err := service.Control(s, svcFlag)
		if err != nil {
			log.Printf("Valid actions: %q\n", service.ControlAction)
			log.Fatal(err)
		}
		return
	}

	// Run the service
	err = s.Run()
	if err != nil {
		logger.Error(err)
	}
}

// Start implements service.Interface
func (p *program) Start(s service.Service) error {
	p.logger.Infof("Starting agent version %s", version)

	// Create agent
	ag, err := agent.New(p.configPath, version)
	if err != nil {
		return fmt.Errorf("failed to create agent: %w", err)
	}

	p.agent = ag

	// Start agent in goroutine
	go func() {
		if err := p.agent.Run(); err != nil {
			p.logger.Errorf("Agent error: %v", err)
		}
	}()

	return nil
}

// Stop implements service.Interface
func (p *program) Stop(s service.Service) error {
	p.logger.Info("Stopping agent")

	if p.agent != nil {
		if err := p.agent.Shutdown(); err != nil {
			p.logger.Errorf("Error during shutdown: %v", err)
			return err
		}
	}

	return nil
}
