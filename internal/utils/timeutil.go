package utils

import "time"

// NowRFC3339 returns the current UTC time formatted as RFC3339.
// All wire payloads (telemetry and command responses) use this format
// in their "ts" field, matching the other stone-age.io applications.
func NowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
