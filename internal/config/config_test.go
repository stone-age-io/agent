package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestValidateCode tests agent code validation
func TestValidateCode(t *testing.T) {
	tests := []struct {
		name     string
		deviceID string
		wantErr  bool
		errText  string
	}{
		// Valid device IDs
		{
			name:     "alphanumeric",
			deviceID: "device123",
			wantErr:  false,
		},
		{
			name:     "with dashes",
			deviceID: "device-123-abc",
			wantErr:  false,
		},
		{
			name:     "with underscores",
			deviceID: "device_123_abc",
			wantErr:  false,
		},
		{
			name:     "mixed valid characters",
			deviceID: "dev-ice_123-ABC",
			wantErr:  false,
		},
		{
			name:     "UUID format",
			deviceID: "550e8400-e29b-41d4-a716-446655440000",
			wantErr:  false,
		},

		// Invalid device IDs
		{
			name:     "empty",
			deviceID: "",
			wantErr:  true,
			errText:  "code is required",
		},
		{
			name:     "with spaces",
			deviceID: "device 123",
			wantErr:  true,
			errText:  "must contain only alphanumeric",
		},
		{
			name:     "with dots",
			deviceID: "device.123",
			wantErr:  true,
			errText:  "must contain only alphanumeric",
		},
		{
			name:     "with special characters",
			deviceID: "device@123",
			wantErr:  true,
			errText:  "must contain only alphanumeric",
		},
		{
			name:     "with slash",
			deviceID: "device/123",
			wantErr:  true,
			errText:  "must contain only alphanumeric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Code:          tt.deviceID,
				SubjectPrefix: "agents",
				NATS: NATSConfig{
					URLs: []string{"nats://localhost:4222"},
					Auth: AuthConfig{Type: "none"},
				},
				Tasks: TasksConfig{
					Heartbeat:     HeartbeatConfig{Enabled: true, Interval: 1 * time.Minute},
					SystemMetrics: SystemMetricsConfig{Enabled: true, Interval: 5 * time.Minute},
					ServiceCheck:  ServiceCheckConfig{Enabled: false},
					Inventory:     InventoryConfig{Enabled: true, Interval: 24 * time.Hour},
				},
				Commands: CommandsConfig{
					Timeout: 30 * time.Second,
				},
				Logging: LoggingConfig{
					Level:      "info",
					File:       "test.log",
					MaxSizeMB:  100,
					MaxBackups: 3,
				},
			}

			err := validate(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errText != "" && err != nil {
				if indexOf(err.Error(), tt.errText) < 0 {
					t.Errorf("validate() error = %v, want error containing %q", err, tt.errText)
				}
			}
		})
	}
}

// TestValidateSubjectPrefix tests subject prefix validation
func TestValidateSubjectPrefix(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		wantErr bool
		errText string
	}{
		// Valid prefixes
		{
			name:    "simple prefix",
			prefix:  "agents",
			wantErr: false,
		},
		{
			name:    "with dash",
			prefix:  "win-agents",
			wantErr: false,
		},
		{
			name:    "with underscore",
			prefix:  "win_agents",
			wantErr: false,
		},
		{
			name:    "hierarchical two levels",
			prefix:  "production.agents",
			wantErr: false,
		},
		{
			name:    "hierarchical three levels",
			prefix:  "region.dev.agents",
			wantErr: false,
		},
		{
			name:    "complex hierarchical",
			prefix:  "us-east-1.production.win-agents",
			wantErr: false,
		},
		{
			name:    "with numbers",
			prefix:  "region1.env2.agents3",
			wantErr: false,
		},
		{
			name:    "mixed characters",
			prefix:  "my_region.dev-env.agents",
			wantErr: false,
		},

		// Invalid prefixes
		{
			name:    "leading dot",
			prefix:  ".agents",
			wantErr: true,
			errText: "cannot start or end with a dot",
		},
		{
			name:    "trailing dot",
			prefix:  "agents.",
			wantErr: true,
			errText: "cannot start or end with a dot",
		},
		{
			name:    "consecutive dots",
			prefix:  "region..agents",
			wantErr: true,
			errText: "consecutive dots not allowed",
		},
		{
			name:    "only dot",
			prefix:  ".",
			wantErr: true,
			errText: "cannot start or end with a dot",
		},
		{
			name:    "special characters in token",
			prefix:  "region@dev.agents",
			wantErr: true,
			errText: "contains invalid characters",
		},
		{
			name:    "spaces",
			prefix:  "my region.agents",
			wantErr: true,
			errText: "contains invalid characters",
		},
		{
			name:    "forward slash",
			prefix:  "region/dev.agents",
			wantErr: true,
			errText: "contains invalid characters",
		},
		{
			name:    "wildcard",
			prefix:  "region.*.agents",
			wantErr: true,
			errText: "contains invalid characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSubjectPrefix(tt.prefix)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSubjectPrefix() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errText != "" && err != nil {
				if indexOf(err.Error(), tt.errText) < 0 {
					t.Errorf("validateSubjectPrefix() error = %v, want error containing %q", err, tt.errText)
				}
			}
		})
	}
}

