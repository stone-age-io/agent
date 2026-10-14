package edge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/stone-age-io/agent/internal/config"
	"github.com/stone-age-io/agent/internal/health"
	"github.com/stone-age-io/agent/internal/platform"
)

const refreshPasswordEnv = "TEST_EDGE_PLATFORM_PASSWORD"

// fakeLeafPlatform serves the routes the refresh touches: a password login, the
// account JWT probe and /api/me/leaf-config. What it serves can be changed
// between calls, which is the whole of what a revocation looks like from here.
// noProbe makes it a platform that predates the probe: 404, like PocketBase.
type fakeLeafPlatform struct {
	mu      sync.Mutex
	lc      platform.LeafConfig
	noProbe bool
	reads   int // leaf-config reads: each one carries the .creds
	probes  int
}

func (f *fakeLeafPlatform) set(mutate func(*platform.LeafConfig)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutate(&f.lc)
}

// counts returns (leaf-config reads, probes) so far.
func (f *fakeLeafPlatform) counts() (reads, probes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads, f.probes
}

func (f *fakeLeafPlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/collections/things/auth-with-password", "/api/collections/things/auth-refresh":
		json.NewEncoder(w).Encode(map[string]any{
			"token":  "token-1",
			"record": map[string]any{"id": "thing1", "code": "gw-01"},
		})
	case "/api/me/account-jwt":
		if f.noProbe {
			http.NotFound(w, r)
			return
		}
		f.probes++
		json.NewEncoder(w).Encode(map[string]string{"account_jwt": f.lc.AccountJWT, "account_pub": f.lc.AccountPub})
	case "/api/me/leaf-config":
		f.reads++
		json.NewEncoder(w).Encode(f.lc)
	default:
		http.NotFound(w, r)
	}
}

