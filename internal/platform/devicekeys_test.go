package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	natsjwt "github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	nebulacert "github.com/slackhq/nebula/cert"
	"go.uber.org/zap"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/curve25519"

	"github.com/stone-age-io/agent/internal/config"
)

// deviceFake is a platform whose NATS identity and Nebula host hold
// device-held keys. It behaves as pb-nats and pb-nebula do: a key arriving
// signs a JWT or a certificate, a second different key is a 409, and revoke
// empties the row.
type deviceFake struct {
	t       *testing.T
	mu      sync.Mutex
	account nkeys.KeyPair

	natsPub     string
	natsJWT     string
	natsUpdated int

	nebulaPub     string
	nebulaConfig  string
	nebulaUpdated int

	enrollments []map[string]string
}

func newDeviceFake(t *testing.T) *deviceFake {
	t.Helper()
	account, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	return &deviceFake{t: t, account: account, natsUpdated: 1, nebulaUpdated: 1}
}

// signNATS issues a user JWT for pub, as pb-nats does when a key arrives.
func (f *deviceFake) signNATS(pub string) string {
	claims := natsjwt.NewUserClaims(pub)
	token, err := claims.Encode(f.account)
	if err != nil {
		f.t.Fatalf("sign user JWT: %v", err)
	}
	return token
}

// revokeNATS is an operator pressing Revoke: pb-nats empties the row.
func (f *deviceFake) revokeNATS() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.natsPub, f.natsJWT = "", ""
	f.natsUpdated++
}