// TestValidateSubjectPrefixInConfig tests subject prefix validation through full config validation
func TestValidateSubjectPrefixInConfig(t *testing.T) {
	tests := []struct {
		name          string
		subjectPrefix string
		wantErr       bool
		errText       string
	}{
		{
			name:          "default prefix",
			subjectPrefix: "agents",
			wantErr:       false,
		},
		{
			name:          "hierarchical prefix",
			subjectPrefix: "us-west.production.agents",
			wantErr:       false,
		},
		{
			name:          "too long",
			subjectPrefix: "this-is-a-very-long-prefix-that-exceeds-the-maximum-allowed-length-of-fifty-characters",
			wantErr:       true,
			errText:       "must not exceed 50 characters",
		},
		{
			name:          "leading dot",
			subjectPrefix: ".agents",
			wantErr:       true,
			errText:       "cannot start or end with a dot",
		},
		{
			name:          "empty prefix",
			subjectPrefix: "",
			wantErr:       true,
			errText:       "subject_prefix is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Code:          "test-device",
				SubjectPrefix: tt.subjectPrefix,
				NATS: NATSConfig{
					URLs: []string{"nats://localhost:4222"},
					Auth: AuthConfig{Type: "none"},
				},
				Tasks: TasksConfig{
					Heartbeat:     HeartbeatConfig{Enabled: true, Interval: 1 * time.Minute},
					SystemMetrics: SystemMetricsConfig{Enabled: true, Interval: 5 * time.Minute},
					ServiceCheck:  ServiceCheckConfig{Enabled: false},
					Inventory:     InventoryConfig{Enabled: true, Interval: 24 * time.Hour},
				},
				Commands: CommandsConfig{
					Timeout: 30 * time.Second,
				},
				Logging: LoggingConfig{
					Level:      "info",
					File:       "test.log",
					MaxSizeMB:  100,
					MaxBackups: 3,
				},
			}

			err := validate(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errText != "" && err != nil {
				if indexOf(err.Error(), tt.errText) < 0 {
					t.Errorf("validate() error = %v, want error containing %q", err, tt.errText)
				}
			}
		})
	}
}

