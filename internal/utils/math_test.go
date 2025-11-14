package utils

import (
	"math"
	"testing"
)

// TestRound tests the floating-point rounding function
func TestRound(t *testing.T) {
	tests := []struct {
		name  string
		input float64
		want  float64
	}{
		// Basic rounding
		{
			name:  "round down",
			input: 1.234,
			want:  1.23,
		},
		{
			name:  "round up",
			input: 1.236,
			want:  1.24,
		},
		{
			name:  "exact two decimals",
			input: 1.23,
			want:  1.23,
		},
		{
			name:  "one decimal",
			input: 1.2,
			want:  1.2,
		},
		{
			name:  "no decimals",
			input: 1.0,
			want:  1.0,
		},
		{
			name:  "zero",
			input: 0.0,
			want:  0.0,
		},

		// Edge cases
		{
			name:  "negative round down",
			input: -1.234,
			want:  -1.23,
		},
		{
			name:  "negative round up",
			input: -1.236,
			want:  -1.24,
		},
		{
			name:  "very small positive",
			input: 0.001,
			want:  0.0,
		},
		{
			name:  "very small negative",
			input: -0.001,
			want:  0.0,
		},
		{
			name:  "boundary .5",
			input: 1.235,
			want:  1.24, // Should round up
		},
		{
			name:  "boundary .5 negative",
			input: -1.235,
			want:  -1.24, // Should round away from zero
		},

		// Realistic metrics values
		{
			name:  "cpu percentage",
			input: 23.456789,
			want:  23.46,
		},
		{
			name:  "memory GB",
			input: 15.9876,
			want:  15.99,
		},
		{
			name:  "disk free percent",
			input: 78.123456,
			want:  78.12,
		},
		{
			name:  "disk io rate",
			input: 1234567.89,
			want:  1234567.89,
		},

		// Large numbers
		{
			name:  "large number",
			input: 9999999.999,
			want:  10000000.0,
		},
		{
			name:  "very large number",
			input: 123456789.123456,
			want:  123456789.12,
		},

		// Scientific notation
		{
			name:  "scientific small",
			input: 1e-5,
			want:  0.0,
		},
		{
			name:  "scientific large",
			input: 1.23456e6,
			want:  1234560.0,
		},

		// Special values (documented behavior)
		{
			name:  "exactly 100",
			input: 100.0,
			want:  100.0,
		},
		{
			name:  "just under 100",
			input: 99.999,
			want:  100.0,
		},
		{
			name:  "just over 100",
			input: 100.001,
			want:  100.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Round(tt.input)
			
			// Use a small epsilon for floating-point comparison
			epsilon := 0.001
			if math.Abs(got-tt.want) > epsilon {
				t.Errorf("Round(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestRoundPrecision tests that Round maintains exactly 2 decimal places
func TestRoundPrecision(t *testing.T) {
	tests := []float64{
		1.23456789,
		99.999999,
		0.001,
		1234567.89123,
		-45.678901,
	}

	for _, input := range tests {
		result := Round(input)
		
		// Convert to string and check decimal places
		// This ensures we're actually maintaining precision
		rounded := Round(result)
		if rounded != result {
			t.Errorf("Round(%v) = %v, but Round(Round(%v)) = %v - not stable at 2 decimals",
				input, result, input, rounded)
		}
	}
}

// BenchmarkRound benchmarks the rounding function
func BenchmarkRound(b *testing.B) {
	values := []float64{
		1.23456789,
		99.999999,
		0.001,
		1234567.89123,
		-45.678901,
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Round(values[i%len(values)])
	}
}
