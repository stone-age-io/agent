package edge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	njwt "github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nkeys"

	"github.com/stone-age-io/agent/internal/platform"
)

func TestBuildLeafConf(t *testing.T) {
	conf := buildLeafConf(leafConfParams{
		OperatorJWT:   "OPJWT",
		AccountPub:    "ACCOUNTPUB",
		AccountJWT:    "ACCJWT",
		SysAccountPub: "SYSPUB",
		SysAccountJWT: "SYSJWT",
		Domain:        "edge-warehouse",
		HubLeafURL:    "nats-leaf://hub.example.com:7422",
		CredsFile:     "/etc/agent/edge.creds",
		StoreDir:      "/etc/agent/jetstream",
		PidFile:       "/etc/agent/nats-leaf.pid",
	})
	// Each directive a stock nats-server needs to come up as an offline,
	// operator-mode leaf bound to one preloaded account.
	wants := []string{
		`server_name: "edge-warehouse"`,
		`domain: "edge-warehouse"`, // JetStream domain isolates the edge from the hub
		`operator: "OPJWT"`,        // operator trust anchor
		`resolver: MEMORY`,         // no dynamic account resolver
		`ACCOUNTPUB: "ACCJWT"`,     // resolver_preload: the org account
		`SYSPUB: "SYSJWT"`,         // ...and the $SYS account the operator JWT names
		`url: "nats-leaf://hub.example.com:7422"`,
		`account: "ACCOUNTPUB"`, // required on every remote in operator mode
		`credentials: "/etc/agent/edge.creds"`,
		`store_dir: "/etc/agent/jetstream"`,
		`http: "127.0.0.1:8222"`,               // localhost monitoring only
		`pid_file: "/etc/agent/nats-leaf.pid"`, // what the agent SIGHUPs a separate leaf through
	}
	for _, w := range wants {
		if !strings.Contains(conf, w) {
			t.Errorf("nats-leaf.conf missing %q\n---\n%s", w, conf)
		}
	}
}

// nats-server resolves relative paths against its working directory, not the
// conf's, so every path leafConf writes is absolute and inside the conf's own
// directory -- even when the directory it is given is relative, which is what
// a relative nats.auth.creds_file produces.
func TestLeafConfPathsAreAbsoluteIntoTheConfDir(t *testing.T) {
	t.Chdir(t.TempDir())
	confDir, err := filepath.Abs("gw")
	if err != nil {
		t.Fatal(err)
	}
	conf, err := leafConf(&platform.LeafConfig{AccountPub: "ACCOUNTPUB"}, "gw", filepath.Join("elsewhere", "gw.creds"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		fmt.Sprintf("credentials: %q", filepath.Join(confDir, "gw.creds")),
		fmt.Sprintf("store_dir: %q", filepath.Join(confDir, LeafStoreDirName)),
		fmt.Sprintf("pid_file: %q", filepath.Join(confDir, LeafPidName)),
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("nats-leaf.conf missing %s\n---\n%s", want, conf)
		}
	}
}

// The string assertions above cannot tell whether nats-server will actually
// accept the result, and for a long time it would not have: the config was
// missing the remote's `account` key and the $SYS account preload, so every
// generated nats-leaf.conf was rejected before the server started. Both bugs are
// invisible to a Contains() check and neither needs a port or a network — the
// server validates the whole config while constructing itself.
//
// So: build a real operator/$SYS/account/user set, run the real generator, and
// hand the output to the real nats-server config loader.
func TestBuildLeafConfIsAcceptedByNATSServer(t *testing.T) {
	okp, err := nkeys.CreateOperator()
	if err != nil {
		t.Fatalf("create operator key: %v", err)
	}
	opub, _ := okp.PublicKey()
	// $SYS, as pb-nats seeds it centrally.
	sysKP, _ := nkeys.CreateAccount()
	sysPub, _ := sysKP.PublicKey()
	sysJWT, err := njwt.NewAccountClaims(sysPub).Encode(okp)
	if err != nil {
		t.Fatalf("encode system account: %v", err)
	}
	// pb-nats records the system account inside the operator JWT
	// (internal/jwt/generator.go). That is what forces the preload below.
	oc := njwt.NewOperatorClaims(opub)
	oc.Name = "stone-age.io"
	oc.SystemAccount = sysPub
	opJWT, err := oc.Encode(okp)
	if err != nil {
		t.Fatalf("encode operator: %v", err)
	}
	akp, _ := nkeys.CreateAccount()
	apub, _ := akp.PublicKey()
	ac := njwt.NewAccountClaims(apub)
	ac.Name = "acme"
	ac.Limits.JetStreamLimits.DiskStorage = -1
	ac.Limits.JetStreamLimits.MemoryStorage = -1
	accJWT, err := ac.Encode(okp)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	// Through leafConf, the path the agent takes, so the paths are the real
	// absolute ones in a real directory, written through %q -- on Windows that
	// means backslashes the config lexer has to unescape, which is exactly the
	// kind of thing only the real parser can vouch for.
	dir := t.TempDir()
	conf, err := leafConf(&platform.LeafConfig{
		OperatorJWT:   opJWT,
		AccountPub:    apub,
		AccountJWT:    accJWT,
		SysAccountPub: sysPub,
		SysAccountJWT: sysJWT,
		Domain:        "edge-warehouse",
		HubLeafURL:    "nats-leaf://hub.example.com:7422",
	}, dir, filepath.Join(dir, "edge.creds"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, LeafConfName)
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	opts, err := natsserver.ProcessConfigFile(path)
	if err != nil {
		t.Fatalf("nats-server rejected the generated config: %v\n---\n%s", err, conf)
	}
	// Every path the server will open resolves into the conf's directory.
	if len(opts.LeafNode.Remotes) != 1 {
		t.Fatalf("parsed %d leaf remotes, want 1", len(opts.LeafNode.Remotes))
	}
	for _, p := range []struct{ name, got, want string }{
		{"pid_file", opts.PidFile, filepath.Join(dir, LeafPidName)},
		{"store_dir", opts.StoreDir, filepath.Join(dir, LeafStoreDirName)},
		{"credentials", opts.LeafNode.Remotes[0].Credentials, filepath.Join(dir, "edge.creds")},
	} {
		if p.got != p.want {
			t.Errorf("%s parsed as %q, want %q", p.name, p.got, p.want)
		}
	}
	// NewServer is where operator-mode validation runs (remote account keys,
	// system account resolution). It binds no ports, so this stays hermetic.
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("nats-server could not build a server from the generated config: %v\n---\n%s", err, conf)
	}
	srv.Shutdown()
}

// fetchBootstrap must hit exactly one endpoint. `config` used to read nats_users
// and nats_accounts through the CRUD API; the leaf-node identity now has no grant
// on either, so a regression to a collection read would 404 in the field.
