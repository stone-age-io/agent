package platform

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// leafConfigPath is the platform's gateway bootstrap route. Like the rotation
// route it takes no record id — the target is derived from the caller's own auth
// token, so there is no parameter that could aim it at another Thing.
const leafConfigPath = "/api/me/leaf-config"

// LeafConfig is everything a gateway needs to stand up its NATS leaf server.
//
// Only one of these values is otherwise unreachable: the operator JWT lives in a
// superuser-only collection, which is the entire reason the route exists. The
// rest travels with it because the caller needs it in the same breath — and
// because a single call before NATS is up is simpler than four.
//
// Nothing here is secret beyond Creds. Operator and account JWTs are public
// trust material, verified by every server in the network; Creds is this Thing's
// own credential, which it must hold to connect at all. Account seeds and
// signing keys are never served.
type LeafConfig struct {
	// Code is this Thing's code, and Domain is the same value: the JetStream
	// domain is the code, computed by the platform and stored nowhere. Both are
	// returned because the generated nats-leaf.conf writes them into different
	// directives — `server_name` and `jetstream { domain }` — and a reader
	// should not have to know they are the same string.
	Code   string `json:"code"`
	Domain string `json:"domain"`

	Creds string `json:"creds"`

	AccountJWT string `json:"account_jwt"`
	AccountPub string `json:"account_pub"`

	OperatorJWT string `json:"operator_jwt"`

	// The $SYS account's JWT and public key. Not optional and not a $SYS
	// identity: the operator JWT names a system account, and a leaf running
	// `resolver: MEMORY` has nowhere to fetch it, so without it preloaded
	// nats-server refuses to start with "error resolving system account:
	// account missing" — before JetStream is ever reached. Connecting AS $SYS
	// needs a $SYS user credential, which is never served.
	SysAccountJWT string `json:"sys_account_jwt"`
	SysAccountPub string `json:"sys_account_pub"`

	// Where this deployment's hub accepts leaf connections, and the hub's own
	// JetStream domain. Deployment-wide facts held once on the Control Plane
	// rather than configured by hand on every edge box.
	HubLeafURL string `json:"hub_leaf_url"`
	HubDomain  string `json:"hub_domain"`
}

// LeafConfig fetches this agent's leaf configuration from the platform.
//
// It takes the same lock and session discipline as Sync and NebulaSource.Fetch:
// every writer of the session file does a read-modify-write under c.mu, or one
// job drops another's token — the agent's only password-free way back in.
//
// There is deliberately no revision probe here, unlike the credential and Nebula
// syncs. This is a one-shot run before the NATS server exists, not a poll, and
// every value in it is either public or already on disk.
func (c *Client) LeafConfig() (*LeafConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	prev := c.loadSession()

	token, _, err := c.authenticate(prev.Token)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(http.MethodGet, c.url(leafConfigPath, "", ""), nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req, token)

	var cfg LeafConfig
	if err := c.do(req, &cfg); err != nil {
		return nil, fmt.Errorf("failed to read leaf config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	prev.Token = token
	prev.HubDomain = cfg.HubDomain
	c.saveSession(prev)

	return &cfg, nil
}

// HubDomain returns the hub's JetStream domain: from the session file if a leaf
// config has ever been fetched, otherwise by fetching one now.
//
// The cache is what matters. Edge sync needs this value on every start, and an
// edge box is precisely the thing whose platform may be unreachable — so the
// common path reads a local file and the network is touched once, on the first
// start after bootstrap.
//
// It deliberately does not re-fetch to check for drift. A hub's JetStream domain
// changing is a deployment-wide event that invalidates every leaf's config, not
// something to poll for; `agent -leaf-config` is the thing that re-reads it.
func (c *Client) HubDomain() (string, error) {
	c.mu.Lock()
	cached := c.loadSession().HubDomain
	c.mu.Unlock()

	if cached != "" {
		return cached, nil
	}

	// Takes c.mu itself, so it must be called unlocked — Go mutexes are not
	// reentrant. It also persists the domain, so this path runs at most once.
	lc, err := c.LeafConfig()
	if err != nil {
		return "", err
	}
	return lc.HubDomain, nil
}

// validate refuses a half-provisioned response by NAME.
//
// Every field here becomes a directive in nats-leaf.conf. An empty one produces
// a file that either fails to load or loads and reaches nowhere, and the symptom
// arrives much later and somewhere else — a site that never appears. Failing
// here, saying which field was missing, is the difference between a one-line fix
// and an afternoon.
func (c *LeafConfig) validate() error {
	missing := []string(nil)
	for name, value := range map[string]string{
		"code":            c.Code,
		"domain":          c.Domain,
		"creds":           c.Creds,
		"account_jwt":     c.AccountJWT,
		"account_pub":     c.AccountPub,
		"operator_jwt":    c.OperatorJWT,
		"sys_account_jwt": c.SysAccountJWT,
		"sys_account_pub": c.SysAccountPub,
		"hub_leaf_url":    c.HubLeafURL,
		"hub_domain":      c.HubDomain,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// Sorted so the message is stable across runs; map iteration is not.
	sort.Strings(missing)

	return fmt.Errorf("platform returned an incomplete leaf config, missing: %s",
		strings.Join(missing, ", "))
}
