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
		}
	case "linux":
		return PlatformDefaults{
			LogFile:          "/var/log/agent/agent.log",
			ScriptsDirectory: "/opt/agent/scripts",
			ConfigPath:       "/etc/agent/config.yaml",
			ExporterURL:      "http://localhost:9100/metrics", // node_exporter
		}
	case "freebsd":
		return PlatformDefaults{
			LogFile:          "/var/log/agent/agent.log",
			ScriptsDirectory: "/usr/local/etc/agent/scripts",
			ConfigPath:       "/usr/local/etc/agent/config.yaml",
			ExporterURL:      "http://localhost:9100/metrics", // node_exporter
		}
	default:
		// Fallback to Linux-like defaults for unknown platforms
		return PlatformDefaults{
			LogFile:          "/var/log/agent/agent.log",
			ScriptsDirectory: "/opt/agent/scripts",
			ConfigPath:       "/etc/agent/config.yaml",
			ExporterURL:      "http://localhost:9100/metrics",
		}
	}
}

// GetDefaultConfigPath returns the platform-specific default config path
func GetDefaultConfigPath() string {
	return GetPlatformDefaults().ConfigPath
}