// TestValidateNATSAuth tests NATS authentication validation
func TestValidateNATSAuth(t *testing.T) {
	tests := []struct {
		name    string
		auth    AuthConfig
		wantErr bool
		errText string
	}{
		// Valid configurations
		{
			name: "none auth",
			auth: AuthConfig{
				Type: "none",
			},
			wantErr: false,
		},
		{
			name: "token auth",
			auth: AuthConfig{
				Type:  "token",
				Token: "secret-token",
			},
			wantErr: false,
		},
		{
			name: "userpass auth",
			auth: AuthConfig{
				Type:     "userpass",
				Username: "user",
				Password: "pass",
			},
			wantErr: false,
		},

		// Invalid configurations
		{
			name: "invalid type",
			auth: AuthConfig{
				Type: "invalid",
			},
			wantErr: true,
			errText: "invalid auth type",
		},
		{
			name: "token missing",
			auth: AuthConfig{
				Type: "token",
			},
			wantErr: true,
			errText: "token is required",
		},
		{
			name: "userpass missing username",
			auth: AuthConfig{
				Type:     "userpass",
				Password: "pass",
			},
			wantErr: true,
			errText: "username and password are required",
		},
		{
			name: "userpass missing password",
			auth: AuthConfig{
				Type:     "userpass",
				Username: "user",
			},
			wantErr: true,
			errText: "username and password are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Code:          "test-device",
				SubjectPrefix: "agents",
				NATS: NATSConfig{
					URLs: []string{"nats://localhost:4222"},
					Auth: tt.auth,
				},
				Tasks: TasksConfig{
					Heartbeat:     HeartbeatConfig{Enabled: true, Interval: 1 * time.Minute},
					SystemMetrics: SystemMetricsConfig{Enabled: true, Interval: 5 * time.Minute},
					ServiceCheck:  ServiceCheckConfig{Enabled: false},
					Inventory:     InventoryConfig{Enabled: true, Interval: 24 * time.Hour},
				},
				Commands: CommandsConfig{
					Timeout: 30 * time.Second,
				},
				Logging: LoggingConfig{
					Level:      "info",
					File:       "test.log",
					MaxSizeMB:  100,
					MaxBackups: 3,
				},
			}

			err := validate(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errText != "" && err != nil {
				if indexOf(err.Error(), tt.errText) < 0 {
					t.Errorf("validate() error = %v, want error containing %q", err, tt.errText)
				}
			}
		})
	}
}

// TestValidateTLS tests TLS configuration validation
func TestValidateTLS(t *testing.T) {
	// Create temporary test files
	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "cert.pem")
	keyFile := filepath.Join(tmpDir, "key.pem")
	caFile := filepath.Join(tmpDir, "ca.pem")

	// Create dummy files
	os.WriteFile(certFile, []byte("cert"), 0644)
	os.WriteFile(keyFile, []byte("key"), 0644)
	os.WriteFile(caFile, []byte("ca"), 0644)

	tests := []struct {
		name    string
		tls     TLSConfig
		wantErr bool
		errText string
	}{
		// Valid configurations
		{
			name: "TLS disabled",
			tls: TLSConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "TLS enabled with no files",
			tls: TLSConfig{
				Enabled: true,
			},
			wantErr: false,
		},
		{
			name: "TLS with CA only",
			tls: TLSConfig{
				Enabled: true,
				CAFile:  caFile,
			},
			wantErr: false,
		},
		{
			name: "TLS with client cert and key",
			tls: TLSConfig{
				Enabled:  true,
				CertFile: certFile,
				KeyFile:  keyFile,
			},
			wantErr: false,
		},
		{
			name: "TLS with all files",
			tls: TLSConfig{
				Enabled:  true,
				CertFile: certFile,
				KeyFile:  keyFile,
				CAFile:   caFile,
			},
			wantErr: false,
		},

		// Invalid configurations
		{
			name: "cert without key",
			tls: TLSConfig{
				Enabled:  true,
				CertFile: certFile,
			},
			wantErr: true,
			errText: "key_file is required",
		},
		{
			name: "key without cert",
			tls: TLSConfig{
				Enabled: true,
				KeyFile: keyFile,
			},
			wantErr: true,
			errText: "cert_file is required",
		},
		{
			name: "cert file not found",
			tls: TLSConfig{
				Enabled:  true,
				CertFile: "/nonexistent/cert.pem",
				KeyFile:  keyFile,
			},
			wantErr: true,
			errText: "certificate file not found",
		},
		{
			name: "key file not found",
			tls: TLSConfig{
				Enabled:  true,
				CertFile: certFile,
				KeyFile:  "/nonexistent/key.pem",
			},
			wantErr: true,
			errText: "key file not found",
		},
		{
			name: "CA file not found",
			tls: TLSConfig{
				Enabled: true,
				CAFile:  "/nonexistent/ca.pem",
			},
			wantErr: true,
			errText: "CA file not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Code:          "test-device",
				SubjectPrefix: "agents",
				NATS: NATSConfig{
					URLs: []string{"nats://localhost:4222"},
					Auth: AuthConfig{Type: "none"},
					TLS:  tt.tls,
				},
				Tasks: TasksConfig{
					Heartbeat:     HeartbeatConfig{Enabled: true, Interval: 1 * time.Minute},
					SystemMetrics: SystemMetricsConfig{Enabled: true, Interval: 5 * time.Minute},
					ServiceCheck:  ServiceCheckConfig{Enabled: false},
					Inventory:     InventoryConfig{Enabled: true, Interval: 24 * time.Hour},
				},
				Commands: CommandsConfig{
					Timeout: 30 * time.Second,
				},
				Logging: LoggingConfig{
					Level:      "info",
					File:       "test.log",
					MaxSizeMB:  100,
					MaxBackups: 3,
				},
			}

			err := validate(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errText != "" && err != nil {
				if indexOf(err.Error(), tt.errText) < 0 {
					t.Errorf("validate() error = %v, want error containing %q", err, tt.errText)
				}
			}
		})
	}
}

