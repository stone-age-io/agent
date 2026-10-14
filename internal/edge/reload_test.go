package edge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	njwt "github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/stone-age-io/agent/internal/natsd"
	"github.com/stone-age-io/agent/internal/platform"
)

// testTrust is an operator, its $SYS account and one org account, built the way
// pb-nats builds them, so a generated config can be loaded by a real server.
type testTrust struct {
	opJWT  string
	sysPub string
	sysJWT string
	akp    nkeys.KeyPair
	apub   string
	okp    nkeys.KeyPair
}

func newTestTrust(t *testing.T) *testTrust {
	t.Helper()
	okp, _ := nkeys.CreateOperator()
	opub, _ := okp.PublicKey()

	sysKP, _ := nkeys.CreateAccount()
	sysPub, _ := sysKP.PublicKey()
	sysJWT, err := njwt.NewAccountClaims(sysPub).Encode(okp)
	if err != nil {
		t.Fatalf("encode system account: %v", err)
	}

	oc := njwt.NewOperatorClaims(opub)
	oc.Name = "stone-age.io"
	oc.SystemAccount = sysPub
	opJWT, err := oc.Encode(okp)
	if err != nil {
		t.Fatalf("encode operator: %v", err)
	}

	akp, _ := nkeys.CreateAccount()
	apub, _ := akp.PublicKey()
	return &testTrust{opJWT: opJWT, sysPub: sysPub, sysJWT: sysJWT, akp: akp, apub: apub, okp: okp}
}

// accountJWT is the org account, revoking the given user public keys -- which
// is precisely what pb-nats does when a device is revoked: re-sign the account
// with the user on its revocation list.
func (tt *testTrust) accountJWT(t *testing.T, revoke ...string) string {
	t.Helper()
	ac := njwt.NewAccountClaims(tt.apub)
	ac.Name = "acme"
	ac.Limits.JetStreamLimits.DiskStorage = -1
	ac.Limits.JetStreamLimits.MemoryStorage = -1
	for _, pub := range revoke {
		ac.Revoke(pub)
	}
	jwt, err := ac.Encode(tt.okp)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	return jwt
}

// user mints a user in the org account and returns its public key and a
// nats.Option that authenticates as it.
func (tt *testTrust) user(t *testing.T) (string, nats.Option) {
	t.Helper()
	ukp, _ := nkeys.CreateUser()
	upub, _ := ukp.PublicKey()
	ujwt, err := njwt.NewUserClaims(upub).Encode(tt.akp)
	if err != nil {
		t.Fatalf("encode user: %v", err)
	}
	seed, _ := ukp.Seed()
	return upub, nats.UserJWTAndSeed(ujwt, string(seed))
}

func (tt *testTrust) leafConfig(accountJWT string) *platform.LeafConfig {
	return &platform.LeafConfig{
		Code:          "edge-test",
		Domain:        "edge-test",
		Creds:         "unused",
		AccountJWT:    accountJWT,
		AccountPub:    tt.apub,
		OperatorJWT:   tt.opJWT,
		SysAccountJWT: tt.sysJWT,
		SysAccountPub: tt.sysPub,
		// Nothing listens here: the remote just retries, quietly enough.
		HubLeafURL: "nats-leaf://127.0.0.1:1",
		HubDomain:  "hub",
	}
}

// runnable makes a generated config safe to start inside a test: a random
// client port on loopback and no monitoring port (8222 may be taken, and
// nothing here reads it). Everything that matters to these tests -- operator,
// MEMORY resolver, preload, the absolute store_dir in the test's temp dir -- is
// the generator's output untouched.
func runnable(t *testing.T, lc *platform.LeafConfig, dir, credsFile string) string {
	t.Helper()
	conf, err := leafConf(lc, dir, credsFile)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Replace(conf, `http: "127.0.0.1:8222"`, "", 1)
	if out == conf {
		t.Fatal("the generated config no longer has the line this test rewrites; update runnable()")
	}
	return out + "\nhost: \"127.0.0.1\"\nport: -1\n"
}

