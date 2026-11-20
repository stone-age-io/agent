package platform

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	natsjwt "github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	nebulacert "github.com/slackhq/nebula/cert"
	"go.uber.org/zap"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/curve25519"

	"github.com/stone-age-io/agent/internal/utils"
)

// DEVICE-HELD KEYS
//
// A Thing whose NATS identity or Nebula host was made with device_key holds its
// own key pair: the platform has only the public half, signs a JWT or a
// certificate for it, and never stores a seed, a creds_file or a Nebula private
// key (platform plans/device-held-keys.md). So the agent makes the pair, keeps
// the private half on disk (0600), and enrolls the public half with
// POST /api/me/keys.
//
// One rule decides everything: IF THE PLATFORM HAS NO KEY FOR ME, MAKE A NEW
// ONE. Never send again a key the platform dropped. An empty public_key on the
// platform means the device has not enrolled yet, or an operator revoked the
// key; either way the old key, if there is one, is not coming back.
//
//	platform public_key   the agent
//	empty                 generates a pair, writes it, then enrolls it
//	the same as mine      runs
//	something else        stops: an operator enrolled a different key
//
// Write first, then enroll. A crash in between leaves a key nobody enrolled,
// and the next run finds the platform empty and makes another.

// keysPath is the platform's enrollment route. Thing auth, no record id: the
// target is the caller's own nats_user and nebula_host.
const keysPath = "/api/me/keys"

// ErrForeignKey means the platform holds a public key this device did not
// make. Someone with owner or admin rights pasted a key for this Thing. The
// agent stops rather than generate over it: a second key would be refused, and
// the operator's key is the one the platform trusts.
var ErrForeignKey = errors.New("the platform has a different key enrolled for this thing than the one this device holds; " +
	"an owner or admin enrolled it. Revoke it on the platform to let this device enroll its own")

// natsSeedPath is where a device-held NATS seed lives: beside the .creds file,
// with nk's .nk extension.
func (c *Client) natsSeedPath() string {
	return strings.TrimSuffix(c.credsPath, filepath.Ext(c.credsPath)) + ".nk"
}

