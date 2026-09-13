// Package platform gets this agent's NATS credentials from the stone-age.io
// platform and keeps them current.
//
// On the platform an agent is a Thing: it authenticates as itself against the
// `things` auth collection, and its NATS credential lives on the related
// `nats_users` record's `creds_file` field. The platform's access rules are
// built for exactly this shape — an authenticated thing can see only its own
// record and only its assigned NATS identity — so no server-side route is
// needed to read a credential.
//
// Three operations, in the order a device meets them:
//
//	EnsureCredentials  First boot: log in with the thing's password and write
//	                   the .creds file.
//	Sync               Routine upkeep: keep the platform session token alive and
//	                   pick up a credential the platform re-minted (an operator
//	                   pressing Regenerate, say).
//	Rotate             On demand: ask the platform to re-mint this agent's
//	                   credential, then take the new one.
//
// Sync deliberately does NOT ask for the credential every time. A .creds file
// embeds the nkey seed — a private key — so the routine path refreshes the token
// without expanding any relation and then probes only the credential's `updated`
// timestamp via ?fields=updated. Key material crosses the network only when it
// has actually changed. That split is necessary rather than fussy: `fields` is
// not applied to PocketBase auth responses (apis/record_helpers.go marshals the
// record by hand), so any auth call with expand=nats_user returns the whole
// credential whether the caller wants it or not.
package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stone-age-io/agent/internal/config"
	"go.uber.org/zap"
)

const httpTimeout = 15 * time.Second

// The collections are fixed on purpose: the agent is opinionated about the
// stone-age.io schema (things → nats_user relation → creds_file).
const (
	thingsCollection    = "things"
	natsUsersCollection = "nats_users"
)

// rotatePath is the platform's self-service rotation route. It takes no record
// id — it derives the caller's identity from the auth token — and writes only
// the `regenerate` flag (platform hooks/credential_routes.go).
const rotatePath = "/api/me/nats-creds/rotate"

// Client talks to one platform instance on behalf of one agent.
type Client struct {
	// mu serialises everything that touches the session file. The credential sync
	// and the Nebula config sync are separate scheduled jobs sharing one session,
	// and each does a read-modify-write on it; without this, one can drop the
	// other's revision or, worse, its token — the agent's only password-free way
	// back in.
	mu sync.Mutex

	code      string
	location  string
	credsPath string
	auth      config.StoneAgeAuth
	http      *http.Client
	logger    *zap.Logger
}

// NewClient builds a client from validated config. The URL scheme is checked in
// config.validate() so a misconfigured agent fails at load rather than on the
// first request.
func NewClient(cfg *config.Config, logger *zap.Logger) *Client {
	return &Client{
		code:      cfg.Code,
		location:  cfg.Location,
		credsPath: cfg.NATS.Auth.CredsFile,
		auth:      cfg.NATS.Auth.StoneAge,
		http:      &http.Client{Timeout: httpTimeout},
		logger:    logger,
	}
}

// thingRecord is the authenticated thing, narrowed to the fields the agent uses.
// NATSUserID is the raw relation id, which is present whether or not the caller
// asked for the relation to be expanded.
type thingRecord struct {
	ID         string `json:"id"`
	Code       string `json:"code"`
	NATSUserID string `json:"nats_user"`
	// NebulaHostID is the parallel relation for the overlay identity, read by
	// NebulaSource in nebula.go.
	NebulaHostID string `json:"nebula_host"`
	Expand     struct {
		NATSUser natsUserRecord `json:"nats_user"`
		Location struct {
			Code string `json:"code"`
		} `json:"location"`
	} `json:"expand"`
}

// natsUserRecord is the NATS identity. Updated is the drift signal; CredsFile is
// the credential itself and is only ever requested once Updated says it moved.
type natsUserRecord struct {
	CredsFile string `json:"creds_file"`
	Updated   string `json:"updated"`
}

type authResponse struct {
	Token  string      `json:"token"`
	Record thingRecord `json:"record"`
}