func (f *deviceFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	natsRecord := func() map[string]any {
		return map[string]any{
			"creds_file": "", "device_key": true, "public_key": f.natsPub, "jwt": f.natsJWT,
			"updated": strings.Repeat("u", f.natsUpdated),
		}
	}

	switch r.URL.Path {
	case "/api/collections/things/auth-with-password", "/api/collections/things/auth-refresh":
		record := map[string]any{"id": "thing123", "code": "server-01", "nats_user": "natsuser123", "nebula_host": "host123"}
		if strings.Contains(r.URL.Query().Get("expand"), "nats_user") {
			record["expand"] = map[string]any{"nats_user": natsRecord()}
		}
		json.NewEncoder(w).Encode(map[string]any{"token": freshToken, "record": record})

	case "/api/collections/nats_users/records/natsuser123":
		if r.URL.Query().Get("fields") == "updated" {
			json.NewEncoder(w).Encode(map[string]string{"updated": strings.Repeat("u", f.natsUpdated)})
			return
		}
		json.NewEncoder(w).Encode(natsRecord())

	case "/api/collections/nebula_hosts/records/host123":
		json.NewEncoder(w).Encode(map[string]any{
			"config_yaml": f.nebulaConfig, "device_key": true, "public_key": f.nebulaPub,
			"updated": strings.Repeat("n", f.nebulaUpdated),
		})

	case keysPath:
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.enrollments = append(f.enrollments, body)
		if k := body["nats_public_key"]; k != "" {
			if f.natsPub != "" && f.natsPub != k {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"message":"a different key is already enrolled"}`))
				return
			}
			f.natsPub, f.natsJWT = k, f.signNATS(k)
			f.natsUpdated++
		}
		if k := body["nebula_public_key"]; k != "" {
			f.nebulaPub = k
			f.nebulaConfig = "pki:\n  ca: CA-PEM\n  cert: CERT-PEM\n  key: /etc/nebula/host.key\nlighthouse:\n  am_lighthouse: false\n"
			f.nebulaUpdated++
		}
		json.NewEncoder(w).Encode(map[string]any{"enrolled": map[string]string{}})

	case leafConfigPath:
		if f.natsPub == "" {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"message":"not enrolled: POST /api/me/keys"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"code": "server-01", "domain": "server-01", "creds": "", "jwt": f.natsJWT,
			"account_jwt": "ACCT-JWT", "account_pub": "ACCT-PUB", "operator_jwt": "OP-JWT",
			"sys_account_jwt": "SYS-JWT", "sys_account_pub": "SYS-PUB",
			"hub_leaf_url": "nats-leaf://hub:7422", "hub_domain": "hub",
		})

	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func (f *deviceFake) enrolled() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.enrollments)
}

// deviceClient is testClient with a Nebula cache file, so the Nebula key has
// somewhere to live.
func deviceClient(t *testing.T, url string) *Client {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Code: "server-01",
		Platform: config.PlatformConfig{
			URL: url, Identity: "thing@example.com", PasswordEnv: passwordEnv,
			SessionFile: filepath.Join(dir, "platform-session.json"),
		},
		NATS:   config.NATSConfig{Auth: config.AuthConfig{Type: "platform", CredsFile: filepath.Join(dir, "device.creds")}},
		Nebula: config.NebulaConfig{CacheFile: filepath.Join(dir, "nebula-cache.yaml")},
	}
	t.Setenv(passwordEnv, "secret")
	return NewClient(cfg, zap.NewNop())
}

// seedPublicKey reads the local seed and returns its public key.
func seedPublicKey(t *testing.T, c *Client) string {
	t.Helper()
	seed, err := os.ReadFile(c.natsSeedPath())
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	kp, err := nkeys.FromSeed(bytes.TrimSpace(seed))
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	pub, _ := kp.PublicKey()
	return pub
}

// credsJWTSubject returns the key the JWT in the .creds file was signed for.
func credsJWTSubject(t *testing.T, c *Client) string {
	t.Helper()
	raw, err := os.ReadFile(c.credsPath)
	if err != nil {
		t.Fatalf("read creds: %v", err)
	}
	token, err := natsjwt.ParseDecoratedJWT(raw)
	if err != nil {
		t.Fatalf("parse creds JWT: %v", err)
	}
	claims, err := natsjwt.DecodeUserClaims(token)
	if err != nil {
		t.Fatalf("decode creds JWT: %v", err)
	}
	return claims.Subject
}

// First boot: the platform has no key, so the agent makes one, writes the
// seed, enrolls it, and builds the .creds from the JWT the platform signed.
func TestDeviceKeyFirstBootEnrolls(t *testing.T) {
	fake := newDeviceFake(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := deviceClient(t, srv.URL)

	if err := c.EnsureCredentials(); err != nil {
		t.Fatalf("EnsureCredentials: %v", err)
	}
	if fake.enrolled() != 1 {
		t.Fatalf("enrollments = %d, want 1", fake.enrolled())
	}
	pub := seedPublicKey(t, c)
	if pub != fake.natsPub {
		t.Errorf("enrolled %s, want the seed's key %s", fake.natsPub, pub)
	}
	if got := credsJWTSubject(t, c); got != pub {
		t.Errorf(".creds carries a JWT for %s, want %s", got, pub)
	}
	if info, err := os.Stat(c.natsSeedPath()); err == nil && info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("seed mode = %v, want 0600", info.Mode().Perm())
	}

	// The next sync finds the same key and enrolls nothing.
	if _, err := c.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if fake.enrolled() != 1 {
		t.Errorf("a sync with the key already enrolled enrolled again (%d)", fake.enrolled())
	}
}

// After a revoke the platform has no key. The agent makes a NEW one: the old
// key is on the revocation list and sending it again would be refused.
func TestDeviceKeyRevokedReenrollsWithANewKey(t *testing.T) {
	fake := newDeviceFake(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := deviceClient(t, srv.URL)

	if err := c.EnsureCredentials(); err != nil {
		t.Fatalf("EnsureCredentials: %v", err)
	}
	old := seedPublicKey(t, c)

	fake.revokeNATS()
	changed, err := c.Sync()
	if err != nil {
		t.Fatalf("Sync after revoke: %v", err)
	}
	if !changed {
		t.Error("the .creds did not change after re-enrolling")
	}
	fresh := seedPublicKey(t, c)
	if fresh == old {
		t.Fatal("the agent sent the revoked key again")
	}
	if got := credsJWTSubject(t, c); got != fresh {
		t.Errorf(".creds carries a JWT for %s, want the new key %s", got, fresh)
	}
}

// An operator pasted a different key: the agent stops and says so, and
// enrolls nothing over it.
func TestDeviceKeyForeignKeyStops(t *testing.T) {
	fake := newDeviceFake(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := deviceClient(t, srv.URL)

	if err := c.EnsureCredentials(); err != nil {
		t.Fatalf("EnsureCredentials: %v", err)
	}

	other, _ := nkeys.CreateUser()
	otherPub, _ := other.PublicKey()
	fake.mu.Lock()
	fake.natsPub, fake.natsJWT = otherPub, fake.signNATS(otherPub)
	fake.natsUpdated++
	fake.mu.Unlock()

	_, err := c.Sync()
	if !errors.Is(err, ErrForeignKey) {
		t.Fatalf("Sync with a foreign key: %v, want ErrForeignKey", err)
	}
	if fake.enrolled() != 1 {
		t.Errorf("the agent enrolled over an operator's key (%d enrollments)", fake.enrolled())
	}
}

// The leaf config of a device that never ran the credential sync: the
// platform answers 409, the agent enrolls and asks again, and builds the
// leaf's .creds from the JWT.
func TestDeviceKeyLeafConfigEnrollsOn409(t *testing.T) {
	fake := newDeviceFake(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := deviceClient(t, srv.URL)

	lc, err := c.LeafConfig()
	if err != nil {
		t.Fatalf("LeafConfig: %v", err)
	}
	if fake.enrolled() != 1 {
		t.Fatalf("enrollments = %d, want 1", fake.enrolled())
	}
	token, err := natsjwt.ParseDecoratedJWT([]byte(lc.Creds))
	if err != nil {
		t.Fatalf("leaf creds do not parse: %v", err)
	}
	if token != fake.natsJWT {
		t.Error("the leaf .creds does not carry the platform's JWT")
	}
	if _, err := natsjwt.ParseDecoratedNKey([]byte(lc.Creds)); err != nil {
		t.Errorf("the leaf .creds carries no usable seed: %v", err)
	}
}

// The Nebula host: the agent makes an X25519 pair, enrolls the public key,
// and points pki.key at its own key file instead of the platform's fixed path.
func TestDeviceKeyNebulaEnrollsAndPointsPkiKeyAtTheLocalKey(t *testing.T) {
	fake := newDeviceFake(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := deviceClient(t, srv.URL)

	fetched, err := c.NebulaSource().Fetch("")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var doc struct {
		PKI struct {
			Key string `yaml:"key"`
		} `yaml:"pki"`
	}
	if err := yaml.Unmarshal([]byte(fetched.YAML), &doc); err != nil {
		t.Fatalf("parse fetched config: %v", err)
	}
	if doc.PKI.Key != c.nebulaKeyPath {
		t.Errorf("pki.key = %q, want the local key %q", doc.PKI.Key, c.nebulaKeyPath)
	}

	keyPEM, err := os.ReadFile(c.nebulaKeyPath)
	if err != nil {
		t.Fatalf("read the Nebula key: %v", err)
	}
	priv, _, _, err := nebulacert.UnmarshalPrivateKeyFromPEM(keyPEM)
	if err != nil {
		t.Fatalf("the Nebula key is not a private key PEM: %v", err)
	}
	pub, _ := curve25519.X25519(priv, curve25519.Basepoint)
	enrolled, _, _, err := nebulacert.UnmarshalPublicKeyFromPEM([]byte(fake.nebulaPub))
	if err != nil {
		t.Fatalf("the enrolled key is not a public key PEM: %v", err)
	}
	if !bytes.Equal(pub, enrolled) {
		t.Error("the enrolled Nebula key is not the local key's public half")
	}
}

// The credential sync used to save a fresh session holding only its own two
// fields, so every sync dropped the Nebula revision (the next Nebula sync then
// re-read its private key off the network) and the hub domain an islanded
// edge needs. Every session writer is a read-modify-write.
func TestCredentialSyncKeepsTheRestOfTheSession(t *testing.T) {
	fake := newFakePlatform(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := testClient(t, srv.URL)

	writeSession(t, c, session{Token: testToken, NebulaRevision: "nebula-rev", HubDomain: "hub"})
	if _, err := c.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	s := readSession(t, c)
	if s.NebulaRevision != "nebula-rev" || s.HubDomain != "hub" {
		t.Errorf("after a credential sync the session holds nebula_revision=%q hub_domain=%q, want both kept",
			s.NebulaRevision, s.HubDomain)
	}
}
