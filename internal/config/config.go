package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config represents the complete agent configuration
type Config struct {
	DeviceID      string         `mapstructure:"device_id"`
	SubjectPrefix string         `mapstructure:"subject_prefix"`
	NATS          NATSConfig     `mapstructure:"nats"`
	Tasks         TasksConfig    `mapstructure:"tasks"`
	Commands      CommandsConfig `mapstructure:"commands"`
	Logging       LoggingConfig  `mapstructure:"logging"`
}

// NATSConfig holds NATS connection settings
type NATSConfig struct {
	URLs          []string      `mapstructure:"urls"`
	Auth          AuthConfig    `mapstructure:"auth"`
	TLS           TLSConfig     `mapstructure:"tls"`
	MaxReconnects int           `mapstructure:"max_reconnects"`
	ReconnectWait time.Duration `mapstructure:"reconnect_wait"`
	DrainTimeout  time.Duration `mapstructure:"drain_timeout"`
}

// AuthConfig holds NATS authentication credentials
type AuthConfig struct {
	Type       string         `mapstructure:"type"`       // creds, token, userpass, pocketbase, none
	CredsFile  string         `mapstructure:"creds_file"` // for creds and pocketbase auth
	Token      string         `mapstructure:"token"`      // for token auth
	Username   string         `mapstructure:"username"`   // for userpass auth
	Password   string         `mapstructure:"password"`   // for userpass auth
	PocketBase PocketBaseAuth `mapstructure:"pocketbase"` // for pocketbase bootstrap
}

// PocketBaseAuth holds PocketBase bootstrap configuration for fetching NATS credentials
type PocketBaseAuth struct {
	URL            string `mapstructure:"url"`             // PocketBase server URL
	AuthCollection string `mapstructure:"auth_collection"` // Auth collection (default: _superusers)
	Identity       string `mapstructure:"identity"`        // Login identity (email/username)
	PasswordEnv    string `mapstructure:"password_env"`    // Env var containing password
	Collection     string `mapstructure:"collection"`      // Collection holding creds records
	DeviceIDField  string `mapstructure:"device_id_field"` // Field name for device ID lookup (default: device_id)
	CredsField     string `mapstructure:"creds_field"`     // Field name containing creds content (default: nats_creds)
}

// TLSConfig holds TLS connection settings
type TLSConfig struct {
	Enabled            bool   `mapstructure:"enabled"`
	CertFile           string `mapstructure:"cert_file"`            // Client certificate
	KeyFile            string `mapstructure:"key_file"`             // Client private key
	CAFile             string `mapstructure:"ca_file"`              // CA certificate for server verification
	InsecureSkipVerify bool   `mapstructure:"insecure_skip_verify"` // Skip server certificate verification (NOT recommended for production)
}

// TasksConfig holds scheduled task configurations
type TasksConfig struct {
	Heartbeat     HeartbeatConfig     `mapstructure:"heartbeat"`
	SystemMetrics SystemMetricsConfig `mapstructure:"system_metrics"`
	ServiceCheck  ServiceCheckConfig  `mapstructure:"service_check"`
	Inventory     InventoryConfig     `mapstructure:"inventory"`
}

// HeartbeatConfig configures the heartbeat task
type HeartbeatConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Interval time.Duration `mapstructure:"interval"`
}

// SystemMetricsConfig configures metrics collection
type SystemMetricsConfig struct {
	Enabled     bool          `mapstructure:"enabled"`
	Interval    time.Duration `mapstructure:"interval"`
	Source      string        `mapstructure:"source"`       // "builtin" (default) or "exporter"
	ExporterURL string        `mapstructure:"exporter_url"` // Only used when Source="exporter"
}

// ServiceCheckConfig configures service status monitoring
type ServiceCheckConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Interval time.Duration `mapstructure:"interval"`
	Services []string      `mapstructure:"services"`
}

// InventoryConfig configures system inventory reporting
type InventoryConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Interval time.Duration `mapstructure:"interval"`
}