// TestValidateTaskIntervals tests task interval validation
func TestValidateTaskIntervals(t *testing.T) {
	tests := []struct {
		name              string
		heartbeatInterval time.Duration
		metricsInterval   time.Duration
		wantErr           bool
		errText           string
	}{
		// Valid configurations
		{
			name:              "heartbeat more frequent than metrics",
			heartbeatInterval: 1 * time.Minute,
			metricsInterval:   5 * time.Minute,
			wantErr:           false,
		},
		{
			name:              "equal intervals",
			heartbeatInterval: 5 * time.Minute,
			metricsInterval:   5 * time.Minute,
			wantErr:           false,
		},

		// Invalid configurations
		{
			name:              "heartbeat less frequent than metrics",
			heartbeatInterval: 10 * time.Minute,
			metricsInterval:   5 * time.Minute,
			wantErr:           true,
			errText:           "heartbeat interval",
		},
		{
			name:              "heartbeat too short",
			heartbeatInterval: 5 * time.Second,
			metricsInterval:   5 * time.Minute,
			wantErr:           true,
			errText:           "at least 10 seconds",
		},
		{
			name:              "metrics too short",
			heartbeatInterval: 1 * time.Minute,
			metricsInterval:   10 * time.Second,
			wantErr:           true,
			errText:           "at least 30 seconds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Code:          "test-device",
				SubjectPrefix: "agents",
				NATS: NATSConfig{
					URLs: []string{"nats://localhost:4222"},
					Auth: AuthConfig{Type: "none"},
				},
				Tasks: TasksConfig{
					Heartbeat:     HeartbeatConfig{Enabled: true, Interval: tt.heartbeatInterval},
					SystemMetrics: SystemMetricsConfig{Enabled: true, Interval: tt.metricsInterval},
					ServiceCheck:  ServiceCheckConfig{Enabled: false},
					Inventory:     InventoryConfig{Enabled: true, Interval: 24 * time.Hour},
				},
				Commands: CommandsConfig{
					Timeout: 30 * time.Second,
				},
				Logging: LoggingConfig{
					Level:      "info",
					File:       "test.log",
					MaxSizeMB:  100,
					MaxBackups: 3,
				},
			}

			err := validate(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errText != "" && err != nil {
				if indexOf(err.Error(), tt.errText) < 0 {
					t.Errorf("validate() error = %v, want error containing %q", err, tt.errText)
				}
			}
		})
	}
}