// EnsureCredentials writes the .creds file if it is missing. It is a no-op once
// the file exists, which keeps startup fast and offline-tolerant on every boot
// after the first.
func (c *Client) EnsureCredentials() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.credsFileExists() {
		c.logger.Info("Credentials file exists, skipping bootstrap", zap.String("path", c.credsPath))
		return nil
	}

	c.logger.Info("Credentials file not found, bootstrapping from platform",
		zap.String("path", c.credsPath),
		zap.String("platform_url", c.auth.URL))

	// A device that lost its .creds but still holds a session token should not
	// need the thing's password handed back to it, so try the token first. On a
	// true first boot there is no session and this is skipped.
	if c.loadSession().Token != "" {
		if _, err := c.syncLocked(); err == nil {
			c.logger.Info("Credentials restored using the stored platform session")
			return nil
		} else {
			c.logger.Warn("Could not restore credentials with the stored platform session, falling back to password",
				zap.Error(err))
		}
	}

	// First boot is the one call that has to carry the credential, so it is also
	// the only one that expands relations: creds and location in a single trip.
	token, record, err := c.authWithPassword("nats_user,location")
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	c.logger.Info("Authenticated with platform as thing", zap.String("thing_id", record.ID))

	// Location is advisory (it only rides along in payloads), so a mismatch
	// warns where a code mismatch fails.
	if platformLoc := record.Expand.Location.Code; platformLoc != "" && c.location != platformLoc {
		c.logger.Warn("Location mismatch between config and platform thing record",
			zap.String("config_location", c.location),
			zap.String("platform_location", platformLoc))
	}

	creds := record.Expand.NATSUser.CredsFile
	if creds == "" {
		return errors.New("bootstrap: thing record has no NATS credentials (is a nats_user assigned to this thing, and does it have creds generated?)")
	}

	if _, err := c.writeCreds(creds); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	c.logger.Info("Credentials file written", zap.String("path", c.credsPath))

	// Record the revision just taken so the first Sync has something to compare
	// against and doesn't re-fetch a key it already holds.
	c.saveSession(session{Token: token, CredsRevision: record.Expand.NATSUser.Updated})

	return nil
}

// Sync renews the platform session token and adopts the credential the platform
// currently holds for this agent, reporting whether the .creds file changed.
//
// This is the routine path, so it stays off the credential unless the revision
// moved: refresh (no expand) then probe (?fields=updated), and only then read
// the key.
func (c *Client) Sync() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.syncLocked()
}

// syncLocked is Sync with the session lock already held, so EnsureCredentials can
// reuse it. Go mutexes are not reentrant: calling the exported Sync from another
// exported method that holds the lock deadlocks the agent at startup.
func (c *Client) syncLocked() (bool, error) {
	prev := c.loadSession()

	token, record, err := c.authenticate(prev.Token)
	if err != nil {
		return false, err
	}
	if record.NATSUserID == "" {
		return false, errors.New("thing record has no nats_user assigned")
	}

	revision, err := c.probeCredsRevision(token, record.NATSUserID)
	if err != nil {
		return false, err
	}

	// Unchanged since the last look, and the file is still where it belongs: bank
	// the refreshed token and leave the key material on the platform. The file
	// check matters — without it a deleted .creds would never be restored,
	// because its revision on the platform has not moved.
	if revision != "" && revision == prev.CredsRevision && c.credsFileExists() {
		c.saveSession(session{Token: token, CredsRevision: revision})
		return false, nil
	}

	user, err := c.fetchCreds(token, record.NATSUserID)
	if err != nil {
		return false, err
	}

	changed, err := c.writeCreds(user.CredsFile)
	if err != nil {
		return false, err
	}
	c.saveSession(session{Token: token, CredsRevision: user.Updated})

	return changed, nil
}