// CommandsConfig holds command execution settings
type CommandsConfig struct {
	ScriptsDirectory string        `mapstructure:"scripts_directory"` // Directory containing allowed PowerShell scripts
	AllowedServices  []string      `mapstructure:"allowed_services"`
	AllowedCommands  []string      `mapstructure:"allowed_commands"`
	AllowedLogPaths  []string      `mapstructure:"allowed_log_paths"`
	Timeout          time.Duration `mapstructure:"timeout"` // Command execution timeout
}

// LoggingConfig holds logging settings
type LoggingConfig struct {
	Level      string `mapstructure:"level"`
	File       string `mapstructure:"file"`
	MaxSizeMB  int    `mapstructure:"max_size_mb"`
	MaxBackups int    `mapstructure:"max_backups"`
}

// Load reads and parses the configuration file
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// Set config file path
	v.SetConfigFile(configPath)

	// Set defaults
	setDefaults(v)

	// Read config file
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	// Unmarshal into struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate configuration
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// setDefaults sets sensible default values
func setDefaults(v *viper.Viper) {
	// Get platform-specific defaults
	defaults := GetPlatformDefaults()

	// Subject prefix default
	v.SetDefault("subject_prefix", "agents")

	// NATS defaults
	v.SetDefault("nats.max_reconnects", -1) // infinite
	v.SetDefault("nats.reconnect_wait", "2s")
	v.SetDefault("nats.drain_timeout", "30s")

	// PocketBase bootstrap defaults
	v.SetDefault("nats.auth.pocketbase.auth_collection", "_superusers")
	v.SetDefault("nats.auth.pocketbase.device_id_field", "device_id")
	v.SetDefault("nats.auth.pocketbase.creds_field", "nats_creds")

	// TLS defaults
	v.SetDefault("nats.tls.enabled", false)
	v.SetDefault("nats.tls.insecure_skip_verify", false)

	// Task defaults with platform-specific exporter URL
	v.SetDefault("tasks.heartbeat.enabled", true)
	v.SetDefault("tasks.heartbeat.interval", "1m")
	v.SetDefault("tasks.system_metrics.enabled", true)
	v.SetDefault("tasks.system_metrics.interval", "5m")
	v.SetDefault("tasks.system_metrics.source", "builtin") // Default to builtin (gopsutil)
	v.SetDefault("tasks.system_metrics.exporter_url", defaults.ExporterURL)
	v.SetDefault("tasks.service_check.enabled", true)
	v.SetDefault("tasks.service_check.interval", "1m")
	v.SetDefault("tasks.inventory.enabled", true)
	v.SetDefault("tasks.inventory.interval", "24h")

	// Command defaults with platform-specific scripts directory
	v.SetDefault("commands.timeout", "30s")
	v.SetDefault("commands.scripts_directory", defaults.ScriptsDirectory)

	// Logging defaults with platform-specific log file path
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.file", defaults.LogFile)
	v.SetDefault("logging.max_size_mb", 100)
	v.SetDefault("logging.max_backups", 3)
}

