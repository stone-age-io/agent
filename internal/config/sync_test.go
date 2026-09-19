package config

import (
	"strings"
	"testing"
)

// syncTestConfig is a config that passes validateSyncConfig, so each test below
// changes exactly the one thing it is about.
func syncTestConfig() *Config {
	cfg := &Config{}
	cfg.NATS.Auth.Type = authPlatform
	cfg.Sync.Twin = true
	return cfg
}

// The headline check. A bucket named in both directions has two writers, and
// two writers do not resolve to a loser — they oscillate, each write across the
// link generating the event that causes the next one back.
//
// With two built-in buckets this was unrepresentable. A list makes it one typo
// away, so it has to be refused at load: by the time the relay is running there
// is nothing useful left to do about it.
func TestSyncRejectsABucketInBothDirections(t *testing.T) {
	cfg := syncTestConfig()
	cfg.Sync.Twin = false
	cfg.Sync.Mirrors = []SyncBucket{{Bucket: "events"}}
	cfg.Sync.Relays = []SyncBucket{{Bucket: "events"}}

	err := validateSyncConfig(cfg)
	if err == nil {
		t.Fatal("a bucket declared in both directions was accepted; it will oscillate")
	}
	if !strings.Contains(err.Error(), "events") {
		t.Errorf("error does not name the offending bucket: %v", err)
	}
}

// The preset occupies its two names, so a user entry may not claim them either.
func TestSyncRejectsAUserEntryCollidingWithThePreset(t *testing.T) {
	for _, bucket := range PresetBuckets {
		cfg := syncTestConfig()
		cfg.Sync.Relays = []SyncBucket{{Bucket: bucket}}

		if err := validateSyncConfig(cfg); err == nil {
			t.Errorf("%q was accepted alongside sync.twin, which already declares it", bucket)
		}
	}
}

// Declaring the same bucket twice in one direction is a mistake rather than a
// hazard, but it would start two relays on one bucket and double every write.
func TestSyncRejectsADuplicateWithinOneDirection(t *testing.T) {
	cfg := syncTestConfig()
	cfg.Sync.Twin = false
	cfg.Sync.Relays = []SyncBucket{{Bucket: "events"}, {Bucket: "events"}}

	if err := validateSyncConfig(cfg); err == nil {
		t.Fatal("the same bucket was accepted twice in one direction")
	}
}

// twin.enabled never worked — the hub's JetStream domain never reached the
// running agent — so there is nothing to stay compatible with. But viper ignores
// keys it does not know, so a config file still carrying it would sync nothing
// and say nothing.
func TestTwinEnabledIsRejectedByName(t *testing.T) {
	cfg := &Config{}
	cfg.Twin.Enabled = true

	err := validateSyncConfig(cfg)
	if err == nil {
		t.Fatal("twin.enabled was ignored rather than rejected; the agent would silently sync nothing")
	}
	if !strings.Contains(err.Error(), "sync.twin") {
		t.Errorf("the error should name the replacement key, got: %v", err)
	}
}

// The hub's domain arrives with the leaf config from the platform and is
// reachable no other way, so sync without platform auth can never work.
func TestSyncRequiresPlatformAuth(t *testing.T) {
	cfg := syncTestConfig()
	cfg.NATS.Auth.Type = "creds"

	if err := validateSyncConfig(cfg); err == nil {
		t.Fatal("sync was accepted without platform auth; it could never learn the hub's domain")
	}
}

// keys is a key pattern, never a subject. A `$KV.` written here would be
// silently doubled when the agent builds the mirror's subject filter, and the
// mirror would match nothing — which cannot be corrected without deleting the
// bucket.
func TestSyncRejectsASubjectWhereAKeyPatternBelongs(t *testing.T) {
	cfg := syncTestConfig()
	cfg.Sync.Mirrors = []SyncBucket{{Bucket: "recipes", Keys: "$KV.recipes.line-a.>"}}

	err := validateSyncConfig(cfg)
	if err == nil {
		t.Fatal("a $KV subject was accepted as a key pattern")
	}
	// The message should show the key pattern they meant.
	if !strings.Contains(err.Error(), `"line-a.>"`) {
		t.Errorf("the error should suggest the key pattern, got: %v", err)
	}
}

func TestSyncRejectsAMalformedKeyPattern(t *testing.T) {
	for _, keys := range []string{
		"line-a..temp",  // empty token
		"line-a.>.temp", // > is not last
		"line a.>",      // space
	} {
		cfg := syncTestConfig()
		cfg.Sync.Relays = []SyncBucket{{Bucket: "events", Keys: keys}}

		if err := validateSyncConfig(cfg); err == nil {
			t.Errorf("key pattern %q was accepted", keys)
		}
	}
}

func TestSyncAcceptsAReasonableDeclaration(t *testing.T) {
	cfg := syncTestConfig()
	cfg.Sync.Mirrors = []SyncBucket{{Bucket: "recipes", Keys: "line-a.>"}}
	cfg.Sync.Relays = []SyncBucket{{Bucket: "events", Keys: "site.S01.*"}, {Bucket: "readings"}}

	if err := validateSyncConfig(cfg); err != nil {
		t.Fatalf("a valid declaration was refused: %v", err)
	}
}

// Nothing declared means nothing to check — an agent that never touches the sync
// block can never be refused on account of it.
func TestSyncSkipsEverythingWhenNothingIsDeclared(t *testing.T) {
	if err := validateSyncConfig(&Config{}); err != nil {
		t.Fatalf("an agent with no sync block was refused: %v", err)
	}
}
