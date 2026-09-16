package platform

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

const (
	testCreds    = "-----BEGIN NATS USER JWT-----\nabc123\n------END NATS USER JWT------\n"
	newTestCreds = "-----BEGIN NATS USER JWT-----\nxyz789\n------END NATS USER JWT------\n"
	testToken    = "session-token-1"
	freshToken   = "session-token-2"
	passwordEnv  = "TEST_AGENT_PLATFORM_PASSWORD"
)

// fakePlatform mimics the four platform endpoints the agent uses, and counts
// which ones were hit so tests can assert on the shape of the traffic and not
// just the outcome.
type fakePlatform struct {
	t *testing.T

	thingCode    string
	locationCode string
	natsUserID   string
	creds        string
	revision     string

	// rotation replaces the credential with these when the route is called
	rotatedCreds    string
	rotatedRevision string

	rejectRefresh  bool // pretend the stored token has lapsed
	rotateConfirms bool

	passwordAuths     int
	refreshes         int
	probes            int
	reads             int
	rotates           int
	lastRefreshExpand string
}

func newFakePlatform(t *testing.T) *fakePlatform {
	t.Helper()
	return &fakePlatform{
		t:               t,
		thingCode:       "server-01",
		locationCode:    "hq",
		natsUserID:      "natsuser123",
		creds:           testCreds,
		revision:        "2026-07-25 05:20:55.123Z",
		rotatedCreds:    newTestCreds,
		rotatedRevision: "2026-07-25 06:00:00.000Z",
		rotateConfirms:  true,
	}
}

func (f *fakePlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.URL.Path == "/api/collections/things/auth-with-password":
		f.passwordAuths++

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
			w.Write([]byte(`{"message":"Failed to authenticate."}`))
			return
		}
		f.writeAuth(w, r.URL.Query().Get("expand"))

	case r.URL.Path == "/api/collections/things/auth-refresh":
		f.refreshes++
		f.lastRefreshExpand = r.URL.Query().Get("expand")

		if f.rejectRefresh || r.Header.Get("Authorization") != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"The request requires valid record authorization token."}`))
			return
		}
		f.writeAuth(w, r.URL.Query().Get("expand"))

	case r.URL.Path == "/api/collections/nats_users/records/"+f.natsUserID:
		if !f.authorized(w, r) {
			return
		}
		// The routine drift probe asks for one field and must not be answered
		// with anything else — that is the whole point of the two-call path
		if r.URL.Query().Get("fields") == "updated" {
			f.probes++
			json.NewEncoder(w).Encode(map[string]string{"updated": f.revision})
			return
		}
		f.reads++
		json.NewEncoder(w).Encode(map[string]string{
			"creds_file": f.creds,
			"updated":    f.revision,
		})

	case r.URL.Path == rotatePath:
		f.rotates++
		if !f.authorized(w, r) {
			return
		}
		if !f.rotateConfirms {
			json.NewEncoder(w).Encode(map[string]any{"rotated": false})
			return
		}
		// pb-nats re-mints inside the update hook, so the new credential is
		// already readable by the time this route answers
		f.creds = f.rotatedCreds
		f.revision = f.rotatedRevision
		json.NewEncoder(w).Encode(map[string]any{
			"rotated":   true,
			"nats_user": f.natsUserID,
		})

	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func (f *fakePlatform) authorized(w http.ResponseWriter, r *http.Request) bool {
	if token := r.Header.Get("Authorization"); token != testToken && token != freshToken {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"unauthorized"}`))
		return false
	}
	return true
}

func (f *fakePlatform) writeAuth(w http.ResponseWriter, expand string) {
	record := map[string]any{
		"id":        "thing123",
		"code":      f.thingCode,
		"nats_user": f.natsUserID,
	}

	if strings.Contains(expand, "nats_user") || strings.Contains(expand, "location") {
		expanded := map[string]any{}
		if strings.Contains(expand, "nats_user") && f.creds != "" {
			expanded["nats_user"] = map[string]any{
				"creds_file": f.creds,
				"updated":    f.revision,
			}
		}
		if strings.Contains(expand, "location") && f.locationCode != "" {
			expanded["location"] = map[string]any{"code": f.locationCode}
		}
		record["expand"] = expanded
	}

	json.NewEncoder(w).Encode(map[string]any{
		"token":  freshToken,
		"record": record,
	})
}

