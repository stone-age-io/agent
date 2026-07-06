package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stone-age-io/agent/internal/config"
	"go.uber.org/zap"
)

const testCreds = "-----BEGIN NATS USER JWT-----\nabc123\n------END NATS USER JWT------\n"

// newPlatformServer returns an httptest server that mimics the platform's
// things auth-with-password endpoint with nats_user/location expanded.
func newPlatformServer(t *testing.T, thingCode, locationCode, credsFile string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/collections/things/auth-with-password" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if expand := r.URL.Query().Get("expand"); expand != "nats_user,location" {
			t.Errorf("expected expand=nats_user,location, got %q", expand)
		}

		var body struct {
			Identity string `json:"identity"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.Identity != "thing@example.com" || body.Password != "secret" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"Failed to authenticate."}`)) //nolint:errcheck
			return
		}

		expand := map[string]interface{}{}
		if credsFile != "" {
			expand["nats_user"] = map[string]interface{}{"creds_file": credsFile}
		}
		if locationCode != "" {
			expand["location"] = map[string]interface{}{"code": locationCode}
		}
		resp := map[string]interface{}{
			"token": "test-jwt",
			"record": map[string]interface{}{
				"id":     "thing123",
				"code":   thingCode,
				"expand": expand,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
}

func testConfig(t *testing.T, url string) *config.Config {
	t.Helper()
	return &config.Config{
		Code:     "server-01",
		Location: "hq",
		NATS: config.NATSConfig{
			Auth: config.AuthConfig{
				Type:      "pocketbase",
				CredsFile: filepath.Join(t.TempDir(), "device.creds"),
				PocketBase: config.PocketBaseAuth{
					URL:         url,
					Identity:    "thing@example.com",
					PasswordEnv: "TEST_AGENT_PB_PASSWORD",
				},
			},
		},
	}
}

func TestFetchCredentialsSuccess(t *testing.T) {
	srv := newPlatformServer(t, "server-01", "hq", testCreds)
	defer srv.Close()

	cfg := testConfig(t, srv.URL)
	t.Setenv("TEST_AGENT_PB_PASSWORD", "secret")

	if err := FetchCredentials(cfg, zap.NewNop()); err != nil {
		t.Fatalf("FetchCredentials() error = %v", err)
	}

	content, err := os.ReadFile(cfg.NATS.Auth.CredsFile)
	if err != nil {
		t.Fatalf("creds file not written: %v", err)
	}
	if string(content) != testCreds {
		t.Errorf("creds file content = %q, want %q", content, testCreds)
	}
}

func TestFetchCredentialsSkipsWhenFileExists(t *testing.T) {
	// Server that fails the test if contacted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("platform should not be contacted when creds file exists")
	}))
	defer srv.Close()

	cfg := testConfig(t, srv.URL)
	if err := os.WriteFile(cfg.NATS.Auth.CredsFile, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_AGENT_PB_PASSWORD", "secret")

	if err := FetchCredentials(cfg, zap.NewNop()); err != nil {
		t.Fatalf("FetchCredentials() error = %v", err)
	}

	content, _ := os.ReadFile(cfg.NATS.Auth.CredsFile) //nolint:errcheck
	if string(content) != "existing" {
		t.Errorf("existing creds file was overwritten")
	}
}

func TestFetchCredentialsCodeMismatch(t *testing.T) {
	srv := newPlatformServer(t, "different-thing", "hq", testCreds)
	defer srv.Close()

	cfg := testConfig(t, srv.URL)
	t.Setenv("TEST_AGENT_PB_PASSWORD", "secret")

	err := FetchCredentials(cfg, zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "code mismatch") {
		t.Fatalf("FetchCredentials() error = %v, want code mismatch error", err)
	}
	if _, statErr := os.Stat(cfg.NATS.Auth.CredsFile); statErr == nil {
		t.Error("creds file should not be written on code mismatch")
	}
}

func TestFetchCredentialsMissingNATSUser(t *testing.T) {
	srv := newPlatformServer(t, "server-01", "hq", "")
	defer srv.Close()

	cfg := testConfig(t, srv.URL)
	t.Setenv("TEST_AGENT_PB_PASSWORD", "secret")

	err := FetchCredentials(cfg, zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "no NATS credentials") {
		t.Fatalf("FetchCredentials() error = %v, want missing credentials error", err)
	}
}

func TestFetchCredentialsBadPassword(t *testing.T) {
	srv := newPlatformServer(t, "server-01", "hq", testCreds)
	defer srv.Close()

	cfg := testConfig(t, srv.URL)
	t.Setenv("TEST_AGENT_PB_PASSWORD", "wrong-password")

	err := FetchCredentials(cfg, zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("FetchCredentials() error = %v, want authentication error", err)
	}
}

func TestFetchCredentialsMissingPasswordEnv(t *testing.T) {
	cfg := testConfig(t, "http://unused.example.com")
	t.Setenv("TEST_AGENT_PB_PASSWORD", "")

	err := FetchCredentials(cfg, zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "not set or empty") {
		t.Fatalf("FetchCredentials() error = %v, want missing env var error", err)
	}
}