// Rotate asks the platform to re-mint this agent's NATS credential and adopts
// the result, reporting whether the .creds file changed.
//
// Rotation is not revocation: the previous credential stays valid until it
// expires or an operator revokes it on the platform.
func (c *Client) Rotate() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	prev := c.loadSession()

	token, record, err := c.authenticate(prev.Token)
	if err != nil {
		return false, err
	}

	natsUserID, err := c.requestRotate(token)
	if err != nil {
		return false, err
	}
	if natsUserID == "" {
		natsUserID = record.NATSUserID
	}
	if natsUserID == "" {
		return false, errors.New("rotation returned no nats_user id")
	}

	// pb-nats handles the `regenerate` flag in a record-update MODEL hook and
	// sets the new jwt/creds_file on the record before the save commits
	// (pb-nats internal/sync/manager.go). The new credential is therefore
	// already durable by the time the route answered — this read needs no
	// polling, and adding a retry loop here would only hide a real failure.
	user, err := c.fetchCreds(token, natsUserID)
	if err != nil {
		return false, err
	}

	changed, err := c.writeCreds(user.CredsFile)
	if err != nil {
		return false, err
	}
	c.saveSession(session{Token: token, CredsRevision: user.Updated})

	return changed, nil
}

// authenticate returns a live platform token and this agent's thing record,
// preferring the stored session token so the thing's password is only needed on
// first boot — or after the token's TTL lapses without a refresh.
//
// Neither call expands anything: the only field the callers need from the record
// is the `nats_user` relation id, which is a plain field.
func (c *Client) authenticate(prevToken string) (string, *thingRecord, error) {
	if prevToken != "" {
		token, record, err := c.authRefresh(prevToken)
		if err == nil {
			return token, record, nil
		}
		// A lapsed or invalidated token is an expected end-state, not a fault,
		// so this falls through to the password instead of failing.
		c.logger.Info("Platform session token rejected, falling back to password",
			zap.Error(err))
	}

	return c.authWithPassword("")
}

// authWithPassword logs in as the thing with the password from the environment.
func (c *Client) authWithPassword(expand string) (string, *thingRecord, error) {
	if c.auth.PasswordEnv == "" {
		return "", nil, errors.New("no password_env configured and no usable platform session token")
	}
	password := os.Getenv(c.auth.PasswordEnv)
	if password == "" {
		return "", nil, fmt.Errorf("environment variable %s is not set or empty", c.auth.PasswordEnv)
	}

	payload, err := json.Marshal(map[string]string{
		"identity": c.auth.Identity,
		"password": password,
	})
	if err != nil {
		return "", nil, fmt.Errorf("failed to encode auth request: %w", err)
	}

	req, err := c.newRequest(http.MethodPost,
		c.url("/api/collections/"+thingsCollection+"/auth-with-password", expand, ""),
		bytes.NewReader(payload))
	if err != nil {
		return "", nil, err
	}

	var resp authResponse
	if err := c.do(req, &resp); err != nil {
		return "", nil, fmt.Errorf("authentication failed: %w", err)
	}

	return c.finishAuth(&resp)
}

// authRefresh trades a valid session token for a fresh one, renewing its TTL.
func (c *Client) authRefresh(token string) (string, *thingRecord, error) {
	req, err := c.newRequest(http.MethodPost,
		c.url("/api/collections/"+thingsCollection+"/auth-refresh", "", ""), nil)
	if err != nil {
		return "", nil, err
	}
	c.setAuth(req, token)

	var resp authResponse
	if err := c.do(req, &resp); err != nil {
		return "", nil, err
	}

	return c.finishAuth(&resp)
}

// finishAuth validates an auth or refresh response and enforces that the
// platform agrees about who this device is.
//
// A code mismatch means the device is running with the wrong config or someone
// else's thing login, and its telemetry would be attributed to the wrong device.
// That fails rather than warns, on every authentication rather than only the
// first, because a re-provisioned thing record can start disagreeing later.
func (c *Client) finishAuth(resp *authResponse) (string, *thingRecord, error) {
	if resp.Record.ID == "" {
		return "", nil, errors.New("response contained no thing record")
	}
	if resp.Token == "" {
		return "", nil, errors.New("response contained no auth token")
	}
	if resp.Record.Code != c.code {
		return "", nil, fmt.Errorf("code mismatch: config has %q but the platform thing record has %q — fix the agent config or the thing record before starting", c.code, resp.Record.Code)
	}

	return resp.Token, &resp.Record, nil
}

