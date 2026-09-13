// Package nebula runs an embedded Nebula overlay host inside the agent and keeps
// its config current.
//
// The agent is a member of its organization's Nebula mesh the same way it is a
// member of its NATS account: the platform generates the config, and the agent's
// job is to adopt it. That matters more than it sounds. Nebula has no CRL —
// revoking a certificate means listing its fingerprint in pki.blocklist in every
// other host's config — so a mesh only converges if its members re-read their
// configs on their own. The same is true of CA rotation and certificate renewal.
// Without something doing this, all three are a person with curl.
//
// This package is deliberately free of HTTP. Fetching is a Source, implemented
// for the platform in internal/platform beside the credential code it shares
// authentication with, so what is here can be tested without a server.
//
// See docs/nebula-design.md for the decisions behind the shape, including the
// alternatives that were rejected.
package nebula

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	nebulalib "github.com/slackhq/nebula"
	nebulaconfig "github.com/slackhq/nebula/config"
	"go.uber.org/zap"
)

// verifyPoll is how often the verify step asks whether a lighthouse tunnel has
// come up. Short enough that a healthy config is confirmed promptly, long enough
// that the wait is not a spin loop.
const verifyPoll = 500 * time.Millisecond

// Manager owns the embedded Nebula host: its config, its lifecycle, and the
// decision about whether a newly fetched config is safe to keep.
type Manager struct {
	source        Source
	cacheFile     string
	verifyTimeout time.Duration
	version       string
	logger        *zap.Logger

	// mu serialises everything that touches control or the applied config. Sync
	// runs on the scheduler, Restart arrives on a NATS command, and Stop comes
	// from shutdown — all three mutate the same Nebula instance.
	mu       sync.Mutex
	control  *nebulalib.Control
	nebCfg   *nebulaconfig.C
	applied  string // the YAML currently running
	revision string // the source revision that YAML came from
	lastSync time.Time
	rolled   bool   // the running config is a rollback, not what the source wanted
	lastErr  string // most recent failure, for the health payload
}

// Options configures a Manager. CacheFile may be empty, in which case nothing is
// cached and a rollback has nowhere to fall back to — which is the correct
// behaviour for a file source, whose config is already on disk.
type Options struct {
	Source        Source
	CacheFile     string
	VerifyTimeout time.Duration
	Version       string
	Logger        *zap.Logger
}

func New(opts Options) *Manager {
	return &Manager{
		source:        opts.Source,
		cacheFile:     opts.CacheFile,
		verifyTimeout: opts.VerifyTimeout,
		version:       opts.Version,
		logger:        opts.Logger,
	}
}

// Start brings the overlay up, preferring the source and falling back to the
// cached config.
//
// The fallback is the reason the cache exists. A device that reboots while the
// platform is unreachable still has to reach the mesh — quite possibly the mesh
// is how anyone would reach the device to fix it — so an unreachable platform at
// startup is a warning, not a failure. The scheduled sync retries.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fetched, err := m.source.Fetch("")
	if err == nil && fetched.Changed {
		if applyErr := m.applyLocked(fetched.YAML, fetched.Revision); applyErr == nil {
			m.cacheLocked(fetched.YAML)
			return nil
		} else {
			// The config the source handed us does not work. Fall through to the
			// cache: a config that reached the mesh last time is a better bet than
			// no overlay at all.
			m.logger.Error("Nebula config from source failed to start, falling back to cache",
				zap.String("source", m.source.Name()),
				zap.Error(applyErr))
			err = applyErr
		}
	} else if err != nil {
		m.logger.Warn("Could not fetch Nebula config at startup, falling back to cache",
			zap.String("source", m.source.Name()),
			zap.Error(err))
	}

	cached, cacheErr := m.readCache()
	if cacheErr != nil {
		if err == nil {
			err = cacheErr
		}
		m.lastErr = err.Error()
		return fmt.Errorf("no usable Nebula config: %w", err)
	}

	if applyErr := m.applyLocked(cached, ""); applyErr != nil {
		m.lastErr = applyErr.Error()
		return fmt.Errorf("cached Nebula config failed to start: %w", applyErr)
	}

	m.rolled = true
	m.logger.Warn("Running Nebula on the cached config")
	return nil
}