// credsFor returns the .creds content for this agent's NATS identity, and the
// identity as it stands afterwards (its `updated` moves if a key was enrolled).
//
// A platform-held key's credential is on the record. A device-held key's is
// built here, from the record's JWT and the local seed, after enrolling a new
// key if the platform has none.
func (c *Client) credsFor(token, natsUserID string, user *natsUserRecord) (*natsUserRecord, string, error) {
	if !user.DeviceKey {
		if user.CredsFile == "" {
			return nil, "", errors.New("the platform has no NATS credentials for this thing (is a nats_user assigned, and does it have creds generated?)")
		}
		return user, user.CredsFile, nil
	}

	if user.PublicKey == "" {
		if err := c.enrollNATSKey(token); err != nil {
			return nil, "", err
		}
		fresh, err := c.fetchUser(token, natsUserID)
		if err != nil {
			return nil, "", err
		}
		user = fresh
	}

	seed, err := os.ReadFile(c.natsSeedPath())
	if err != nil {
		return nil, "", fmt.Errorf("the platform has a NATS key enrolled for this thing but this device has no seed at %s, "+
			"so it cannot connect. Revoke the key on the platform and the device enrolls a new one: %w", c.natsSeedPath(), err)
	}
	kp, err := nkeys.FromSeed(bytes.TrimSpace(seed))
	if err != nil {
		return nil, "", fmt.Errorf("the NATS seed at %s does not parse: %w", c.natsSeedPath(), err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return nil, "", fmt.Errorf("derive the public key from %s: %w", c.natsSeedPath(), err)
	}
	if pub != user.PublicKey {
		return nil, "", fmt.Errorf("NATS: %w", ErrForeignKey)
	}
	if user.JWT == "" {
		return nil, "", errors.New("the NATS key is enrolled but the platform has issued no JWT for it (is the identity deactivated?)")
	}

	creds, err := natsjwt.FormatUserConfig(user.JWT, bytes.TrimSpace(seed))
	if err != nil {
		return nil, "", fmt.Errorf("build the .creds from the platform JWT and the local seed: %w", err)
	}
	return user, string(creds), nil
}

// credsFromJWT builds a .creds from a JWT the platform signed for this
// device's key and the local seed. The leaf config uses it: for a device-held
// key the platform serves the JWT and no creds.
func (c *Client) credsFromJWT(userJWT string) (string, error) {
	seed, err := os.ReadFile(c.natsSeedPath())
	if err != nil {
		return "", fmt.Errorf("read the device NATS seed: %w", err)
	}
	creds, err := natsjwt.FormatUserConfig(userJWT, bytes.TrimSpace(seed))
	if err != nil {
		return "", fmt.Errorf("build the .creds from the platform JWT and the local seed: %w", err)
	}
	return string(creds), nil
}

// enrollNATSKey makes a new user key pair, writes the seed, and enrolls the
// public key. Called only when the platform has no key, so it always makes a
// new one: an old seed on disk belongs to a key the platform dropped.
func (c *Client) enrollNATSKey(token string) error {
	kp, err := nkeys.CreateUser()
	if err != nil {
		return fmt.Errorf("generate a NATS key: %w", err)
	}
	seed, err := kp.Seed()
	if err != nil {
		return fmt.Errorf("read the new NATS seed: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return fmt.Errorf("read the new NATS public key: %w", err)
	}
	if err := utils.WriteFileAtomic(c.natsSeedPath(), seed, 0o600); err != nil {
		return fmt.Errorf("write the NATS seed: %w", err)
	}
	if err := c.enroll(token, map[string]string{"nats_public_key": pub}); err != nil {
		return err
	}
	c.logger.Info("Enrolled a device-held NATS key", zap.String("public_key", pub), zap.String("seed", c.natsSeedPath()))
	return nil
}

// deviceNebulaConfig is credsFor for the Nebula host: enroll a key if the
// platform has none, check the platform's key is this device's, and point the
// config's pki.key at the local key file. The platform writes a fixed path
// there (/etc/nebula/host.key); the agent keeps its key beside its cache file.
func (c *Client) deviceNebulaConfig(token, nebulaHostID string, host *nebulaHostRecord) (*nebulaHostRecord, error) {
	if c.nebulaKeyPath == "" {
		return nil, errors.New("this Nebula host holds its own key, which lives beside nebula.cache_file; set nebula.cache_file")
	}

	if host.PublicKey == "" {
		if err := c.enrollNebulaKey(token); err != nil {
			return nil, err
		}
		fresh, err := c.fetchNebulaHost(token, nebulaHostID)
		if err != nil {
			return nil, err
		}
		host = fresh
	}

	keyPEM, err := os.ReadFile(c.nebulaKeyPath)
	if err != nil {
		return nil, fmt.Errorf("the platform has a Nebula key enrolled for this thing but this device has no key at %s; "+
			"deactivate the host on the platform and attach a new one: %w", c.nebulaKeyPath, err)
	}
	priv, _, curve, err := nebulacert.UnmarshalPrivateKeyFromPEM(keyPEM)
	if err != nil || curve != nebulacert.Curve_CURVE25519 {
		return nil, fmt.Errorf("the Nebula key at %s is not an X25519 private key: %v", c.nebulaKeyPath, err)
	}
	mine, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive the Nebula public key: %w", err)
	}
	theirs, _, _, err := nebulacert.UnmarshalPublicKeyFromPEM([]byte(host.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("the platform's Nebula public key does not parse: %w", err)
	}
	if !bytes.Equal(mine, theirs) {
		return nil, fmt.Errorf("nebula: %w", ErrForeignKey)
	}

	if host.ConfigYAML == "" {
		return host, nil // no certificate yet; the caller says why
	}
	rewritten, err := withKeyPath(host.ConfigYAML, c.nebulaKeyPath)
	if err != nil {
		return nil, err
	}
	host.ConfigYAML = rewritten
	return host, nil
}

// enrollNebulaKey makes an X25519 pair exactly as `nebula-cert keygen` does
// (32 random bytes, the public key derived from them), writes the private key,
// and enrolls the public PEM.
func (c *Client) enrollNebulaKey(token string) error {
	priv := make([]byte, curve25519.ScalarSize)
	if _, err := io.ReadFull(rand.Reader, priv); err != nil {
		return fmt.Errorf("generate a Nebula key: %w", err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return fmt.Errorf("derive the Nebula public key: %w", err)
	}
	privPEM := nebulacert.MarshalPrivateKeyToPEM(nebulacert.Curve_CURVE25519, priv)
	if err := utils.WriteFileAtomic(c.nebulaKeyPath, privPEM, 0o600); err != nil {
		return fmt.Errorf("write the Nebula key: %w", err)
	}
	pubPEM := string(nebulacert.MarshalPublicKeyToPEM(nebulacert.Curve_CURVE25519, pub))
	if err := c.enroll(token, map[string]string{"nebula_public_key": pubPEM}); err != nil {
		return err
	}
	c.logger.Info("Enrolled a device-held Nebula key", zap.String("key", c.nebulaKeyPath))
	return nil
}

// withKeyPath sets pki.key in a Nebula config to path.
func withKeyPath(configYAML, path string) (string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(configYAML), &doc); err != nil {
		return "", fmt.Errorf("parse the platform's Nebula config: %w", err)
	}
	pki, ok := doc["pki"].(map[string]any)
	if !ok {
		return "", errors.New("the platform's Nebula config has no pki section")
	}
	pki["key"] = path
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("write the Nebula config: %w", err)
	}
	return string(out), nil
}

// enroll calls POST /api/me/keys. A 409 means the platform already holds a
// different key: someone enrolled one between our read and this call.
func (c *Client) enroll(token string, body map[string]string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode the enrollment: %w", err)
	}
	req, err := c.newRequest(http.MethodPost, c.url(keysPath, "", ""), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.setAuth(req, token)

	err = c.do(req, nil)
	var he *httpError
	if errors.As(err, &he) && he.status == http.StatusConflict {
		return ErrForeignKey
	}
	if err != nil {
		return fmt.Errorf("enroll the device key: %w", err)
	}
	return nil
}
