package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config represents the complete agent configuration
type Config struct {
	Code          string         `mapstructure:"code"`     // Agent identity token used in NATS subjects (was: device_id)
	Location      string         `mapstructure:"location"` // Optional deployment location, carried in telemetry payloads
	SubjectPrefix string         `mapstructure:"subject_prefix"`
	NATS          NATSConfig     `mapstructure:"nats"`
	Nebula        NebulaConfig   `mapstructure:"nebula"`
	Tasks         TasksConfig    `mapstructure:"tasks"`
	Commands      CommandsConfig `mapstructure:"commands"`
	Logging       LoggingConfig  `mapstructure:"logging"`
}

// NebulaConfig configures the embedded Nebula overlay host. Disabled by default:
// an agent that leaves this alone behaves exactly as it did before the feature
// existed.
//
// See docs/nebula-design.md for why the agent runs Nebula in-process rather than
// supervising a separate service, and why there is only one mode.
type NebulaConfig struct {
	Enabled bool `mapstructure:"enabled"`

	// Source is where the config comes from: "platform" (the stone-age.io
	// nebula_hosts record for this agent's thing) or "file".
	//
	// These are not peers. "platform" polls for changes, rolls back a config that
	// cannot reach the mesh, and reports the revision it adopted. "file" reads a
	// path and does none of that — it exists so the feature works without the
	// platform, and so a device can join the mesh before it has a thing record.
	Source string `mapstructure:"source"`

	// ConfigFile is the Nebula config to load when Source is "file".
	ConfigFile string `mapstructure:"config_file"`

	// CacheFile holds the last config known to have reached the mesh, so a device
	// that reboots while the platform is unreachable still comes up on the
	// overlay. Contains the host private key: written 0600. Unused by "file".
	CacheFile string `mapstructure:"cache_file"`

	// SyncInterval is how often to ask the platform whether the config changed.
	//
	// THIS IS A SECURITY NUMBER, NOT A TUNING KNOB. Nebula has no CRL: revoking a
	// certificate means adding its fingerprint to pki.blocklist in every other
	// host's config, and a peer only learns about it when it re-reads that config.
	// The sync interval is therefore the revocation latency for this device.
	SyncInterval time.Duration `mapstructure:"sync_interval"`

	// VerifyTimeout is how long a newly applied config has to reach a lighthouse
	// before it is treated as broken and rolled back.
	VerifyTimeout time.Duration `mapstructure:"verify_timeout"`
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
	Type      string       `mapstructure:"type"`       // creds, token, userpass, stone-age, none
	CredsFile string       `mapstructure:"creds_file"` // for creds and stone-age auth
	Token     string       `mapstructure:"token"`      // for token auth
	Username  string       `mapstructure:"username"`   // for userpass auth
	Password  string       `mapstructure:"password"`   // for userpass auth
	StoneAge  StoneAgeAuth `mapstructure:"stone-age"`  // for stone-age.io platform credentials
}

// StoneAgeAuth configures credential management against the stone-age.io
// platform. The agent is a Thing on the platform: it authenticates as itself
// against the `things` auth collection and its NATS credential lives on the
// related nats_user record.
//
// The name is the platform, not the database behind it: the agent depends on the
// stone-age.io schema (things → nats_user → creds_file) and on a route the
// platform defines itself (POST /api/me/nats-creds/rotate), neither of which
// comes from PocketBase.
type StoneAgeAuth struct {
	URL      string `mapstructure:"url"`      // Platform base URL (https:// unless allow_insecure_url)
	Identity string `mapstructure:"identity"` // The thing's login email

	// PasswordEnv names the env var holding the thing's password. Required until
	// the agent has bootstrapped; after that it holds a session token instead and
	// the password can be removed from the service environment. Without it, a
	// device whose token has lapsed needs manual re-provisioning.
	PasswordEnv string `mapstructure:"password_env"`

	// SessionFile stores the platform session token and the revision of the
	// credential already on disk. Defaults to platform-session.json beside
	// creds_file.
	SessionFile string `mapstructure:"session_file"`

	// AllowInsecureURL permits a plain http:// platform URL. Development only:
	// bootstrap sends the thing's password and receives an nkey seed, so on the
	// wire in cleartext both are readable.
	AllowInsecureURL bool `mapstructure:"allow_insecure_url"`
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
	CredsSync     CredsSyncConfig     `mapstructure:"creds_sync"`
}