// A revocation reaches a running leaf through Reload, and this is the test
// that says what "reaches" means for a connected client.
//
// The whole leaf refresh rests on one nats-server behaviour: under `resolver:
// MEMORY`, reloading a config whose resolver_preload carries a re-signed account
// JWT re-applies that account's claims, revocations included. This pins it
// against the real server, through natsd.Server.Reload, with the real generator
// writing both configs.
//
// WHAT IT SHOWS: the revoked user's EXISTING connection is closed by the
// reload itself (nats-server closes it with "User Authentication Revoked"), and
// its next connect is refused. An unrevoked user in the same account stays
// connected and can still connect. So a site's revocation latency is the
// refresh interval, not "until the device happens to reconnect".
func TestLeafReloadAppliesRevocation(t *testing.T) {
	tt := newTestTrust(t)
	revokedPub, revokedAuth := tt.user(t)
	_, keptAuth := tt.user(t)

	dir := t.TempDir()
	credsFile := filepath.Join(dir, "edge.creds")
	confPath := filepath.Join(dir, LeafConfName)

	write := func(accountJWT string) {
		t.Helper()
		conf := runnable(t, tt.leafConfig(accountJWT), dir, credsFile)
		if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
			t.Fatalf("write conf: %v", err)
		}
	}

	write(tt.accountJWT(t))
	srv, err := natsd.Start(confPath, "", "nats.urls")
	if err != nil {
		t.Fatalf("start the leaf: %v", err)
	}
	t.Cleanup(srv.Stop)
	url := srv.ClientURL()

	closed := make(chan struct{})
	asyncErrs := make(chan error, 8) // a channel, not a variable: the handler runs on nats.go's goroutine
	revoked, err := nats.Connect(url, revokedAuth,
		nats.NoReconnect(),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			select {
			case asyncErrs <- err:
			default:
			}
		}),
		nats.ClosedHandler(func(*nats.Conn) { close(closed) }),
	)
	if err != nil {
		t.Fatalf("the soon-to-be-revoked user could not connect before revocation: %v", err)
	}
	defer revoked.Close()

	kept, err := nats.Connect(url, keptAuth, nats.NoReconnect())
	if err != nil {
		t.Fatalf("the unrevoked user could not connect: %v", err)
	}
	defer kept.Close()

	// The refresh's job: rewrite the preload with the re-signed account, then
	// reload. Nothing else in the file changes.
	write(tt.accountJWT(t, revokedPub))
	if err := srv.Reload(); err != nil {
		t.Fatalf("Reload refused a changed resolver_preload: %v", err)
	}

	select {
	case <-closed:
		var why error
		select {
		case why = <-asyncErrs:
		default:
		}
		t.Logf("revoked user's live connection closed by the reload (async error: %v)", why)
	case <-time.After(5 * time.Second):
		t.Errorf("the revoked user's existing connection survived the reload (status %v)", revoked.Status())
	}

	if nc, err := nats.Connect(url, revokedAuth, nats.NoReconnect()); err == nil {
		nc.Close()
		t.Error("the revoked user could connect again after the reload")
	} else {
		t.Logf("revoked user's reconnect refused: %v", err)
	}

	if err := kept.Flush(); err != nil {
		t.Errorf("the unrevoked user's connection did not survive the reload: %v", err)
	}
	if nc, err := nats.Connect(url, keptAuth, nats.NoReconnect()); err != nil {
		t.Errorf("the unrevoked user could not connect after the reload: %v", err)
	} else {
		nc.Close()
	}
}

// An upgrade rewrites a conf an older agent generated, whose store_dir was the
// relative "./jetstream", with an absolute one -- and nats-server refuses to
// reload a running server onto a different store_dir ("config reload not
// supported for jetstream storage directory", server/reload.go). The refusal
// is all-or-nothing: the new preload is not applied either. This pins that the
// refusal comes back as Reload's error, which is what the refresh reports, so
// the upgrade's one required restart is said rather than silent.
func TestLeafReloadRefusesAStoreDirChange(t *testing.T) {
	tt := newTestTrust(t)
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "edge.creds")
	confPath := filepath.Join(dir, LeafConfName)

	// The old conf stands in for the relative one: any other store_dir is the
	// same difference to the server, without a ./jetstream beside the package.
	current := runnable(t, tt.leafConfig(tt.accountJWT(t)), dir, credsFile)
	oldStore := fmt.Sprintf("store_dir: %q", filepath.Join(dir, LeafStoreDirName))
	older := strings.Replace(current, oldStore, fmt.Sprintf("store_dir: %q", filepath.Join(dir, "old-cwd", "jetstream")), 1)
	if older == current {
		t.Fatal("the generated config has no store_dir line to rewrite")
	}
	if err := os.WriteFile(confPath, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := natsd.Start(confPath, "", "nats.urls")
	if err != nil {
		t.Fatalf("start the leaf: %v", err)
	}
	t.Cleanup(srv.Stop)

	if err := os.WriteFile(confPath, []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	err = srv.Reload()
	if err == nil || !strings.Contains(err.Error(), "jetstream storage directory") {
		t.Fatalf("Reload onto a different store_dir = %v, want nats-server's refusal", err)
	}
	t.Logf("reload refused as expected: %v", err)
}
