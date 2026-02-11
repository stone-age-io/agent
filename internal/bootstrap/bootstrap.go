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

// authResponse is the PocketBase auth-with-password response
type authResponse struct {
	Token string `json:"token"`
}

// FetchCredentials checks if the .creds file exists, and if not, fetches it
// from PocketBase and writes it to disk. Returns nil if the file already exists
// or was successfully created.
func FetchCredentials(cfg *config.Config, logger *zap.Logger) error {
	credsPath := cfg.NATS.Auth.CredsFile
	pb := cfg.NATS.Auth.PocketBase

	// If .creds file already exists, skip bootstrap
	if _, err := os.Stat(credsPath); err == nil {
		logger.Info("Credentials file exists, skipping bootstrap", zap.String("path", credsPath))
		return nil
	}

	logger.Info("Credentials file not found, bootstrapping from PocketBase",
		zap.String("path", credsPath),
		zap.String("pocketbase_url", pb.URL))

	// Read password from environment variable
	password := os.Getenv(pb.PasswordEnv)
	if password == "" {
		return fmt.Errorf("bootstrap: environment variable %s is not set or empty", pb.PasswordEnv)
	}

	client := &http.Client{Timeout: httpTimeout}

	// Step 1: Authenticate with PocketBase
	token, err := authenticate(client, pb.URL, pb.AuthCollection, pb.Identity, password)
	if err != nil {
		return fmt.Errorf("bootstrap: authentication failed: %w", err)
	}
	logger.Info("Authenticated with PocketBase")

	// Step 2: Fetch the credentials record
	credsContent, err := fetchCredsRecord(client, pb.URL, token, pb.Collection, pb.DeviceIDField, cfg.DeviceID, pb.CredsField)
	if err != nil {
		return fmt.Errorf("bootstrap: failed to fetch credentials: %w", err)
	}
	logger.Info("Fetched credentials from PocketBase")

	// Step 3: Write .creds file to disk
	if err := writeCredsFile(credsPath, credsContent); err != nil {
		return fmt.Errorf("bootstrap: failed to write credentials file: %w", err)
	}
	logger.Info("Credentials file written", zap.String("path", credsPath))

	return nil
}

// authenticate calls PocketBase auth-with-password and returns the JWT token
func authenticate(client *http.Client, baseURL, collection, identity, password string) (string, error) {
	url := fmt.Sprintf("%s/api/collections/%s/auth-with-password", strings.TrimRight(baseURL, "/"), collection)

	payload := fmt.Sprintf(`{"identity":%q,"password":%q}`, identity, password)
	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("auth returned %d: %s", resp.StatusCode, string(body))
	}

	var authResp authResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return "", fmt.Errorf("failed to parse auth response: %w", err)
	}

	if authResp.Token == "" {
		return "", fmt.Errorf("auth response contained no token")
	}

	return authResp.Token, nil
}

// fetchCredsRecord queries PocketBase for the device's credentials record and
// extracts the creds file content from the specified field
func fetchCredsRecord(client *http.Client, baseURL, token, collection, deviceIDField, deviceID, credsField string) (string, error) {
	filter := fmt.Sprintf("%s='%s'", deviceIDField, deviceID)
	url := fmt.Sprintf("%s/api/collections/%s/records?filter=%s&perPage=1",
		strings.TrimRight(baseURL, "/"),
		collection,
		filter,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("request returned %d: %s", resp.StatusCode, string(body))
	}

	// Parse the list response
	var result struct {
		Items      []map[string]interface{} `json:"items"`
		TotalItems int                      `json:"totalItems"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if result.TotalItems == 0 || len(result.Items) == 0 {
		return "", fmt.Errorf("no record found for %s='%s' in collection '%s'", deviceIDField, deviceID, collection)
	}

	// Extract the creds field
	credsValue, ok := result.Items[0][credsField]
	if !ok {
		return "", fmt.Errorf("record does not contain field '%s'", credsField)
	}

	credsStr, ok := credsValue.(string)
	if !ok || credsStr == "" {
		return "", fmt.Errorf("field '%s' is empty or not a string", credsField)
	}

	return credsStr, nil
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
