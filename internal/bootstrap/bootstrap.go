// Package bootstrap fetches the agent's NATS credentials from the
// stone-age.io platform (PocketBase) on first start.
//
// On the platform an agent is a Thing: it authenticates as itself against the
// `things` auth collection and reads its NATS credentials from the expanded
// `nats_user` relation's `creds_file` field. The platform's access rules are
// built for exactly this flow — an authenticated thing can see only its own
// record and only its assigned NATS user — so the whole bootstrap is a single
// auth-with-password call with an expand parameter.
package bootstrap

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stone-age-io/agent/internal/config"
	"go.uber.org/zap"
)

const httpTimeout = 15 * time.Second

// thingsCollection is the platform auth collection agents authenticate
// against. Fixed on purpose: the agent is opinionated about the stone-age.io
// schema (things → nats_user relation → creds_file).
const thingsCollection = "things"

// authResponse is the PocketBase auth-with-password response, narrowed to the
// fields the bootstrap needs from the authenticated thing record.
type authResponse struct {
	Record thingRecord `json:"record"`
}

type thingRecord struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	Expand struct {
		NATSUser struct {
			CredsFile string `json:"creds_file"`
		} `json:"nats_user"`
		Location struct {
			Code string `json:"code"`
		} `json:"location"`
	} `json:"expand"`
}

// FetchCredentials checks if the .creds file exists, and if not, fetches it
// from the platform and writes it to disk. Returns nil if the file already
// exists or was successfully created.
func FetchCredentials(cfg *config.Config, logger *zap.Logger) error {
	credsPath := cfg.NATS.Auth.CredsFile
	pb := cfg.NATS.Auth.PocketBase

	// If .creds file already exists, skip bootstrap
	if _, err := os.Stat(credsPath); err == nil {
		logger.Info("Credentials file exists, skipping bootstrap", zap.String("path", credsPath))
		return nil
	}

	logger.Info("Credentials file not found, bootstrapping from platform",
		zap.String("path", credsPath),
		zap.String("platform_url", pb.URL))

	// Read password from environment variable
	password := os.Getenv(pb.PasswordEnv)
	if password == "" {
		return fmt.Errorf("bootstrap: environment variable %s is not set or empty", pb.PasswordEnv)
	}

	client := &http.Client{Timeout: httpTimeout}

	// Authenticate as the thing; the expanded record carries everything we need
	record, err := authenticateThing(client, pb.URL, pb.Identity, password)
	if err != nil {
		return fmt.Errorf("bootstrap: authentication failed: %w", err)
	}
	logger.Info("Authenticated with platform as thing", zap.String("thing_id", record.ID))

	// The platform is the source of truth for identity: a code mismatch means
	// this device is running with the wrong config or the wrong thing login,
	// and its telemetry would be attributed to the wrong device. Fail fast.
	if record.Code != cfg.Code {
		return fmt.Errorf("bootstrap: code mismatch: config has %q but the platform thing record has %q — fix the agent config or the thing record before starting", cfg.Code, record.Code)
	}

	// Location is advisory (payload-only), so a mismatch warns instead of failing
	if platformLoc := record.Expand.Location.Code; platformLoc != "" && cfg.Location != platformLoc {
		logger.Warn("Location mismatch between config and platform thing record",
			zap.String("config_location", cfg.Location),
			zap.String("platform_location", platformLoc))
	}

	creds := record.Expand.NATSUser.CredsFile
	if creds == "" {
		return fmt.Errorf("bootstrap: thing record has no NATS credentials (is a nats_user assigned to this thing, and does it have creds generated?)")
	}
	logger.Info("Fetched credentials from platform")

	// Write .creds file to disk
	if err := writeCredsFile(credsPath, creds); err != nil {
		return fmt.Errorf("bootstrap: failed to write credentials file: %w", err)
	}
	logger.Info("Credentials file written", zap.String("path", credsPath))

	return nil
}

// authenticateThing calls auth-with-password on the things collection with
// nats_user and location expanded, returning the authenticated thing record.
func authenticateThing(client *http.Client, baseURL, identity, password string) (*thingRecord, error) {
	url := fmt.Sprintf("%s/api/collections/%s/auth-with-password?expand=nats_user,location",
		strings.TrimRight(baseURL, "/"), thingsCollection)

	payload := fmt.Sprintf(`{"identity":%q,"password":%q}`, identity, password)
	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body) //nolint:errcheck // best-effort read for error message
		return nil, fmt.Errorf("auth returned %d: %s", resp.StatusCode, string(body))
	}

	var authResp authResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return nil, fmt.Errorf("failed to parse auth response: %w", err)
	}

	if authResp.Record.ID == "" {
		return nil, fmt.Errorf("auth response contained no thing record")
	}

	return &authResp.Record, nil
}

// writeCredsFile writes the credentials content to disk, creating parent
// directories if needed. File is written with restrictive permissions.
func writeCredsFile(path, content string) error {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Write with restrictive permissions (owner read/write only)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}