// testClient wires a client to a fake platform, with both secret files inside a
// temp dir.
func testClient(t *testing.T, url string) *Client {
	t.Helper()

	dir := t.TempDir()
	cfg := &config.Config{
		Code:     "server-01",
		Location: "hq",
		Platform: config.PlatformConfig{
			URL:         url,
			Identity:    "thing@example.com",
			PasswordEnv: passwordEnv,
			SessionFile: filepath.Join(dir, "platform-session.json"),
		},
		NATS: config.NATSConfig{
			Auth: config.AuthConfig{
				Type:      "platform",
				CredsFile: filepath.Join(dir, "device.creds"),
			},
		},
	}

	return NewClient(cfg, zap.NewNop())
}

func readSession(t *testing.T, c *Client) session {
	t.Helper()

	data, err := os.ReadFile(c.auth.SessionFile)
	if err != nil {
		t.Fatalf("session file not written: %v", err)
	}
	var s session
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("session file unreadable: %v", err)
	}
	return s
}

func writeSession(t *testing.T, c *Client, s session) {
	t.Helper()

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.auth.SessionFile, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureCredentialsBootstraps(t *testing.T) {
	fake := newFakePlatform(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	t.Setenv(passwordEnv, "secret")

	if err := c.EnsureCredentials(); err != nil {
		t.Fatalf("EnsureCredentials() error = %v", err)
	}

	content, err := os.ReadFile(c.credsPath)
	if err != nil {
		t.Fatalf("creds file not written: %v", err)
	}
	if string(content) != testCreds {
		t.Errorf("creds file content = %q, want %q", content, testCreds)
	}

	// The session it banks is what lets the next start skip the password
	got := readSession(t, c)
	if got.Token != freshToken {
		t.Errorf("session token = %q, want %q", got.Token, freshToken)
	}
	if got.CredsRevision != fake.revision {
		t.Errorf("session revision = %q, want %q", got.CredsRevision, fake.revision)
	}
}

func TestEnsureCredentialsSkipsWhenFileExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("platform should not be contacted when the creds file exists")
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	if err := os.WriteFile(c.credsPath, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(passwordEnv, "secret")

	if err := c.EnsureCredentials(); err != nil {
		t.Fatalf("EnsureCredentials() error = %v", err)
	}

	content, _ := os.ReadFile(c.credsPath)
	if string(content) != "existing" {
		t.Error("existing creds file was overwritten")
	}
}

func TestEnsureCredentialsCodeMismatch(t *testing.T) {
	fake := newFakePlatform(t)
	fake.thingCode = "different-thing"
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	t.Setenv(passwordEnv, "secret")

	err := c.EnsureCredentials()
	if err == nil || !strings.Contains(err.Error(), "code mismatch") {
		t.Fatalf("EnsureCredentials() error = %v, want code mismatch error", err)
	}
	if _, statErr := os.Stat(c.credsPath); statErr == nil {
		t.Error("creds file should not be written on code mismatch")
	}
}

func TestEnsureCredentialsMissingNATSUser(t *testing.T) {
	fake := newFakePlatform(t)
	fake.creds = ""
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	t.Setenv(passwordEnv, "secret")

	err := c.EnsureCredentials()
	if err == nil || !strings.Contains(err.Error(), "no NATS credentials") {
		t.Fatalf("EnsureCredentials() error = %v, want missing credentials error", err)
	}
}

func TestEnsureCredentialsBadPassword(t *testing.T) {
	srv := httptest.NewServer(newFakePlatform(t))
	defer srv.Close()

	c := testClient(t, srv.URL)
	t.Setenv(passwordEnv, "wrong-password")

	err := c.EnsureCredentials()
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("EnsureCredentials() error = %v, want authentication error", err)
	}
}

func TestEnsureCredentialsMissingPasswordEnv(t *testing.T) {
	c := testClient(t, "https://unused.example.com")
	t.Setenv(passwordEnv, "")

	err := c.EnsureCredentials()
	if err == nil || !strings.Contains(err.Error(), "not set or empty") {
		t.Fatalf("EnsureCredentials() error = %v, want missing env var error", err)
	}
}

// TestSyncUnchangedKeepsCredentialOffTheWire pins the security property the
// two-call routine path exists for: when nothing has been re-minted, the agent
// renews its token and probes a timestamp, and no key material is transferred.
func TestSyncUnchangedKeepsCredentialOffTheWire(t *testing.T) {
	fake := newFakePlatform(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	if err := os.WriteFile(c.credsPath, []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}
	writeSession(t, c, session{Token: testToken, CredsRevision: fake.revision})

	changed, err := c.Sync()
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if changed {
		t.Error("Sync() reported a change when the revision was unchanged")
	}

	if fake.reads != 0 {
		t.Errorf("credential was read %d times, want 0 when unchanged", fake.reads)
	}
	if fake.probes != 1 {
		t.Errorf("revision probes = %d, want 1", fake.probes)
	}
	if fake.refreshes != 1 {
		t.Errorf("token refreshes = %d, want 1", fake.refreshes)
	}
	if fake.passwordAuths != 0 {
		t.Errorf("password auths = %d, want 0 when a session token is stored", fake.passwordAuths)
	}
	if fake.lastRefreshExpand != "" {
		t.Errorf("refresh asked for expand=%q, want no expand (it would return the credential)", fake.lastRefreshExpand)
	}

	// The renewed token has to be banked or the next start falls back to the password
	if got := readSession(t, c); got.Token != freshToken {
		t.Errorf("session token = %q, want the refreshed %q", got.Token, freshToken)
	}
}

func TestSyncAdoptsReMintedCredential(t *testing.T) {
	fake := newFakePlatform(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	if err := os.WriteFile(c.credsPath, []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}
	// An operator regenerated on the platform: same identity, newer revision
	writeSession(t, c, session{Token: testToken, CredsRevision: "2026-07-01 00:00:00.000Z"})
	fake.creds = newTestCreds

	changed, err := c.Sync()
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !changed {
		t.Error("Sync() reported no change after the credential was re-minted")
	}

	content, err := os.ReadFile(c.credsPath)
	if err != nil {
		t.Fatalf("creds file unreadable: %v", err)
	}
	if string(content) != newTestCreds {
		t.Errorf("creds file content = %q, want %q", content, newTestCreds)
	}
	if fake.reads != 1 {
		t.Errorf("credential reads = %d, want 1", fake.reads)
	}
	if got := readSession(t, c); got.CredsRevision != fake.revision {
		t.Errorf("session revision = %q, want %q", got.CredsRevision, fake.revision)
	}
}

// TestSyncRestoresDeletedCredsFile covers the case where the revision has not
// moved but the file is gone — deleting .creds used to be the documented way to
// force a re-fetch, so the agent has to notice.
func TestSyncRestoresDeletedCredsFile(t *testing.T) {
	fake := newFakePlatform(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	writeSession(t, c, session{Token: testToken, CredsRevision: fake.revision})
	// No creds file on disk, and the platform's revision matches the session

	changed, err := c.Sync()
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !changed {
		t.Error("Sync() reported no change when the creds file was missing")
	}

	content, err := os.ReadFile(c.credsPath)
	if err != nil {
		t.Fatalf("creds file was not restored: %v", err)
	}
	if string(content) != testCreds {
		t.Errorf("creds file content = %q, want %q", content, testCreds)
	}
}

// TestEnsureCredentialsRestoresWithSessionToken pins the password-free recovery
// path: a device that has dropped the password from its environment can still
// rebuild a deleted .creds from its stored session.
func TestEnsureCredentialsRestoresWithSessionToken(t *testing.T) {
	fake := newFakePlatform(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	writeSession(t, c, session{Token: testToken, CredsRevision: fake.revision})
	t.Setenv(passwordEnv, "") // password deliberately unavailable

	if err := c.EnsureCredentials(); err != nil {
		t.Fatalf("EnsureCredentials() error = %v", err)
	}

	content, err := os.ReadFile(c.credsPath)
	if err != nil {
		t.Fatalf("creds file not written: %v", err)
	}
	if string(content) != testCreds {
		t.Errorf("creds file content = %q, want %q", content, testCreds)
	}
	if fake.passwordAuths != 0 {
		t.Errorf("password auths = %d, want 0 when a session token can do the work", fake.passwordAuths)
	}
}

func TestSyncFallsBackToPasswordWhenTokenRejected(t *testing.T) {
	fake := newFakePlatform(t)
	fake.rejectRefresh = true
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	if err := os.WriteFile(c.credsPath, []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}
	writeSession(t, c, session{Token: testToken, CredsRevision: fake.revision})
	t.Setenv(passwordEnv, "secret")

	changed, err := c.Sync()
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if changed {
		t.Error("Sync() reported a change when only the token had lapsed")
	}
	if fake.refreshes != 1 || fake.passwordAuths != 1 {
		t.Errorf("refreshes = %d, password auths = %d; want 1 and 1", fake.refreshes, fake.passwordAuths)
	}
}

func TestSyncFailsWhenTokenLapsedAndNoPassword(t *testing.T) {
	fake := newFakePlatform(t)
	fake.rejectRefresh = true
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	writeSession(t, c, session{Token: testToken, CredsRevision: fake.revision})
	t.Setenv(passwordEnv, "")

	if _, err := c.Sync(); err == nil {
		t.Fatal("Sync() error = nil, want an error when the token lapsed and no password is available")
	}
}

func TestRotateAdoptsNewCredential(t *testing.T) {
	fake := newFakePlatform(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	if err := os.WriteFile(c.credsPath, []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}
	writeSession(t, c, session{Token: testToken, CredsRevision: fake.revision})

	changed, err := c.Rotate()
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if !changed {
		t.Error("Rotate() reported no change")
	}

	if fake.rotates != 1 {
		t.Errorf("rotation requests = %d, want 1", fake.rotates)
	}
	// One read straight after the route: pb-nats re-mints before the save
	// commits, so this must not need a polling loop
	if fake.reads != 1 {
		t.Errorf("credential reads = %d, want 1", fake.reads)
	}

	content, err := os.ReadFile(c.credsPath)
	if err != nil {
		t.Fatalf("creds file unreadable: %v", err)
	}
	if string(content) != newTestCreds {
		t.Errorf("creds file content = %q, want %q", content, newTestCreds)
	}
	if got := readSession(t, c); got.CredsRevision != fake.rotatedRevision {
		t.Errorf("session revision = %q, want %q", got.CredsRevision, fake.rotatedRevision)
	}
}

func TestRotateFailsWhenPlatformDoesNotConfirm(t *testing.T) {
	fake := newFakePlatform(t)
	fake.rotateConfirms = false
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := testClient(t, srv.URL)
	if err := os.WriteFile(c.credsPath, []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}
	writeSession(t, c, session{Token: testToken, CredsRevision: fake.revision})

	_, err := c.Rotate()
	if err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("Rotate() error = %v, want an unconfirmed rotation error", err)
	}

	content, _ := os.ReadFile(c.credsPath)
	if string(content) != testCreds {
		t.Error("creds file was replaced despite an unconfirmed rotation")
	}
}

// TestWriteCredsReplacesAtomically checks the rename path leaves no temp files
// behind, since they would be extra copies of a private key.
func TestWriteCredsReplacesAtomically(t *testing.T) {
	c := testClient(t, "https://unused.example.com")
	if err := os.WriteFile(c.credsPath, []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := c.writeCreds(testCreds)
	if err != nil {
		t.Fatalf("writeCreds() error = %v", err)
	}
	if changed {
		t.Error("writeCreds() reported a change when the content was identical")
	}

	changed, err = c.writeCreds(newTestCreds)
	if err != nil {
		t.Fatalf("writeCreds() error = %v", err)
	}
	if !changed {
		t.Error("writeCreds() reported no change when the content differed")
	}

	entries, err := os.ReadDir(filepath.Dir(c.credsPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