// Sync asks the source whether the config changed and adopts it if so, reporting
// whether anything was applied.
//
// This is the convergence path: it is what makes a revocation, a renewed
// certificate, or a rotated CA actually reach this device.
func (m *Manager) Sync() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fetched, err := m.source.Fetch(m.revision)
	m.lastSync = time.Now()
	if err != nil {
		m.lastErr = err.Error()
		return false, err
	}

	if !fetched.Changed {
		m.lastErr = ""
		return false, nil
	}

	previous, previousRevision := m.applied, m.revision

	if err := m.applyLocked(fetched.YAML, fetched.Revision); err != nil {
		m.lastErr = err.Error()

		// Nothing to fall back to. Leave whatever is running in place rather than
		// tearing down a working overlay because a new config was bad.
		if previous == "" {
			return false, err
		}

		m.logger.Error("New Nebula config did not come up, rolling back",
			zap.String("revision", fetched.Revision),
			zap.Error(err))

		if rbErr := m.applyLocked(previous, previousRevision); rbErr != nil {
			// Both configs are now unusable. Say so loudly; the scheduled sync is
			// the only thing that will try again.
			m.logger.Error("Rollback to the previous Nebula config also failed",
				zap.Error(rbErr))
			return false, errors.Join(err, rbErr)
		}

		m.rolled = true
		return false, err
	}

	m.rolled = false
	m.lastErr = ""
	m.cacheLocked(fetched.YAML)

	m.logger.Info("Adopted new Nebula config",
		zap.String("revision", fetched.Revision),
		zap.String("source", m.source.Name()))
	return true, nil
}

// Restart bounces Nebula on the config already running.
//
// It deliberately does not re-fetch: that is Sync's job, and keeping the two
// apart keeps each one dumb. This is the verb for a wedged tunnel, not for
// convergence.
func (m *Manager) Restart() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.applied == "" {
		return errors.New("nebula is not running")
	}

	// Captured before stopping: stopLocked clears both, since after it there is
	// no running config for them to describe.
	yaml, revision := m.applied, m.revision

	m.stopLocked()

	if err := m.applyLocked(yaml, revision); err != nil {
		m.lastErr = err.Error()
		return err
	}

	m.lastErr = ""
	return nil
}

// Stop shuts the overlay down. Safe to call when it was never started.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// applyLocked puts a config into effect and verifies that it worked, starting
// Nebula if it is not running and reloading it if it is.
//
// THE AGENT DOES NOT CLASSIFY CONFIG CHANGES, and must not. Nebula already does
// that internally — its firewall, lighthouse, hostmap, connection manager and tun
// device each register their own reload callback and decide for themselves what
// moved. A table here mirroring which keys hot-reload would duplicate logic one
// layer down and drift from it on every Nebula upgrade.
//
// So the ladder below is expressed in outcomes instead: reload, and if the
// overlay does not come back, restart; the caller rolls back if that fails too.
// The one field that genuinely cannot hot-reload is listen.port, and a device
// agent never hits it — pb-nebula issues port 0 to any host that is neither a
// lighthouse nor a relay.
func (m *Manager) applyLocked(yaml, revision string) error {
	if m.control == nil {
		return m.startLocked(yaml, revision)
	}

	if err := m.nebCfg.ReloadConfigString(yaml); err != nil {
		m.logger.Warn("Nebula config reload failed, restarting instead", zap.Error(err))
		m.stopLocked()
		return m.startLocked(yaml, revision)
	}

	m.applied, m.revision = yaml, revision

	if err := m.verifyLocked(); err != nil {
		m.logger.Warn("Overlay did not come back after reload, restarting", zap.Error(err))
		m.stopLocked()
		return m.startLocked(yaml, revision)
	}

	return nil
}

// startLocked builds a fresh Nebula instance from a config string.
func (m *Manager) startLocked(yaml, revision string) error {
	c := nebulaconfig.NewC(newSlogLogger(m.logger))
	if err := c.LoadString(yaml); err != nil {
		return fmt.Errorf("parse nebula config: %w", err)
	}

	// A nil device factory gives the platform's own TUN. configTest is false: this
	// is the real thing.
	control, err := nebulalib.Main(c, false, m.version, newSlogLogger(m.logger), nil)
	if err != nil {
		return fmt.Errorf("start nebula: %w%s", err, windowsTunHint())
	}

	if err := control.Start(); err != nil {
		control.Stop()
		return fmt.Errorf("start nebula control: %w", err)
	}

	m.control, m.nebCfg = control, c
	m.applied, m.revision = yaml, revision

	if err := m.verifyLocked(); err != nil {
		m.stopLocked()
		return err
	}

	return nil
}

func (m *Manager) stopLocked() {
	if m.control == nil {
		return
	}

	m.logger.Info("Stopping Nebula")
	m.control.Stop()
	m.control, m.nebCfg = nil, nil
	m.applied, m.revision = "", ""
}

