package edge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	natsconf "github.com/nats-io/nats-server/v2/conf"
	"go.uber.org/zap"

	"github.com/stone-age-io/agent/internal/health"
	"github.com/stone-age-io/agent/internal/platform"
	"github.com/stone-age-io/agent/internal/utils"
)

// LeafRefresher keeps the account JWT preloaded into a generated nats-leaf.conf
// in step with the one the platform serves.
//
// WHY THIS EXISTS. A leaf runs `resolver: MEMORY`: the only account JWT it will
// ever know is the one in resolver_preload, written once by `agent -leaf-config`.
// NATS user revocations live inside that JWT -- pb-nats re-signs the account
// with the revocation list -- so without this a device revoked at the hub kept
// authenticating at its site's leaf until somebody re-ran the bootstrap by hand.
// The platform serves the current JWT; this re-reads it, rewrites the file when
// it moved, and has the leaf reload it.
//
// It is NOT the credential sync. The leaf config carries this thing's .creds
// too, and this ignores them: keeping the credential current is the creds sync's
// job, and two writers for one secret would race each other.
//
// TWO READS, BECAUSE THE FULL ONE CARRIES A SECRET. Each run first asks the
// probe route (/api/me/account-jwt) for the account JWT alone -- public trust
// material, nothing else. Only when that differs from the JWT the last full pass
// applied, or nothing has been applied since start, does it read the full leaf
// config, with its .creds, and do the real work. A platform that predates the
// probe answers 404, and then every run reads the full config, as it did before
// the probe existed.
//
// The real work decides change by comparing the file the builder would write
// with the file on disk, byte for byte: the bytes are the thing that actually
// has to be right. The first pass after start always gets that far, which is
// what lets an upgraded agent rewrite a conf an older one generated.
type LeafRefresher struct {
	confPath  string
	outputDir string
	credsFile string
	source    LeafSource
	reload    func() error
	logger    *zap.Logger

	// run serialises whole refreshes: the schedule and cmd.leaf_refresh can
	// arrive together, and two interleaved write-then-reload sequences would be
	// harmless but would log and reload twice. It also guards the two fields
	// below, which only Refresh touches.
	run sync.Mutex
	// appliedJWT is the account JWT of the last full pass that succeeded. Not
	// set by a failed one, so the next run tries again and says why again.
	appliedJWT string
	// warnedNoProbe: the "platform predates the probe" warning is logged once.
	warnedNoProbe bool

	// mu guards what the leaf_preload check reads. Separate from run, so the
	// check never waits behind a refresh that is waiting on the platform.
	mu         sync.Mutex
	accountPub string
	fetchedJWT string
}

// LeafSource is the platform side of the refresh: the probe and the full
// read. Implemented by *platform.Client.
type LeafSource interface {
	AccountJWT() (accountPub, accountJWT string, err error)
	LeafConfig() (*platform.LeafConfig, error)
}

// LeafConfPath is where `agent -leaf-config` writes nats-leaf.conf for a given
// nats.auth.creds_file, and therefore the only file the refresh will touch.
func LeafConfPath(credsFile string) string {
	return filepath.Join(filepath.Dir(credsFile), LeafConfName)
}

// LeafPidPath is the pid_file the generated conf names, beside the conf.
func LeafPidPath(credsFile string) string {
	return filepath.Join(filepath.Dir(credsFile), LeafPidName)
}

// NewLeafRefresher builds a refresher for the conf beside credsFile.
//
// source is the platform client. reload makes the running leaf re-read its
// config: Edge.ReloadEmbedded when this process hosts it, SignalLeafReload when
// something else supervises it. It is a parameter so a test can count the calls
// without a server; that is the whole seam.
func NewLeafRefresher(credsFile string, source LeafSource, reload func() error, logger *zap.Logger) *LeafRefresher {
	return &LeafRefresher{
		confPath:  LeafConfPath(credsFile),
		outputDir: filepath.Dir(credsFile),
		credsFile: credsFile,
		source:    source,
		reload:    reload,
		logger:    logger,
	}
}

// ConfPath is the file this refresher watches.
func (r *LeafRefresher) ConfPath() string { return r.confPath }

// Refresh runs one pass: probe, and -- when the account JWT moved -- fetch,
// build, compare, and only when the bytes differ write the conf and reload the
// leaf.
//
// changed reports that the file was rewritten, reloaded that the leaf was told
// to re-read it. A non-nil error with changed true means the file on disk is
// current and the running leaf is not: the revocation is NOT in effect yet, and
// the leaf_preload check will say so until it is.
func (r *LeafRefresher) Refresh() (changed, reloaded bool, err error) {
	r.run.Lock()
	defer r.run.Unlock()

	pub, jwt, err := r.source.AccountJWT()
	switch {
	case errors.Is(err, platform.ErrNoAccountJWTRoute):
		if !r.warnedNoProbe {
			r.warnedNoProbe = true
			r.logger.Warn("The platform predates the account JWT probe route, so every leaf refresh reads the full leaf config, credential included. Upgrade the platform to stop that.",
				zap.Error(err))
		}
	case err != nil:
		return false, false, err
	default:
		r.record(pub, jwt)
		if jwt == r.appliedJWT {
			return false, false, nil
		}
	}

	return r.apply()
}