// TestValidateCommandTimeout tests command timeout validation
func TestValidateCommandTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		wantErr bool
		errText string
	}{
		{
			name:    "valid timeout",
			timeout: 30 * time.Second,
			wantErr: false,
		},
		{
			name:    "minimum timeout",
			timeout: 5 * time.Second,
			wantErr: false,
		},
		{
			name:    "maximum timeout",
			timeout: 5 * time.Minute,
			wantErr: false,
		},
		{
			name:    "too short",
			timeout: 1 * time.Second,
			wantErr: true,
			errText: "at least 5 seconds",
		},
		{
			name:    "too long",
			timeout: 10 * time.Minute,
			wantErr: true,
			errText: "must not exceed 5 minutes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Code:          "test-device",
				SubjectPrefix: "agents",
				NATS: NATSConfig{
					URLs: []string{"nats://localhost:4222"},
					Auth: AuthConfig{Type: "none"},
				},
				Tasks: TasksConfig{
					Heartbeat:     HeartbeatConfig{Enabled: true, Interval: 1 * time.Minute},
					SystemMetrics: SystemMetricsConfig{Enabled: true, Interval: 5 * time.Minute},
					ServiceCheck:  ServiceCheckConfig{Enabled: false},
					Inventory:     InventoryConfig{Enabled: true, Interval: 24 * time.Hour},
				},
				Commands: CommandsConfig{
					Timeout: tt.timeout,
				},
				Logging: LoggingConfig{
					Level:      "info",
					File:       "test.log",
					MaxSizeMB:  100,
					MaxBackups: 3,
				},
			}

			err := validate(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errText != "" && err != nil {
				if indexOf(err.Error(), tt.errText) < 0 {
					t.Errorf("validate() error = %v, want error containing %q", err, tt.errText)
				}
			}
		})
	}
}

// TestValidateLocation tests location validation (optional, single NATS token)
func TestValidateLocation(t *testing.T) {
	tests := []struct {
		name     string
		location string
		wantErr  bool
		errText  string
	}{
		{
			name:     "empty location is allowed",
			location: "",
			wantErr:  false,
		},
		{
			name:     "simple location",
			location: "hq",
			wantErr:  false,
		},
		{
			name:     "location with dash and underscore",
			location: "us-east_1",
			wantErr:  false,
		},
		{
			name:     "location with dots",
			location: "hq.floor2",
			wantErr:  true,
			errText:  "location must contain only alphanumeric",
		},
		{
			name:     "location with spaces",
			location: "head quarters",
			wantErr:  true,
			errText:  "location must contain only alphanumeric",
		},
		{
			name:     "location with wildcard",
			location: "*",
			wantErr:  true,
			errText:  "location must contain only alphanumeric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Code:          "test-device",
				Location:      tt.location,
				SubjectPrefix: "agents",
				NATS: NATSConfig{
					URLs: []string{"nats://localhost:4222"},
					Auth: AuthConfig{Type: "none"},
				},
				Tasks: TasksConfig{
					Heartbeat:     HeartbeatConfig{Enabled: true, Interval: 1 * time.Minute},
					SystemMetrics: SystemMetricsConfig{Enabled: true, Interval: 5 * time.Minute},
					ServiceCheck:  ServiceCheckConfig{Enabled: false},
					Inventory:     InventoryConfig{Enabled: true, Interval: 24 * time.Hour},
				},
				Commands: CommandsConfig{
					Timeout: 30 * time.Second,
				},
				Logging: LoggingConfig{
					Level:      "info",
					File:       "test.log",
					MaxSizeMB:  100,
					MaxBackups: 3,
				},
			}

			err := validate(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errText != "" && err != nil {
				if indexOf(err.Error(), tt.errText) < 0 {
					t.Errorf("validate() error = %v, want error containing %q", err, tt.errText)
				}
			}
		})
	}
}

