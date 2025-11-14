package utils

import "math"

// Round rounds a float64 value to 2 decimal places
// Used throughout the agent for metrics to avoid unnecessary precision
func Round(val float64) float64 {
	// Use proper rounding that works for both positive and negative numbers
	return math.Round(val*100) / 100
}