// validate checks that required fields are present and valid
func validate(cfg *Config) error {
	// Validate device_id is present
	if cfg.DeviceID == "" {
		return fmt.Errorf("device_id is required")
	}

	// Validate device_id format (alphanumeric, dash, underscore only)
	// This ensures compatibility with NATS subject names
	validDeviceID := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !validDeviceID.MatchString(cfg.DeviceID) {
		return fmt.Errorf("device_id must contain only alphanumeric characters, dashes, and underscores (got: %s)", cfg.DeviceID)
	}

	// Validate subject_prefix format
	// Allows hierarchical prefixes like "region.dev.agents" or simple prefixes like "agents"
	if cfg.SubjectPrefix == "" {
		return fmt.Errorf("subject_prefix is required (should default to 'agents')")
	}
	if len(cfg.SubjectPrefix) > 50 {
		return fmt.Errorf("subject_prefix must not exceed 50 characters (got: %d)", len(cfg.SubjectPrefix))
	}
	if err := validateSubjectPrefix(cfg.SubjectPrefix); err != nil {
		return fmt.Errorf("invalid subject_prefix: %w", err)
	}

	// Validate NATS URLs
	if len(cfg.NATS.URLs) == 0 {
		return fmt.Errorf("at least one NATS URL is required")
	}

	// Validate NATS auth
	switch cfg.NATS.Auth.Type {
	case "creds":
		if cfg.NATS.Auth.CredsFile == "" {
			return fmt.Errorf("creds_file is required for creds auth type")
		}
		// Verify credentials file exists
		if _, err := os.Stat(cfg.NATS.Auth.CredsFile); err != nil {
			return fmt.Errorf("credentials file not found: %s (%w)", cfg.NATS.Auth.CredsFile, err)
		}
	case "pocketbase":
		if cfg.NATS.Auth.CredsFile == "" {
			return fmt.Errorf("creds_file is required for pocketbase auth type (path where .creds will be written)")
		}
		pb := cfg.NATS.Auth.PocketBase
		if pb.URL == "" {
			return fmt.Errorf("pocketbase.url is required for pocketbase auth type")
		}
		if pb.Identity == "" {
			return fmt.Errorf("pocketbase.identity is required for pocketbase auth type")
		}
		if pb.PasswordEnv == "" {
			return fmt.Errorf("pocketbase.password_env is required for pocketbase auth type")
		}
		if pb.Collection == "" {
			return fmt.Errorf("pocketbase.collection is required for pocketbase auth type")
		}
		// .creds file may not exist yet — bootstrap will create it
	case "token":
		if cfg.NATS.Auth.Token == "" {
			return fmt.Errorf("token is required for token auth type")
		}
	case "userpass":
		if cfg.NATS.Auth.Username == "" || cfg.NATS.Auth.Password == "" {
			return fmt.Errorf("username and password are required for userpass auth type")
		}
	case "none":
		// No validation needed
	default:
		return fmt.Errorf("invalid auth type: %s (must be creds, token, userpass, pocketbase, or none)", cfg.NATS.Auth.Type)
	}

	// Validate TLS configuration
	if cfg.NATS.TLS.Enabled {
		// If client certificate is provided, key must also be provided
		if cfg.NATS.TLS.CertFile != "" && cfg.NATS.TLS.KeyFile == "" {
			return fmt.Errorf("tls.key_file is required when tls.cert_file is specified")
		}
		if cfg.NATS.TLS.KeyFile != "" && cfg.NATS.TLS.CertFile == "" {
			return fmt.Errorf("tls.cert_file is required when tls.key_file is specified")
		}

		// Verify TLS files exist if specified
		if cfg.NATS.TLS.CertFile != "" {
			if _, err := os.Stat(cfg.NATS.TLS.CertFile); err != nil {
				return fmt.Errorf("TLS certificate file not found: %s (%w)", cfg.NATS.TLS.CertFile, err)
			}
		}
		if cfg.NATS.TLS.KeyFile != "" {
			if _, err := os.Stat(cfg.NATS.TLS.KeyFile); err != nil {
				return fmt.Errorf("TLS key file not found: %s (%w)", cfg.NATS.TLS.KeyFile, err)
			}
		}
		if cfg.NATS.TLS.CAFile != "" {
			if _, err := os.Stat(cfg.NATS.TLS.CAFile); err != nil {
				return fmt.Errorf("TLS CA file not found: %s (%w)", cfg.NATS.TLS.CAFile, err)
			}
		}

		// Warn about insecure_skip_verify (but allow it for development)
		if cfg.NATS.TLS.InsecureSkipVerify {
			// This will be logged as a warning in the agent startup
			// We don't block it here to allow development/testing scenarios
		}
	}

	// Validate scripts directory if specified
	if cfg.Commands.ScriptsDirectory != "" {
		// Verify directory exists
		info, err := os.Stat(cfg.Commands.ScriptsDirectory)
		if err != nil {
			return fmt.Errorf("scripts directory not found: %s (%w)", cfg.Commands.ScriptsDirectory, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("scripts_directory must be a directory, not a file: %s", cfg.Commands.ScriptsDirectory)
		}
	}

	// Validate service check has services if enabled
	if cfg.Tasks.ServiceCheck.Enabled && len(cfg.Tasks.ServiceCheck.Services) == 0 {
		return fmt.Errorf("at least one service must be specified when service_check is enabled")
	}

	// Validate task intervals are sensible
	if cfg.Tasks.Heartbeat.Enabled && cfg.Tasks.Heartbeat.Interval < 10*time.Second {
		return fmt.Errorf("heartbeat interval must be at least 10 seconds (got: %v)", cfg.Tasks.Heartbeat.Interval)
	}

	if cfg.Tasks.SystemMetrics.Enabled && cfg.Tasks.SystemMetrics.Interval < 30*time.Second {
		return fmt.Errorf("system_metrics interval must be at least 30 seconds (got: %v)", cfg.Tasks.SystemMetrics.Interval)
	}

	// Validate metrics source
	if cfg.Tasks.SystemMetrics.Enabled {
		source := strings.ToLower(cfg.Tasks.SystemMetrics.Source)
		if source == "" {
			source = "builtin" // Default
		}
		if source != "builtin" && source != "exporter" {
			return fmt.Errorf("invalid system_metrics.source: %s (must be 'builtin' or 'exporter')", cfg.Tasks.SystemMetrics.Source)
		}
		// If exporter mode, URL is required
		if source == "exporter" && cfg.Tasks.SystemMetrics.ExporterURL == "" {
			return fmt.Errorf("exporter_url is required when system_metrics.source is 'exporter'")
		}
	}

	// Validate heartbeat is more frequent than metrics (best practice)
	// Heartbeat should be MORE frequent, meaning a SMALLER interval duration
	if cfg.Tasks.Heartbeat.Enabled && cfg.Tasks.SystemMetrics.Enabled {
		if cfg.Tasks.Heartbeat.Interval > cfg.Tasks.SystemMetrics.Interval {
			return fmt.Errorf("heartbeat interval (%v) should be less than or equal to metrics interval (%v) - heartbeat should be more frequent",
				cfg.Tasks.Heartbeat.Interval, cfg.Tasks.SystemMetrics.Interval)
		}
	}

	// Validate command timeout
	if cfg.Commands.Timeout < 5*time.Second {
		return fmt.Errorf("command timeout must be at least 5 seconds (got: %v)", cfg.Commands.Timeout)
	}
	if cfg.Commands.Timeout > 5*time.Minute {
		return fmt.Errorf("command timeout must not exceed 5 minutes (got: %v)", cfg.Commands.Timeout)
	}

	// Validate log level
	validLevels := map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
	}
	if !validLevels[cfg.Logging.Level] {
		return fmt.Errorf("invalid log level: %s (must be debug, info, warn, or error)", cfg.Logging.Level)
	}

	// Validate log rotation settings
	if cfg.Logging.MaxSizeMB < 1 || cfg.Logging.MaxSizeMB > 1000 {
		return fmt.Errorf("log max_size_mb must be between 1 and 1000 (got: %d)", cfg.Logging.MaxSizeMB)
	}
	if cfg.Logging.MaxBackups < 0 || cfg.Logging.MaxBackups > 100 {
		return fmt.Errorf("log max_backups must be between 0 and 100 (got: %d)", cfg.Logging.MaxBackups)
	}

	return nil
}

// validateSubjectPrefix validates a NATS subject prefix
// Allows hierarchical prefixes like "region.dev.agents" where each token
// contains only alphanumeric characters, dashes, and underscores
func validateSubjectPrefix(prefix string) error {
	// Check for leading or trailing dots
	if len(prefix) > 0 && (prefix[0] == '.' || prefix[len(prefix)-1] == '.') {
		return fmt.Errorf("cannot start or end with a dot (got: %s)", prefix)
	}

	// Split into tokens by dots
	tokens := regexp.MustCompile(`\.`).Split(prefix, -1)
	
	// Validate each token
	validToken := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	for i, token := range tokens {
		if token == "" {
			return fmt.Errorf("empty token at position %d (consecutive dots not allowed)", i)
		}
		if !validToken.MatchString(token) {
			return fmt.Errorf("token '%s' contains invalid characters (only alphanumeric, dash, and underscore allowed)", token)
		}
	}

	return nil
}
