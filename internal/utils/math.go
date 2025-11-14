package utils

// Round rounds a float64 value to 2 decimal places
// Used throughout the agent for metrics to avoid unnecessary precision
func Round(val float64) float64 {
	return float64(int(val*100+0.5)) / 100
}