// CredsSyncConfig configures periodic credential upkeep against the platform.
// Only used with stone-age auth; the scheduler skips the task otherwise.
type CredsSyncConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Interval time.Duration `mapstructure:"interval"`
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

	// Accept the legacy device_id key as a fallback for code
	if !v.IsSet("code") && v.IsSet("device_id") {
		v.Set("code", v.GetString("device_id"))
	}

	// Unmarshal into struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Fill in defaults that can only be derived from other config values
	applyDerivedDefaults(&cfg)

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
	v.SetDefault("tasks.creds_sync.enabled", true)
	v.SetDefault("tasks.creds_sync.interval", "24h")

	// Nebula defaults. Off unless asked for; the paths sit beside the other
	// platform-managed secrets.
	v.SetDefault("nebula.enabled", false)
	v.SetDefault("nebula.source", "platform")
	v.SetDefault("nebula.cache_file", defaults.NebulaCacheFile)
	v.SetDefault("nebula.sync_interval", "10m")
	v.SetDefault("nebula.verify_timeout", "30s")

	// Command defaults with platform-specific scripts directory
	v.SetDefault("commands.timeout", "30s")
	v.SetDefault("commands.scripts_directory", defaults.ScriptsDirectory)

	// Logging defaults with platform-specific log file path
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.file", defaults.LogFile)
	v.SetDefault("logging.max_size_mb", 100)
	v.SetDefault("logging.max_backups", 3)
}

// applyDerivedDefaults fills in values that can only be computed from other
// config values, so validate() and the rest of the agent see one settled shape.
func applyDerivedDefaults(cfg *Config) {
	// The platform session lives next to the credential it belongs to
	if cfg.NATS.Auth.Type == "stone-age" && cfg.NATS.Auth.StoneAge.SessionFile == "" && cfg.NATS.Auth.CredsFile != "" {
		cfg.NATS.Auth.StoneAge.SessionFile = filepath.Join(
			filepath.Dir(cfg.NATS.Auth.CredsFile), "platform-session.json")
	}
}

