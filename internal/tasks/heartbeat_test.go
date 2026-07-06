package tasks

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestCreateHeartbeat tests heartbeat message creation
func TestCreateHeartbeat(t *testing.T) {
	executor, _ := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")

	hb := executor.CreateHeartbeat("server-01", "hq")

	if hb.Code != "server-01" {
		t.Errorf("CreateHeartbeat() code = %v, want server-01", hb.Code)
	}
	if hb.Location != "hq" {
		t.Errorf("CreateHeartbeat() location = %v, want hq", hb.Location)
	}

	// Verify timestamp is set and recent
	if hb.TS == "" {
		t.Error("CreateHeartbeat() ts is empty")
	}

	// Parse timestamp and verify it's recent (within last second)
	ts, err := time.Parse(time.RFC3339, hb.TS)
	if err != nil {
		t.Errorf("CreateHeartbeat() ts parse error: %v", err)
	}

	timeDiff := time.Since(ts)
	if timeDiff > time.Second {
		t.Errorf("CreateHeartbeat() ts too old: %v", timeDiff)
	}
	if timeDiff < 0 {
		t.Errorf("CreateHeartbeat() ts in future: %v", timeDiff)
	}
}

// TestCreateHeartbeatEmptyLocation tests that an unset location is carried as-is
func TestCreateHeartbeatEmptyLocation(t *testing.T) {
	executor, _ := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")

	hb := executor.CreateHeartbeat("server-01", "")

	if hb.Location != "" {
		t.Errorf("CreateHeartbeat() location = %v, want empty", hb.Location)
	}
	if hb.Code != "server-01" {
		t.Errorf("CreateHeartbeat() code = %v, want server-01", hb.Code)
	}
}

// TestCreateHeartbeatFormat tests that heartbeat uses correct time format
func TestCreateHeartbeatFormat(t *testing.T) {
	executor, _ := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")

	hb := executor.CreateHeartbeat("server-01", "hq")

	// Verify it's valid RFC3339
	_, err := time.Parse(time.RFC3339, hb.TS)
	if err != nil {
		t.Errorf("CreateHeartbeat() ts not RFC3339 format: %v", err)
	}

	// Verify UTC timezone
	ts, _ := time.Parse(time.RFC3339, hb.TS)
	if ts.Location() != time.UTC {
		t.Errorf("CreateHeartbeat() ts not in UTC: %v", ts.Location())
	}
}

// TestCreateHeartbeatConsistency tests that multiple heartbeats have consistent format
func TestCreateHeartbeatConsistency(t *testing.T) {
	executor, _ := NewExecutor(zap.NewNop(), 0, context.Background(), "builtin", "")

	// Create multiple heartbeats
	hb1 := executor.CreateHeartbeat("server-01", "hq")
	time.Sleep(1 * time.Second)
	hb2 := executor.CreateHeartbeat("server-01", "hq")

	// Both should carry the same identity
	if hb1.Code != hb2.Code || hb1.Location != hb2.Location {
		t.Errorf("Heartbeats have inconsistent identity: %v/%v vs %v/%v",
			hb1.Code, hb1.Location, hb2.Code, hb2.Location)
	}

	// Timestamps should be different (time has passed)
	if hb1.TS == hb2.TS {
		t.Error("Heartbeat timestamps should be different")
	}

	// Both timestamps should be valid
	_, err1 := time.Parse(time.RFC3339, hb1.TS)
	_, err2 := time.Parse(time.RFC3339, hb2.TS)
	if err1 != nil || err2 != nil {
		t.Errorf("Invalid timestamps: %v, %v", err1, err2)
	}
}