// verifyLocked decides whether the running config actually reached the mesh.
//
// Nebula starting at all is most of the answer: that is what catches malformed
// YAML, an unparseable firewall rule, a key that does not match its certificate,
// and a certificate the CA did not sign. Those are deterministic and need no
// timer.
//
// The one failure that survives a successful start is a config that is valid but
// cannot talk to anyone — the wrong CA after a botched rotation, or a lighthouse
// address that no longer answers. Catching it means waiting for a handshake, so
// this waits only when the config actually names a lighthouse to reach. A
// lighthouse itself has nobody to hand shake with by definition, and a mesh whose
// peers are all offline would otherwise roll back a perfectly good config — a
// false positive here causes the outage it is meant to prevent.
func (m *Manager) verifyLocked() error {
	lighthouses := m.lighthouseTargetsLocked()
	if len(lighthouses) == 0 {
		return nil
	}

	deadline := time.Now().Add(m.verifyTimeout)
	for {
		for _, addr := range lighthouses {
			if m.control.GetHostInfoByVpnAddr(addr, false) != nil {
				return nil
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("no lighthouse reachable within %v", m.verifyTimeout)
		}
		time.Sleep(verifyPoll)
	}
}

// lighthouseTargetsLocked returns the lighthouses this host is meant to reach,
// and nothing when this host is itself a lighthouse.
func (m *Manager) lighthouseTargetsLocked() []netip.Addr {
	if m.nebCfg == nil || m.nebCfg.GetBool("lighthouse.am_lighthouse", false) {
		return nil
	}

	raw := m.nebCfg.GetStringSlice("lighthouse.hosts", nil)
	addrs := make([]netip.Addr, 0, len(raw))
	for _, h := range raw {
		if addr, err := netip.ParseAddr(h); err == nil {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// cacheLocked records a config that reached the mesh, so the next boot has
// something to fall back to when the platform is unreachable.
//
// Best-effort: an agent that is on the overlay right now should not be stopped
// because it could not write a file it only needs after a restart.
func (m *Manager) cacheLocked(yaml string) {
	if m.cacheFile == "" {
		return
	}
	if err := writeFileAtomic(m.cacheFile, []byte(yaml)); err != nil {
		m.logger.Warn("Failed to cache Nebula config", zap.Error(err))
	}
}

func (m *Manager) readCache() (string, error) {
	if m.cacheFile == "" {
		return "", errors.New("no nebula cache file configured")
	}
	raw, err := os.ReadFile(m.cacheFile)
	if err != nil {
		return "", fmt.Errorf("read cached nebula config: %w", err)
	}
	return string(raw), nil
}

// writeFileAtomic writes data through a temp file in the same directory and
// renames it into place, so a crash mid-write cannot leave a truncated config.
//
// os.CreateTemp opens 0600, which is what this wants: a Nebula config embeds the
// host's private key inline, because Nebula's PKI requires it there.
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

// Health is the mesh state the agent reports through cmd.health.
//
// It lives here rather than in internal/nats because this is where the facts
// are. Everything in it is read from the running Nebula instance or from the
// certificate it is using — not from the config the agent believes it applied.
type Health struct {
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`

	// Source says whether this device converges on its own. A "file" agent has no
	// revision and never polls, so an empty ConfigRevision means something
	// different for it than for a "platform" one.
	Source string `json:"source,omitempty"`

	OverlayIP    string `json:"overlay_ip,omitempty"`
	Tunnels      int    `json:"tunnels"`
	LighthouseUp bool   `json:"lighthouse_up"`

	CertExpiresAt string `json:"cert_expires_at,omitempty"`

	// ConfigRevision is the source revision currently running. Comparing it with
	// what the platform holds answers "has this revocation actually landed on this
	// device?" — which is the question the whole feature exists to make answerable.
	ConfigRevision string `json:"config_revision,omitempty"`

	LastSync       string   `json:"last_sync,omitempty"`
	RolledBack     bool     `json:"rolled_back,omitempty"`
	UnsafeNetworks []string `json:"unsafe_networks,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// Health reports the state of the overlay.
func (m *Manager) Health() *Health {
	m.mu.Lock()
	defer m.mu.Unlock()

	h := &Health{
		Enabled:        true,
		Source:         m.source.Name(),
		ConfigRevision: m.revision,
		RolledBack:     m.rolled,
		Error:          m.lastErr,
	}

	if !m.lastSync.IsZero() {
		h.LastSync = m.lastSync.UTC().Format(time.RFC3339)
	}

	if m.control == nil {
		return h
	}
	h.Running = true
	h.Tunnels = len(m.control.ListHostmapHosts(false))

	for _, addr := range m.lighthouseTargetsLocked() {
		if m.control.GetHostInfoByVpnAddr(addr, false) != nil {
			h.LighthouseUp = true
			break
		}
	}

	// Our own address comes from the device rather than the config, and the
	// certificate comes from the address, so both describe what Nebula is
	// actually running with.
	networks := m.control.Device().Networks()
	if len(networks) == 0 {
		return h
	}
	ourAddr := networks[0].Addr()
	h.OverlayIP = ourAddr.String()

	crt := m.control.GetCertByVpnIp(ourAddr)
	if crt == nil {
		return h
	}
	h.CertExpiresAt = crt.NotAfter().UTC().Format(time.RFC3339)
	for _, n := range crt.UnsafeNetworks() {
		h.UnsafeNetworks = append(h.UnsafeNetworks, n.String())
	}

	return h
}