// probeCredsRevision reads only the credential's `updated` timestamp.
//
// `fields` IS honoured on the record CRUD endpoints, unlike the auth responses,
// which makes this the one thing the agent can ask about its own credential
// without any key material coming back.
func (c *Client) probeCredsRevision(token, natsUserID string) (string, error) {
	req, err := c.newRequest(http.MethodGet,
		c.url("/api/collections/"+natsUsersCollection+"/records/"+natsUserID, "", "updated"), nil)
	if err != nil {
		return "", err
	}
	c.setAuth(req, token)

	var user natsUserRecord
	if err := c.do(req, &user); err != nil {
		return "", fmt.Errorf("failed to probe credential revision: %w", err)
	}

	return user.Updated, nil
}

// fetchCreds reads the full NATS identity, including the credential.
func (c *Client) fetchCreds(token, natsUserID string) (*natsUserRecord, error) {
	req, err := c.newRequest(http.MethodGet,
		c.url("/api/collections/"+natsUsersCollection+"/records/"+natsUserID, "", ""), nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req, token)

	var user natsUserRecord
	if err := c.do(req, &user); err != nil {
		return nil, fmt.Errorf("failed to read credential: %w", err)
	}
	if user.CredsFile == "" {
		return nil, errors.New("NATS identity has no credentials (does it have creds generated on the platform?)")
	}

	return &user, nil
}

// requestRotate calls the rotation route and returns the identity it acted on.
func (c *Client) requestRotate(token string) (string, error) {
	req, err := c.newRequest(http.MethodPost, c.url(rotatePath, "", ""), nil)
	if err != nil {
		return "", err
	}
	c.setAuth(req, token)

	var resp struct {
		Rotated  bool   `json:"rotated"`
		NATSUser string `json:"nats_user"`
	}
	if err := c.do(req, &resp); err != nil {
		return "", fmt.Errorf("rotation request failed: %w", err)
	}
	if !resp.Rotated {
		return "", errors.New("platform did not confirm the rotation")
	}

	return resp.NATSUser, nil
}

// url joins the configured base URL with a path and the only two query
// parameters the agent ever sends.
func (c *Client) url(path, expand, fields string) string {
	u := strings.TrimRight(c.auth.URL, "/") + path

	q := neturl.Values{}
	if expand != "" {
		q.Set("expand", expand)
	}
	if fields != "" {
		q.Set("fields", fields)
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	return u
}

func (c *Client) newRequest(method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return req, nil
}

// setAuth attaches the session token. PocketBase reads the raw token from the
// Authorization header; the "Bearer" prefix is optional there, so it is omitted.
func (c *Client) setAuth(req *http.Request, token string) {
	req.Header.Set("Authorization", token)
}

// do performs a request and decodes a JSON response into out.
//
// Success bodies are never logged and never folded into an error, because on the
// credential read one of them is a private key. The body of a non-2xx response
// is safe — the platform answers with an error document, not a record — and is
// included because it is what makes a 400 diagnosable.
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)) //nolint:errcheck // best-effort read for the error message
		return &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	return nil
}

// httpError carries the platform's status code so a caller can tell a rejected
// token from a server that is simply down.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("platform returned %d: %s", e.status, e.body)
}

func (c *Client) credsFileExists() bool {
	_, err := os.Stat(c.credsPath)
	return err == nil
}

// writeCreds writes the credential if it differs from what is already on disk,
// reporting whether anything changed. Comparing first keeps a 24h sync from
// rewriting an unchanged secret 365 times a year.
func (c *Client) writeCreds(creds string) (bool, error) {
	if existing, err := os.ReadFile(c.credsPath); err == nil && string(existing) == creds {
		return false, nil
	}

	if err := writeFileAtomic(c.credsPath, []byte(creds)); err != nil {
		return false, fmt.Errorf("failed to write credentials file: %w", err)
	}

	return true, nil
}

// writeFileAtomic writes data through a temp file in the same directory and
// renames it into place, so a crash mid-write cannot leave a truncated secret —
// an agent that cannot parse its own credential does not come back on its own.
//
// os.CreateTemp opens with 0600, which is what both secrets here want.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck // write error is the one that matters
		return fmt.Errorf("failed to write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to install %s: %w", path, err)
	}

	return nil
}
