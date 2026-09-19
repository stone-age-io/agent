package platform

import (
	"encoding/json"
	"errors"
	"os"

	"go.uber.org/zap"
)

// session is what the agent keeps between runs so it does not need the thing's
// password again: the platform auth token, and the revision of the credential it
// already holds.
//
// The token is a bearer credential in its own right, so the file is written 0600
// like the .creds beside it. It replaces keeping the thing's password in the
// service environment for the life of the device, which is the stronger secret
// and — as a machine-level environment variable on Windows — the more widely
// readable one.
type session struct {
	Token         string `json:"token"`
	CredsRevision string `json:"creds_revision"`

	// NebulaRevision is the same signal for the Nebula config. Both live here so
	// there is one session per thing, matching the one platform identity it has.
	// Every writer must read-modify-write under Client.mu, or one sync drops the
	// other's revision.
	NebulaRevision string `json:"nebula_revision"`

	// HubDomain is the hub's JetStream domain, cached from the last leaf-config
	// fetch. It is not a secret and not a revision — it is here because it is the
	// one thing the edge needs that arrives only over HTTP, and an edge that
	// cannot reach the platform still has to bring its buckets up.
	//
	// Without this the value never reached the running agent at all: the
	// one-shot bootstrap fetched it, wrote nats-leaf.conf (which does not carry
	// it), and exited, so twin sync disabled itself on every start.
	HubDomain string `json:"hub_domain"`
}

// loadSession returns the stored session, or a zero session if there isn't a
// usable one. A missing, unreadable, or corrupt session file is not an error:
// every caller falls back to password authentication, which is the same path a
// first boot takes.
func (c *Client) loadSession() session {
	var s session

	data, err := os.ReadFile(c.auth.SessionFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			c.logger.Warn("Could not read platform session file",
				zap.String("path", c.auth.SessionFile),
				zap.Error(err))
		}
		return s
	}

	if err := json.Unmarshal(data, &s); err != nil {
		c.logger.Warn("Ignoring unreadable platform session file",
			zap.String("path", c.auth.SessionFile),
			zap.Error(err))
		return session{}
	}

	return s
}

// saveSession persists the session.
//
// A failure here costs the agent its password-free path on the next start, which
// is worth a warning but is not worth failing an otherwise successful credential
// update — the .creds file is already correct by this point.
func (c *Client) saveSession(s session) {
	data, err := json.Marshal(s)
	if err != nil {
		c.logger.Warn("Failed to encode platform session", zap.Error(err))
		return
	}

	if err := writeFileAtomic(c.auth.SessionFile, data); err != nil {
		c.logger.Warn("Failed to save platform session",
			zap.String("path", c.auth.SessionFile),
			zap.Error(err))
	}
}