// validate checks that required fields are present and valid
func validate(cfg *Config) error {
	// Validate code is present
	if cfg.Code == "" {
		return fmt.Errorf("code is required")
	}

	// Validate code format (alphanumeric, dash, underscore only)
	// This ensures compatibility with NATS subject names
	validToken := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !validToken.MatchString(cfg.Code) {
		return fmt.Errorf("code must contain only alphanumeric characters, dashes, and underscores (got: %s)", cfg.Code)
	}

	// Validate location format if set. Location is optional and only carried in
	// telemetry payloads today, but it is validated as a single NATS subject
	// token so it could be promoted into subjects without a breaking change.
	if cfg.Location != "" && !validToken.MatchString(cfg.Location) {
		return fmt.Errorf("location must contain only alphanumeric characters, dashes, and underscores (got: %s)", cfg.Location)
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
	case "stone-age":
		if cfg.NATS.Auth.CredsFile == "" {
			return fmt.Errorf("creds_file is required for stone-age auth type (path where .creds will be written)")
		}
		sa := cfg.NATS.Auth.StoneAge
		if sa.URL == "" {
			return fmt.Errorf("stone-age.url is required for stone-age auth type")
		}
		if sa.Identity == "" {
			return fmt.Errorf("stone-age.identity is required for stone-age auth type")
		}
		// The bootstrap POSTs the thing's password and receives an nkey seed, so
		// plain http is a development-only choice and has to be asked for
		if !sa.AllowInsecureURL && !strings.HasPrefix(strings.ToLower(sa.URL), "https://") {
			return fmt.Errorf("stone-age.url must be https:// (got: %s) - set stone-age.allow_insecure_url: true to override for development", sa.URL)
		}
		// The password is only needed while the agent has nothing to work from.
		// Once it holds a credential or a session token it can authenticate
		// without one, so requiring it forever would force every deployment to
		// keep the stronger secret on the device.
		if sa.PasswordEnv == "" {
			_, credsErr := os.Stat(cfg.NATS.Auth.CredsFile)
			_, sessionErr := os.Stat(sa.SessionFile)
			if credsErr != nil && sessionErr != nil {
				return fmt.Errorf("stone-age.password_env is required until the agent has bootstrapped (no credentials at %s and no platform session at %s)",
					cfg.NATS.Auth.CredsFile, sa.SessionFile)
			}
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
		return fmt.Errorf("invalid auth type: %s (must be creds, token, userpass, stone-age, or none)", cfg.NATS.Auth.Type)
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

		// Note: InsecureSkipVerify is allowed for development/testing.
		// A warning is logged during NATS connection setup in nats/client.go.
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

	// Validate credential sync interval. Only meaningful for stone-age auth —
	// the scheduler skips the task entirely for every other auth type.
	if cfg.NATS.Auth.Type == "stone-age" && cfg.Tasks.CredsSync.Enabled {
		if cfg.Tasks.CredsSync.Interval < time.Hour {
			return fmt.Errorf("creds_sync interval must be at least 1 hour (got: %v)", cfg.Tasks.CredsSync.Interval)
		}
		// Each sync renews the platform session token, whose TTL is set by the
		// platform (7 days on the things collection today). Syncing has to stay
		// well inside that window or a couple of missed runs cost the agent its
		// password-free path.
		if cfg.Tasks.CredsSync.Interval > 72*time.Hour {
			return fmt.Errorf("creds_sync interval must not exceed 72 hours (got: %v) - it renews the platform session token before it expires", cfg.Tasks.CredsSync.Interval)
		}
	}

	if err := validateNebula(cfg); err != nil {
		return err
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

// validateNebula checks the embedded overlay's settings. All of it is skipped
// when the feature is off, so an agent that never touches the nebula block can
// never be refused on account of it.
func validateNebula(cfg *Config) error {
	if !cfg.Nebula.Enabled {
		return nil
	}

	switch cfg.Nebula.Source {
	case "platform":
		// The platform connection details — URL, identity, session file — live
		// under nats.auth.stone-age, because that is where they were first needed.
		// Reading a Nebula config from the platform means authenticating as the
		// same thing, so it needs the same block. Saying so here beats failing at
		// the first sync with an empty URL.
		if cfg.NATS.Auth.Type != "stone-age" {
			return fmt.Errorf("nebula.source is \"platform\" but nats.auth.type is %q: reading a Nebula config from the platform authenticates as the same thing as the NATS credential, so it requires stone-age auth (use nebula.source: \"file\" otherwise)", cfg.NATS.Auth.Type)
		}
		if cfg.Nebula.CacheFile == "" {
			return fmt.Errorf("nebula.cache_file must not be empty")
		}
	case "file":
		if cfg.Nebula.ConfigFile == "" {
			return fmt.Errorf("nebula.config_file is required when nebula.source is \"file\"")
		}
	default:
		return fmt.Errorf("nebula.source must be \"platform\" or \"file\" (got: %q)", cfg.Nebula.Source)
	}

	// The sync interval is the revocation latency for this device: a blocklisted
	// certificate is only refused once the peer re-reads its config. An hour is
	// already a long time to keep honouring a revoked certificate, and anything
	// below a minute is hammering the platform for a file that rarely changes.
	if cfg.Nebula.Source == "platform" {
		if cfg.Nebula.SyncInterval < time.Minute {
			return fmt.Errorf("nebula.sync_interval must be at least 1 minute (got: %v)", cfg.Nebula.SyncInterval)
		}
		if cfg.Nebula.SyncInterval > time.Hour {
			return fmt.Errorf("nebula.sync_interval must not exceed 1 hour (got: %v) - it is how long this device keeps honouring a revoked certificate, because Nebula revokes through each peer's pki.blocklist and has no CRL", cfg.Nebula.SyncInterval)
		}
	}

	if cfg.Nebula.VerifyTimeout < 5*time.Second {
		return fmt.Errorf("nebula.verify_timeout must be at least 5 seconds (got: %v)", cfg.Nebula.VerifyTimeout)
	}
	if cfg.Nebula.VerifyTimeout > 5*time.Minute {
		return fmt.Errorf("nebula.verify_timeout must not exceed 5 minutes (got: %v)", cfg.Nebula.VerifyTimeout)
	}

	return nil
}
