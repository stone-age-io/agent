package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestValidateDeviceID tests device ID validation
func TestValidateDeviceID(t *testing.T) {
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
			errText:  "device_id is required",
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
				DeviceID:      tt.deviceID,
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
				DeviceID:      "test-device",
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
				DeviceID:      "test-device",
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
				DeviceID:      "test-device",
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
				DeviceID:      "test-device",
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
				DeviceID:      "test-device",
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

// Helper function
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
