package tasks

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

// TestIsServiceAllowed tests service whitelist validation
// This is CRITICAL for security - prevents unauthorized service control
func TestIsServiceAllowed(t *testing.T) {
	tests := []struct {
		name            string
		serviceName     string
		allowedServices []string
		want            bool
		reason          string
	}{
		// Valid cases
		{
			name:        "exact match",
			serviceName: "MyAppService",
			allowedServices: []string{
				"MyAppService",
				"OtherService",
			},
			want:   true,
			reason: "exact service name match should be allowed",
		},
		{
			name:        "single allowed service",
			serviceName: "CriticalService",
			allowedServices: []string{
				"CriticalService",
			},
			want:   true,
			reason: "single service in whitelist should be allowed",
		},
		{
			name:        "match from multiple",
			serviceName: "DatabaseService",
			allowedServices: []string{
				"WebService",
				"DatabaseService",
				"CacheService",
			},
			want:   true,
			reason: "should match one of multiple allowed services",
		},

		// Invalid cases - security critical
		{
			name:        "not in whitelist",
			serviceName: "DangerousService",
			allowedServices: []string{
				"MyAppService",
				"OtherService",
			},
			want:   false,
			reason: "service not in whitelist must be rejected",
		},
		{
			name:            "empty whitelist",
			serviceName:     "MyAppService",
			allowedServices: []string{},
			want:            false,
			reason:          "empty whitelist means nothing allowed",
		},
		{
			name:        "case sensitivity",
			serviceName: "myappservice",
			allowedServices: []string{
				"MyAppService",
			},
			want:   false,
			reason: "case differences must be rejected - exact match required",
		},
		{
			name:        "partial match",
			serviceName: "MyAppServiceExtended",
			allowedServices: []string{
				"MyAppService",
			},
			want:   false,
			reason: "partial match must be rejected",
		},
		{
			name:        "system service",
			serviceName: "WinDefend",
			allowedServices: []string{
				"MyAppService",
			},
			want:   false,
			reason: "system services must be rejected if not explicitly whitelisted",
		},
		{
			name:        "windows service",
			serviceName: "W32Time",
			allowedServices: []string{
				"MyAppService",
			},
			want:   false,
			reason: "Windows services must be rejected if not explicitly whitelisted",
		},
		{
			name:        "empty service name",
			serviceName: "",
			allowedServices: []string{
				"MyAppService",
			},
			want:   false,
			reason: "empty service name must be rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isServiceAllowed(tt.serviceName, tt.allowedServices)
			if got != tt.want {
				t.Errorf("isServiceAllowed() = %v, want %v: %s", got, tt.want, tt.reason)
			}
		})
	}
}

// TestControlService tests service control validation
func TestControlService(t *testing.T) {
	// Note: These tests validate the whitelist logic only
	// Actual service control tests would require Windows services and are integration tests
	
	// Create executor with builtin metrics source for tests
	executor, err := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	tests := []struct {
		name            string
		serviceName     string
		action          string
		allowedServices []string
		wantErr         bool
		errContains     string
	}{
		{
			name:        "service not in whitelist",
			serviceName: "UnauthorizedService",
			action:      "start",
			allowedServices: []string{
				"MyAppService",
			},
			wantErr:     true,
			errContains: "not in allowed list",
		},
		{
			name:        "invalid action",
			serviceName: "MyAppService",
			action:      "destroy",
			allowedServices: []string{
				"MyAppService",
			},
			wantErr:     true,
			errContains: "invalid action",
		},
		{
			name:        "empty service name",
			serviceName: "",
			action:      "start",
			allowedServices: []string{
				"MyAppService",
			},
			wantErr:     true,
			errContains: "not in allowed list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: This will fail at the Windows service API stage if allowed
			// We're testing whitelist and action validation here
			_, err := executor.ControlService(tt.serviceName, tt.action, tt.allowedServices)
			
			if (err != nil) != tt.wantErr {
				t.Errorf("ControlService() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			
			if tt.wantErr && tt.errContains != "" {
				if err == nil || indexOf(err.Error(), tt.errContains) < 0 {
					t.Errorf("ControlService() error = %v, want error containing %q", err, tt.errContains)
				}
			}
		})
	}
}
