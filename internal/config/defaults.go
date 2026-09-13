package config

import (
	"runtime"
)

// PlatformDefaults returns platform-specific default values
type PlatformDefaults struct {
	LogFile          string
	ScriptsDirectory string
	ConfigPath       string
	ExporterURL      string

	// NebulaCacheFile holds the last Nebula config known to have reached the
	// mesh. It embeds the host private key, so it lives beside the agent's other
	// state rather than anywhere world-readable.
	NebulaCacheFile string
}

// GetPlatformDefaults returns platform-specific defaults based on runtime.GOOS
func GetPlatformDefaults() PlatformDefaults {
	switch runtime.GOOS {
	case "windows":
		return PlatformDefaults{
			LogFile:          `C:\ProgramData\Agent\agent.log`,
			ScriptsDirectory: `C:\ProgramData\Agent\Scripts`,
			ConfigPath:       `C:\ProgramData\Agent\config.yaml`,
			ExporterURL:      "http://localhost:9182/metrics", // windows_exporter
			NebulaCacheFile:  `C:\ProgramData\Agent\nebula-cache.yaml`,
		}
	case "linux":
		return PlatformDefaults{
			LogFile:          "/var/log/agent/agent.log",
			ScriptsDirectory: "/opt/agent/scripts",
			ConfigPath:       "/etc/agent/config.yaml",
			ExporterURL:      "http://localhost:9100/metrics", // node_exporter
			NebulaCacheFile:  "/var/lib/agent/nebula-cache.yaml",
		}
	case "freebsd":
		return PlatformDefaults{
			LogFile:          "/var/log/agent/agent.log",
			ScriptsDirectory: "/usr/local/etc/agent/scripts",
			ConfigPath:       "/usr/local/etc/agent/config.yaml",
			ExporterURL:      "http://localhost:9100/metrics", // node_exporter
			NebulaCacheFile:  "/var/db/agent/nebula-cache.yaml",
		}
	default:
		// Fallback to Linux-like defaults for unknown platforms
		return PlatformDefaults{
			LogFile:          "/var/log/agent/agent.log",
			ScriptsDirectory: "/opt/agent/scripts",
			ConfigPath:       "/etc/agent/config.yaml",
			ExporterURL:      "http://localhost:9100/metrics",
			NebulaCacheFile:  "/var/lib/agent/nebula-cache.yaml",
		}
	}
}

// GetDefaultConfigPath returns the platform-specific default config path
func GetDefaultConfigPath() string {
	return GetPlatformDefaults().ConfigPath
}
