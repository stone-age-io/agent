package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/kardianos/service"
	"win-agent/internal/agent"
)

var (
	version = "1.0.0" // Set via -ldflags during build
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

	flag.StringVar(&configPath, "config", "C:\\ProgramData\\WinAgent\\config.yaml", "Path to configuration file")
	flag.StringVar(&svcFlag, "service", "", "Control the system service: install, uninstall, start, stop, restart")
	flag.Parse()

	// Service configuration
	svcConfig := &service.Config{
		Name:        "win-agent",
		DisplayName: "Windows Agent",
		Description: "Lightweight Windows management and observability agent",
		Arguments:   []string{"-config", configPath},
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
	p.logger.Infof("Starting win-agent version %s", version)

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
	p.logger.Info("Stopping win-agent")

	if p.agent != nil {
		if err := p.agent.Shutdown(); err != nil {
			p.logger.Errorf("Error during shutdown: %v", err)
			return err
		}
	}

	return nil
}