// apply is the full pass: read the leaf config, build the conf, and write and
// reload only when it differs from the disk.
func (r *LeafRefresher) apply() (changed, reloaded bool, err error) {
	lc, err := r.source.LeafConfig()
	if err != nil {
		return false, false, err
	}

	// Recorded before anything can fail below, because it is what the check
	// compares the leaf against: "the platform says X" stays true even when
	// this pass cannot make the leaf agree.
	r.record(lc.AccountPub, lc.AccountJWT)

	want, err := leafConf(lc, r.outputDir, r.credsFile)
	if err != nil {
		return false, false, err
	}

	have, readErr := os.ReadFile(r.confPath)
	if readErr == nil && string(have) == want {
		r.appliedJWT = lc.AccountJWT
		return false, false, nil
	}

	if readErr == nil {
		if err := checkTrustRoot(string(have), lc); err != nil {
			return false, false, err
		}
	}

	if err := utils.WriteFileAtomic(r.confPath, []byte(want), 0o644); err != nil {
		return false, false, fmt.Errorf("write %s: %w", r.confPath, err)
	}

	if err := r.reload(); err != nil {
		return true, false, fmt.Errorf("rewrote %s but the leaf did not reload it: %w", r.confPath, err)
	}
	r.appliedJWT = lc.AccountJWT
	return true, true, nil
}

// record keeps what the platform last said, for the leaf_preload check.
func (r *LeafRefresher) record(accountPub, accountJWT string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accountPub, r.fetchedJWT = accountPub, accountJWT
}

// checkTrustRoot refuses a change that a reload cannot apply.
//
// nats-server refuses to reload a changed operator ("config reload not
// supported for TrustedOperators"), and the system account is named by the
// operator. Either one moving is a deployment-wide event -- a new operator, a
// re-keyed $SYS -- not a revocation, and there is nothing clever to do about it
// from here: the leaf needs a restart on a config that `agent -leaf-config`
// writes in full. So the file is left alone and the error says exactly that,
// on every pass until someone does it.
//
// An old file that does not parse has nothing to compare, and the new one
// replaces it.
func checkTrustRoot(old string, lc *platform.LeafConfig) error {
	m, err := natsconf.Parse(old)
	if err != nil {
		return nil
	}
	const fix = "a running leaf cannot adopt that by reload: re-run `agent -leaf-config` and restart the leaf"
	if op, _ := m["operator"].(string); op != lc.OperatorJWT {
		return fmt.Errorf("the platform serves a different operator JWT than the one in the leaf config; %s", fix)
	}
	preload, _ := m["resolver_preload"].(map[string]any)
	if _, ok := preload[lc.SysAccountPub]; !ok {
		return fmt.Errorf("the platform serves a different $SYS account (%s) than the one in the leaf config; %s", lc.SysAccountPub, fix)
	}
	return nil
}

// fetched returns what the last refresh read from the platform.
func (r *LeafRefresher) fetched() (accountPub, accountJWT string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accountPub, r.fetchedJWT
}

// accountzResponse is the slice of the monitoring port's /accountz?acc= answer
// the check reads: the account JWT the server is actually enforcing.
type accountzResponse struct {
	Account *struct {
		JWT string `json:"jwt"`
	} `json:"account_detail"`
}

// RegisterLeafPreloadCheck adds leaf_preload: is the local leaf enforcing the
// account JWT the platform serves?
//
// It is registered on every agent and skips when r is nil -- there is no
// generated nats-leaf.conf beside the creds file, or the agent is not
// platform-managed. It is not one of Edge's checks because a separately
// supervised leaf with no KV sync declared has no Edge at all, and that is
// exactly the shape that most needs this one.
func RegisterLeafPreloadCheck(reg *health.Registry, r *LeafRefresher) {
	reg.Register("leaf_preload", func(ctx context.Context) health.Result {
		return checkLeafPreload(ctx, monitorURL, r)
	})
}

// checkLeafPreload compares the JWT the leaf reports through its loopback
// monitoring port with the one last fetched from the platform. The same local,
// credential-free read hub_uplink makes, and like it a skip -- not a failure --
// when the port cannot be reached.
//
// Warn, never fail. A stale preload means a revoked device can still connect at
// this site, which someone must act on; but the site is serving its devices,
// and 503 would pull a working site out of whatever is watching it.
func checkLeafPreload(ctx context.Context, base string, r *LeafRefresher) health.Result {
	if r == nil {
		return health.Skip("no generated nats-leaf.conf beside the creds file, so there is no preload to keep current")
	}
	pub, want := r.fetched()
	if want == "" {
		return health.Skip("no account JWT read from the platform yet: the first refresh runs one platform.leaf_refresh_interval after start, or at once with cmd.leaf_refresh")
	}

	var az accountzResponse
	if err := fetchJSON(ctx, base, "accountz?acc="+url.QueryEscape(pub), &az); err != nil {
		return health.Skip("local NATS monitoring endpoint not reachable: " + err.Error())
	}
	if az.Account != nil && az.Account.JWT == want {
		return health.OK("the leaf enforces the account JWT the platform serves")
	}
	return health.Warn(
		"the local leaf enforces a different account JWT from the one the platform serves, so a device revoked since then can still connect at this site",
		"nats-leaf.conf is rewritten automatically; the leaf has not reloaded it, and the agent log says why. "+
			"Embedded leaf: restart the agent. Separately supervised leaf: restart it once -- a leaf started from a "+
			"config an older agent generated cannot be signalled (no pid_file) or reloaded (its store_dir was relative, "+
			"and nats-server refuses to reload a changed store_dir) -- and on Windows restart it after every change, "+
			"because the agent cannot signal it there. If the log says the operator or $SYS account changed, re-run "+
			"`agent -leaf-config` and restart the leaf.",
	)
}