// TestLoadLegacyDeviceID tests that the legacy device_id config key is
// accepted as a fallback for code
func TestLoadLegacyDeviceID(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantCode string
	}{
		{
			name: "device_id falls back to code",
			yaml: `
device_id: "legacy-device"
`,
			wantCode: "legacy-device",
		},
		{
			name: "code takes precedence over device_id",
			yaml: `
code: "new-code"
device_id: "legacy-device"
`,
			wantCode: "new-code",
		},
		{
			name: "code alone",
			yaml: `
code: "new-code"
`,
			wantCode: "new-code",
		},
	}

	base := `
nats:
  urls: ["nats://localhost:4222"]
  auth:
    type: "none"
tasks:
  service_check:
    enabled: false
commands:
  scripts_directory: ""
`

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml+base), 0644); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Code != tt.wantCode {
				t.Errorf("Load() code = %q, want %q", cfg.Code, tt.wantCode)
			}
		})
	}
}

// A config written for exporter mode still loads after its removal. The keys
// are ignored rather than rejected: nothing used the mode, the builtin
// collector publishes the same payload, and refusing to start would take an
// agent offline over a setting whose replacement needs nothing from anyone.
func TestLoadIgnoresRemovedExporterKeys(t *testing.T) {
	yaml := `
code: "server-01"
nats:
  urls: ["nats://localhost:4222"]
  auth:
    type: "none"
tasks:
  system_metrics:
    enabled: true
    interval: "5m"
    source: "exporter"
    exporter_url: "http://localhost:9100/metrics"
  service_check:
    enabled: false
commands:
  scripts_directory: ""
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Tasks.SystemMetrics.Enabled {
		t.Error("system metrics should stay enabled")
	}
}

// TestLoadStoneAgeAuth covers the hyphenated `stone-age` config key end to end —
// through viper and mapstructure, not just validate() — plus the derived session
// file path and the https requirement.
func TestLoadPlatformAuth(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "device.creds")

	// The password satisfies validate() without anything existing on disk yet
	yaml := `
code: "server-01"
platform:
  url: "https://platform.example.com"
  identity: "thing@example.com"
  password_env: "AGENT_PLATFORM_PASSWORD"
nats:
  urls: ["nats://localhost:4222"]
  auth:
    type: "platform"
    creds_file: "` + filepath.ToSlash(credsFile) + `"
tasks:
  service_check:
    enabled: false
commands:
  scripts_directory: ""
`

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	sa := cfg.Platform
	if sa.URL != "https://platform.example.com" {
		t.Errorf("platform.url = %q, want the configured URL", sa.URL)
	}
	if sa.Identity != "thing@example.com" {
		t.Errorf("platform.identity = %q, want thing@example.com", sa.Identity)
	}
	if sa.PasswordEnv != "AGENT_PLATFORM_PASSWORD" {
		t.Errorf("platform.password_env = %q, want AGENT_PLATFORM_PASSWORD", sa.PasswordEnv)
	}

	wantSession := filepath.Join(dir, "platform-session.json")
	if sa.SessionFile != wantSession {
		t.Errorf("session_file = %q, want it derived beside creds_file as %q", sa.SessionFile, wantSession)
	}

	// The credential refresh defaults on, inside the platform's token TTL. A
	// non-zero interval IS the switch: there is no separate enabled flag,
	// because an agent that gets its credential from the platform always wants
	// it refreshed.
	if cfg.Platform.SyncInterval != 24*time.Hour {
		t.Errorf("platform.sync_interval = %v, want 24h", cfg.Platform.SyncInterval)
	}
}

func TestValidatePlatformAuth(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "device.creds")
	sessionFile := filepath.Join(dir, "platform-session.json")

	valid := func() *Config {
		return &Config{
			Code:          "server-01",
			SubjectPrefix: "agents",
			Platform: PlatformConfig{
				URL:          "https://platform.example.com",
				Identity:     "thing@example.com",
				PasswordEnv:  "AGENT_PLATFORM_PASSWORD",
				SessionFile:  sessionFile,
				SyncInterval: 24 * time.Hour,
			},
			NATS: NATSConfig{
				URLs: []string{"nats://localhost:4222"},
				Auth: AuthConfig{
					Type:      "platform",
					CredsFile: credsFile,
				},
			},
			Tasks: TasksConfig{
				Heartbeat:     HeartbeatConfig{Enabled: true, Interval: time.Minute},
				SystemMetrics: SystemMetricsConfig{Enabled: true, Interval: 5 * time.Minute},
			},
			Commands: CommandsConfig{Timeout: 30 * time.Second},
			Logging:  LoggingConfig{Level: "info", MaxSizeMB: 100, MaxBackups: 3},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:   "valid",
			mutate: func(*Config) {},
		},
		{
			name:    "http url rejected",
			mutate:  func(c *Config) { c.Platform.URL = "http://platform.example.com" },
			wantErr: "must be https",
		},
		{
			name: "http url allowed when opted in",
			mutate: func(c *Config) {
				c.Platform.URL = "http://platform.example.com"
				c.Platform.AllowInsecureURL = true
			},
		},
		{
			name:    "missing url",
			mutate:  func(c *Config) { c.Platform.URL = "" },
			wantErr: "platform.url is required",
		},
		{
			name:    "missing identity",
			mutate:  func(c *Config) { c.Platform.Identity = "" },
			wantErr: "platform.identity is required",
		},
		{
			name:    "missing creds_file",
			mutate:  func(c *Config) { c.NATS.Auth.CredsFile = "" },
			wantErr: "creds_file is required",
		},
		{
			// Nothing on disk to authenticate with, so the password is mandatory
			name:    "no password and nothing bootstrapped",
			mutate:  func(c *Config) { c.Platform.PasswordEnv = "" },
			wantErr: "password_env is required",
		},
		{
			name:    "platform sync_interval too short",
			mutate:  func(c *Config) { c.Platform.SyncInterval = 30 * time.Minute },
			wantErr: "at least 1 hour",
		},
		{
			name:    "platform sync_interval beyond token ttl",
			mutate:  func(c *Config) { c.Platform.SyncInterval = 96 * time.Hour },
			wantErr: "must not exceed 72 hours",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			tt.mutate(cfg)

			err := validate(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || indexOf(err.Error(), tt.wantErr) < 0 {
				t.Fatalf("validate() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestValidateStoneAgeAuthPasswordOptionalOnceBootstrapped pins the rule that
// lets a deployment drop the thing's password after the first boot.
func TestValidatePlatformAuthPasswordOptionalOnceBootstrapped(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "device.creds")
	if err := os.WriteFile(credsFile, []byte("creds"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Code:          "server-01",
		SubjectPrefix: "agents",
		Platform: PlatformConfig{
			URL:          "https://platform.example.com",
			Identity:     "thing@example.com",
			SessionFile:  filepath.Join(dir, "platform-session.json"),
			SyncInterval: 24 * time.Hour,
		},
		NATS: NATSConfig{
			URLs: []string{"nats://localhost:4222"},
			Auth: AuthConfig{
				Type:      "platform",
				CredsFile: credsFile,
			},
		},
		Tasks: TasksConfig{
			Heartbeat:     HeartbeatConfig{Enabled: true, Interval: time.Minute},
			SystemMetrics: SystemMetricsConfig{Enabled: true, Interval: 5 * time.Minute},
		},
		Commands: CommandsConfig{Timeout: 30 * time.Second},
		Logging:  LoggingConfig{Level: "info", MaxSizeMB: 100, MaxBackups: 3},
	}

	if err := validate(cfg); err != nil {
		t.Fatalf("validate() error = %v, want nil (credentials exist, so no password is needed)", err)
	}
}

// Helper function
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestValidateNebula(t *testing.T) {
	tests := []struct {
		name     string
		nebula   NebulaConfig
		authType string
		wantErr  bool
		errText  string
	}{
		// The feature is off by default, and nothing about it is checked when it
		// is off. An agent that never touches the nebula block cannot be refused
		// on account of one.
		{
			name:     "disabled ignores everything else",
			nebula:   NebulaConfig{Enabled: false, Source: "nonsense"},
			authType: "creds",
			wantErr:  false,
		},
		{
			name: "platform source with stone-age auth",
			nebula: NebulaConfig{
				Enabled:       true,
				Source:        "platform",
				CacheFile:     "/var/lib/agent/nebula-cache.yaml",
				SyncInterval:  10 * time.Minute,
				VerifyTimeout: 30 * time.Second,
			},
			authType: "platform",
			wantErr:  false,
		},
		{
			name: "file source needs no platform",
			nebula: NebulaConfig{
				Enabled:       true,
				Source:        "file",
				ConfigFile:    "/etc/agent/nebula.yaml",
				VerifyTimeout: 30 * time.Second,
			},
			authType: "creds",
			wantErr:  false,
		},

		{
			name: "platform source without stone-age auth",
			nebula: NebulaConfig{
				Enabled:       true,
				Source:        "platform",
				CacheFile:     "/var/lib/agent/nebula-cache.yaml",
				SyncInterval:  10 * time.Minute,
				VerifyTimeout: 30 * time.Second,
			},
			authType: "creds",
			wantErr:  true,
			errText:  "requires stone-age auth",
		},
		{
			name: "file source without a path",
			nebula: NebulaConfig{
				Enabled:       true,
				Source:        "file",
				VerifyTimeout: 30 * time.Second,
			},
			authType: "creds",
			wantErr:  true,
			errText:  "nebula.config_file is required",
		},
		{
			name: "unknown source",
			nebula: NebulaConfig{
				Enabled:       true,
				Source:        "http",
				VerifyTimeout: 30 * time.Second,
			},
			authType: "platform",
			wantErr:  true,
			errText:  "must be \"platform\" or \"file\"",
		},

		// The sync interval is the revocation latency for this device, which is
		// why it has an upper bound at all — most intervals in this config only
		// have a lower one.
		{
			name: "sync interval too short",
			nebula: NebulaConfig{
				Enabled:       true,
				Source:        "platform",
				CacheFile:     "/var/lib/agent/nebula-cache.yaml",
				SyncInterval:  30 * time.Second,
				VerifyTimeout: 30 * time.Second,
			},
			authType: "platform",
			wantErr:  true,
			errText:  "at least 1 minute",
		},
		{
			name: "sync interval too long",
			nebula: NebulaConfig{
				Enabled:       true,
				Source:        "platform",
				CacheFile:     "/var/lib/agent/nebula-cache.yaml",
				SyncInterval:  6 * time.Hour,
				VerifyTimeout: 30 * time.Second,
			},
			authType: "platform",
			wantErr:  true,
			errText:  "must not exceed 1 hour",
		},
		{
			name: "verify timeout too short",
			nebula: NebulaConfig{
				Enabled:       true,
				Source:        "file",
				ConfigFile:    "/etc/agent/nebula.yaml",
				VerifyTimeout: time.Second,
			},
			authType: "creds",
			wantErr:  true,
			errText:  "at least 5 seconds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Nebula: tt.nebula}
			cfg.NATS.Auth.Type = tt.authType

			err := validateNebula(cfg)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateNebula() returned no error, want one containing %q", tt.errText)
				}
				if !strings.Contains(err.Error(), tt.errText) {
					t.Errorf("validateNebula() error = %q, want it to contain %q", err.Error(), tt.errText)
				}
				return
			}

			if err != nil {
				t.Errorf("validateNebula() error = %v, want nil", err)
			}
		})
	}
}
