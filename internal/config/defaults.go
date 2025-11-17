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

// UpdateConfigDefaults updates viper defaults with platform-specific values
// This should be called from setDefaults() in config.go
func UpdateConfigDefaults(v interface{}) {
	type viper interface {
		SetDefault(key string, value interface{})
	}
	
	if viperInstance, ok := v.(viper); ok {
		defaults := GetPlatformDefaults()
		
		// Update platform-specific defaults
		viperInstance.SetDefault("tasks.system_metrics.exporter_url", defaults.ExporterURL)
		viperInstance.SetDefault("commands.scripts_directory", defaults.ScriptsDirectory)
		viperInstance.SetDefault("logging.file", defaults.LogFile)
	}
}