func newFakeLeafPlatform(t *testing.T) (*fakeLeafPlatform, *platform.Client, string) {
	t.Helper()
	t.Setenv(refreshPasswordEnv, "secret")

	fake := &fakeLeafPlatform{lc: platform.LeafConfig{
		Code:          "gw-01",
		Domain:        "gw-01",
		Creds:         "CREDS-1",
		AccountJWT:    "ACCJWT-1",
		AccountPub:    "ACCOUNTPUB",
		OperatorJWT:   "OPJWT",
		SysAccountJWT: "SYSJWT",
		SysAccountPub: "SYSPUB",
		HubLeafURL:    "nats-leaf://hub.example.com:7422",
		HubDomain:     "hub",
	}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	credsFile := filepath.Join(dir, "gw.creds")
	client := platform.NewClient(&config.Config{
		Code: "gw-01",
		Platform: config.PlatformConfig{
			URL:         srv.URL,
			Identity:    "gw@example.com",
			PasswordEnv: refreshPasswordEnv,
			SessionFile: filepath.Join(dir, "platform-session.json"),
		},
		NATS: config.NATSConfig{Auth: config.AuthConfig{Type: "platform", CredsFile: credsFile}},
	}, zap.NewNop())
	return fake, client, credsFile
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The probe decides whether the full leaf config -- which carries the .creds --
// is read at all. The first run after start reads it once whatever the probe
// says; after that an unchanged probe reads nothing more, and a re-signed
// account reads it exactly once, writes the conf once and reloads once. The
// reload is counted through the injected function, which is the only seam.
func TestLeafRefreshWritesAndReloadsOnlyOnChange(t *testing.T) {
	fake, client, credsFile := newFakeLeafPlatform(t)

	// The state `agent -leaf-config` leaves behind, through its own code path.
	lc, err := client.LeafConfig()
	if err != nil {
		t.Fatalf("LeafConfig: %v", err)
	}
	confPath, credsPath, err := WriteLeafConfig(lc, filepath.Dir(credsFile), credsFile)
	if err != nil {
		t.Fatalf("WriteLeafConfig: %v", err)
	}
	original := readFile(t, confPath)

	reloads := 0
	r := NewLeafRefresher(credsFile, client, func() error { reloads++; return nil }, zap.NewNop())
	if r.ConfPath() != confPath {
		t.Fatalf("refresher watches %s, -leaf-config wrote %s", r.ConfPath(), confPath)
	}

	// expect checks the platform's counters since the bootstrap read.
	expect := func(step string, wantReads, wantProbes int) {
		t.Helper()
		reads, probes := fake.counts()
		if reads-1 != wantReads || probes != wantProbes {
			t.Fatalf("%s: %d leaf-config reads and %d probes, want %d and %d", step, reads-1, probes, wantReads, wantProbes)
		}
	}

	// First run after start: nothing applied yet, so one full read. The bytes
	// -leaf-config wrote are the bytes the refresh would write, so nothing is
	// written and nothing reloaded.
	changed, reloaded, err := r.Refresh()
	if err != nil || changed || reloaded || reloads != 0 {
		t.Fatalf("first refresh = (%v, %v, %v), %d reloads; want no change and no reload", changed, reloaded, err, reloads)
	}
	if readFile(t, confPath) != original {
		t.Fatal("an unchanged refresh rewrote the conf")
	}
	expect("first run", 1, 1)

	// Unchanged probe: no leaf-config read at all.
	if changed, _, err := r.Refresh(); err != nil || changed {
		t.Fatalf("unchanged refresh = (%v, %v), want nothing", changed, err)
	}
	expect("unchanged probe", 1, 2)

	// A revocation: the platform re-signs the account. The creds it serves
	// move too, and must NOT be written -- that is the credential sync's job.
	fake.set(func(lc *platform.LeafConfig) {
		lc.AccountJWT = "ACCJWT-2"
		lc.Creds = "CREDS-2"
	})
	changed, reloaded, err = r.Refresh()
	if err != nil || !changed || !reloaded || reloads != 1 {
		t.Fatalf("changed refresh = (%v, %v, %v), %d reloads; want one write and one reload", changed, reloaded, err, reloads)
	}
	expect("changed probe", 2, 3)
	if conf := readFile(t, confPath); !strings.Contains(conf, `ACCOUNTPUB: "ACCJWT-2"`) {
		t.Errorf("rewritten conf does not preload the new account JWT:\n%s", conf)
	}
	if creds := readFile(t, credsPath); creds != "CREDS-1" {
		t.Errorf("the refresh wrote the creds file (%q); it must leave it to the credential sync", creds)
	}
	if _, jwt := r.fetched(); jwt != "ACCJWT-2" {
		t.Errorf("leaf_preload compares against %q, want the new JWT", jwt)
	}

	// And then it is unchanged again, and reads nothing.
	if changed, _, err := r.Refresh(); err != nil || changed || reloads != 1 {
		t.Errorf("refresh after a change = (%v, %v), %d reloads; want nothing", changed, err, reloads)
	}
	expect("unchanged again", 2, 4)
}

// A platform that predates the probe answers it 404. The refresh then reads
// the full leaf config every run, as it did before the probe existed, and says
// so once rather than every five minutes.
func TestLeafRefreshFallsBackWithoutTheProbeRoute(t *testing.T) {
	fake, client, credsFile := newFakeLeafPlatform(t)
	fake.noProbe = true
	lc, _ := client.LeafConfig()
	if _, _, err := WriteLeafConfig(lc, filepath.Dir(credsFile), credsFile); err != nil {
		t.Fatal(err)
	}

	core, logs := observer.New(zap.WarnLevel)
	reloads := 0
	r := NewLeafRefresher(credsFile, client, func() error { reloads++; return nil }, zap.New(core))

	for i := 0; i < 2; i++ {
		if changed, _, err := r.Refresh(); err != nil || changed {
			t.Fatalf("refresh %d = (%v, %v), want nothing changed", i, changed, err)
		}
	}
	fake.set(func(lc *platform.LeafConfig) { lc.AccountJWT = "ACCJWT-2" })
	if changed, reloaded, err := r.Refresh(); err != nil || !changed || !reloaded {
		t.Fatalf("refresh after a change = (%v, %v, %v), want a write and a reload", changed, reloaded, err)
	}

	if reads, _ := fake.counts(); reads != 4 {
		t.Errorf("%d leaf-config reads, want 4 (one bootstrap, then every one of three refreshes)", reads)
	}
	if n := logs.FilterMessageSnippet("predates the account JWT probe").Len(); n != 1 || logs.Len() != 1 {
		t.Errorf("%d probe warnings (%d warnings in all), want exactly one", n, logs.Len())
	}
}

// Any other probe failure is a failed refresh like any other: no full read.
func TestLeafRefreshStopsOnAProbeError(t *testing.T) {
	fake, client, credsFile := newFakeLeafPlatform(t)
	r := NewLeafRefresher(credsFile, client, func() error { return nil }, zap.NewNop())
	fake.set(func(lc *platform.LeafConfig) { lc.AccountJWT = "" }) // an incomplete probe answer

	if _, _, err := r.Refresh(); err == nil || !strings.Contains(err.Error(), "account_jwt") {
		t.Errorf("err = %v, want the incomplete probe named", err)
	}
	if reads, _ := fake.counts(); reads != 0 {
		t.Errorf("%d leaf-config reads after a failed probe, want 0", reads)
	}
}

// A new operator cannot be applied by reload, so the refresh does not try: it
// leaves the file alone, does not reload, and says what to do instead.
func TestLeafRefreshRefusesAnOperatorChange(t *testing.T) {
	fake, client, credsFile := newFakeLeafPlatform(t)
	lc, _ := client.LeafConfig()
	confPath, _, err := WriteLeafConfig(lc, filepath.Dir(credsFile), credsFile)
	if err != nil {
		t.Fatal(err)
	}
	original := readFile(t, confPath)

	for name, mutate := range map[string]func(*platform.LeafConfig){
		"operator": func(lc *platform.LeafConfig) { lc.OperatorJWT = "OPJWT-NEW" },
		"$SYS":     func(lc *platform.LeafConfig) { lc.SysAccountPub = "SYSPUB-NEW" },
	} {
		t.Run(name, func(t *testing.T) {
			fake.set(func(lc *platform.LeafConfig) { lc.OperatorJWT, lc.SysAccountPub = "OPJWT", "SYSPUB" })
			fake.set(mutate)
			fake.set(func(lc *platform.LeafConfig) { lc.AccountJWT = "ACCJWT-2" })

			reloads := 0
			r := NewLeafRefresher(credsFile, client, func() error { reloads++; return nil }, zap.NewNop())
			changed, reloaded, err := r.Refresh()
			if err == nil || !strings.Contains(err.Error(), "agent -leaf-config") {
				t.Errorf("err = %v, want it to say to re-run agent -leaf-config", err)
			}
			if changed || reloaded || reloads != 0 {
				t.Errorf("refresh = (%v, %v), %d reloads; want the file left alone", changed, reloaded, reloads)
			}
			if readFile(t, confPath) != original {
				t.Error("the conf was rewritten for a change a reload cannot apply")
			}
		})
	}
}

// Rewritten but not reloaded is reported as exactly that, because it is the
// state where the revocation is on disk and not in force.
func TestLeafRefreshReportsAFailedReload(t *testing.T) {
	fake, client, credsFile := newFakeLeafPlatform(t)
	lc, _ := client.LeafConfig()
	if _, _, err := WriteLeafConfig(lc, filepath.Dir(credsFile), credsFile); err != nil {
		t.Fatal(err)
	}
	fake.set(func(lc *platform.LeafConfig) { lc.AccountJWT = "ACCJWT-2" })

	r := NewLeafRefresher(credsFile, client, func() error { return errors.New("no pid file") }, zap.NewNop())
	changed, reloaded, err := r.Refresh()
	if !changed || reloaded || err == nil || !strings.Contains(err.Error(), "no pid file") {
		t.Errorf("refresh = (%v, %v, %v), want changed, not reloaded, and the reload's error", changed, reloaded, err)
	}
}

func TestLeafPreloadCheck(t *testing.T) {
	loaded := "ACCJWT-1"
	monitor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accountz" || r.URL.Query().Get("acc") != "ACCOUNTPUB" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"account_detail": map[string]any{"account_name": "ACCOUNTPUB", "jwt": loaded},
		})
	}))
	defer monitor.Close()

	fetchedOnce := func(jwt string) *LeafRefresher {
		r := &LeafRefresher{}
		r.accountPub, r.fetchedJWT = "ACCOUNTPUB", jwt
		return r
	}

	tests := []struct {
		name string
		r    *LeafRefresher
		base string
		want health.State
	}{
		{"inactive", nil, monitor.URL, health.StateSkipped},
		{"nothing fetched yet", &LeafRefresher{}, monitor.URL, health.StateSkipped},
		{"leaf enforces the platform's JWT", fetchedOnce("ACCJWT-1"), monitor.URL, health.StateOK},
		{"leaf is behind", fetchedOnce("ACCJWT-2"), monitor.URL, health.StateWarn},
		{"monitor unreachable", fetchedOnce("ACCJWT-1"), "http://127.0.0.1:1", health.StateSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkLeafPreload(context.Background(), tt.base, tt.r); got.State != tt.want {
				t.Errorf("state = %s (%s), want %s", got.State, got.Detail, tt.want)
			}
		})
	}
}
