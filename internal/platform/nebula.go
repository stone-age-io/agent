package platform

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/stone-age-io/agent/internal/nebula"
)

// nebulaHostsCollection is the third fixed collection this client knows about,
// for the same reason as the other two: the agent is opinionated about the
// stone-age.io schema, where a thing points at its Nebula identity through a
// `nebula_host` relation exactly as it points at its NATS identity through
// `nats_user`.
const nebulaHostsCollection = "nebula_hosts"

// nebulaHostRecord is this agent's Nebula identity. Updated is the drift signal;
// ConfigYAML is the config itself, which embeds the host's private key and is
// therefore only ever requested once Updated says it moved.
type nebulaHostRecord struct {
	ConfigYAML string `json:"config_yaml"`
	Updated    string `json:"updated"`
}

// NebulaSource reads this agent's Nebula config from the platform.
//
// It is a distinct type rather than more methods on Client because `Fetch` means
// something specific to the nebula package and nothing in particular to a
// credential client. It shares Client's authentication deliberately: a thing has
// one platform identity, and duplicating the session handling to read a second
// record off the same login would be silly. The argument in
// docs/nebula-design.md against a shared library is about sharing across
// repositories, not within one binary.
type NebulaSource struct {
	client *Client
}

// NebulaSource returns a nebula.Source backed by this platform client.
func (c *Client) NebulaSource() *NebulaSource {
	return &NebulaSource{client: c}
}

func (s *NebulaSource) Name() string { return "platform" }

// Fetch returns the Nebula config the platform currently holds for this agent,
// or reports it unchanged.
//
// Same shape, and same reasoning, as Client.Sync: refresh the session without
// expanding anything, probe only the `updated` timestamp, and read the body only
// when that revision has moved. A Nebula config carries the host's private key
// inline — Nebula's PKI requires it there — so the routine path must not pull it
// across the network just to discover nothing changed.
//
// An empty knownRevision means the caller has nothing yet, and skips the probe.
func (s *NebulaSource) Fetch(knownRevision string) (nebula.Fetched, error) {
	c := s.client

	c.mu.Lock()
	defer c.mu.Unlock()

	prev := c.loadSession()

	token, record, err := c.authenticate(prev.Token)
	if err != nil {
		return nebula.Fetched{}, err
	}
	if record.NebulaHostID == "" {
		return nebula.Fetched{}, errors.New("thing record has no nebula_host assigned")
	}

	if knownRevision != "" {
		revision, err := c.probeNebulaRevision(token, record.NebulaHostID)
		if err != nil {
			return nebula.Fetched{}, err
		}

		if revision != "" && revision == knownRevision {
			// Bank the refreshed token and leave the key material on the platform.
			prev.Token = token
			prev.NebulaRevision = revision
			c.saveSession(prev)
			return nebula.Fetched{Revision: revision}, nil
		}
	}

	host, err := c.fetchNebulaConfig(token, record.NebulaHostID)
	if err != nil {
		return nebula.Fetched{}, err
	}

	// The session is only updated once the config is in hand. Recording a revision
	// the agent never received would make the next probe report "unchanged" for a
	// config it does not have.
	prev.Token = token
	prev.NebulaRevision = host.Updated
	c.saveSession(prev)

	return nebula.Fetched{
		YAML:     host.ConfigYAML,
		Revision: host.Updated,
		Changed:  true,
	}, nil
}

// probeNebulaRevision reads only the Nebula host's `updated` timestamp.
//
// `fields` is honoured on the record CRUD endpoints, unlike on auth responses,
// which is what lets the agent ask about its own config without the private key
// inside it coming back.
func (c *Client) probeNebulaRevision(token, nebulaHostID string) (string, error) {
	req, err := c.newRequest(http.MethodGet,
		c.url("/api/collections/"+nebulaHostsCollection+"/records/"+nebulaHostID, "", "updated"), nil)
	if err != nil {
		return "", err
	}
	c.setAuth(req, token)

	var host nebulaHostRecord
	if err := c.do(req, &host); err != nil {
		return "", fmt.Errorf("failed to probe nebula config revision: %w", err)
	}

	return host.Updated, nil
}

// fetchNebulaConfig reads the full Nebula host record, including the config.
func (c *Client) fetchNebulaConfig(token, nebulaHostID string) (*nebulaHostRecord, error) {
	req, err := c.newRequest(http.MethodGet,
		c.url("/api/collections/"+nebulaHostsCollection+"/records/"+nebulaHostID, "", ""), nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req, token)

	var host nebulaHostRecord
	if err := c.do(req, &host); err != nil {
		return nil, fmt.Errorf("failed to read nebula config: %w", err)
	}
	if host.ConfigYAML == "" {
		return nil, errors.New("nebula host has no config (is the host active and its network configured on the platform?)")
	}

	return &host, nil
}
