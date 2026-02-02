package tasks

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

// MetricsCollector defines the interface for collecting system metrics
type MetricsCollector interface {
	// Collect gathers system metrics
	// Returns metrics with CPU/disk I/O rates as 0 on first call (baseline establishment)
	Collect(ctx context.Context) (*SystemMetrics, error)

	// Name returns the collector name for logging
	Name() string

	// ResetCache clears rate calculation state (for staleness handling)
	ResetCache()
}

// NewMetricsCollector creates the appropriate collector based on configuration
func NewMetricsCollector(source, exporterURL string, logger *zap.Logger, httpClient *http.Client) (MetricsCollector, error) {
	source = strings.ToLower(source)
	if source == "" {
		source = "builtin" // Default
	}

	switch source {
	case "builtin":
		logger.Info("Using builtin metrics collector (gopsutil)")
		return NewBuiltinCollector(logger), nil
	case "exporter":
		if exporterURL == "" {
			return nil, fmt.Errorf("exporter_url required for exporter source")
		}
		logger.Info("Using exporter metrics collector", zap.String("url", exporterURL))
		return NewExporterCollector(exporterURL, logger, httpClient), nil
	default:
		return nil, fmt.Errorf("unknown metrics source: %s", source)
	}
}
