package tasks

import (
	"testing"
	"time"
)

// TestCreateHeartbeat tests heartbeat message creation
func TestCreateHeartbeat(t *testing.T) {
	executor := NewExecutor(nil, 0)
	version := "1.0.0"

	// Create heartbeat
	hb := executor.CreateHeartbeat(version)

	// Verify version is set
	if hb.Version != version {
		t.Errorf("CreateHeartbeat() version = %v, want %v", hb.Version, version)
	}

	// Verify timestamp is set and recent
	if hb.Timestamp == "" {
		t.Error("CreateHeartbeat() timestamp is empty")
	}

	// Parse timestamp and verify it's recent (within last second)
	ts, err := time.Parse(time.RFC3339, hb.Timestamp)
	if err != nil {
		t.Errorf("CreateHeartbeat() timestamp parse error: %v", err)
	}

	timeDiff := time.Since(ts)
	if timeDiff > time.Second {
		t.Errorf("CreateHeartbeat() timestamp too old: %v", timeDiff)
	}
	if timeDiff < 0 {
		t.Errorf("CreateHeartbeat() timestamp in future: %v", timeDiff)
	}
}

// TestCreateHeartbeatFormat tests that heartbeat uses correct time format
func TestCreateHeartbeatFormat(t *testing.T) {
	executor := NewExecutor(nil, 0)

	hb := executor.CreateHeartbeat("1.0.0")

	// Verify it's valid RFC3339
	_, err := time.Parse(time.RFC3339, hb.Timestamp)
	if err != nil {
		t.Errorf("CreateHeartbeat() timestamp not RFC3339 format: %v", err)
	}

	// Verify UTC timezone
	ts, _ := time.Parse(time.RFC3339, hb.Timestamp)
	if ts.Location() != time.UTC {
		t.Errorf("CreateHeartbeat() timestamp not in UTC: %v", ts.Location())
	}
}

// TestCreateHeartbeatConsistency tests that multiple heartbeats have consistent format
func TestCreateHeartbeatConsistency(t *testing.T) {
	executor := NewExecutor(nil, 0)
	version := "1.0.0"

	// Create multiple heartbeats
	hb1 := executor.CreateHeartbeat(version)
	time.Sleep(1 * time.Second)
	hb2 := executor.CreateHeartbeat(version)

	// Both should have same version
	if hb1.Version != hb2.Version {
		t.Errorf("Heartbeats have inconsistent versions: %v vs %v", hb1.Version, hb2.Version)
	}

	// Timestamps should be different (time has passed)
	if hb1.Timestamp == hb2.Timestamp {
		t.Error("Heartbeat timestamps should be different")
	}

	// Both timestamps should be valid
	_, err1 := time.Parse(time.RFC3339, hb1.Timestamp)
	_, err2 := time.Parse(time.RFC3339, hb2.Timestamp)
	if err1 != nil || err2 != nil {
		t.Errorf("Invalid timestamps: %v, %v", err1, err2)
	}
}
