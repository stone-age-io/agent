package nebula

import (
	"fmt"
	"os"
)

// Fetched is one answer from a Source.
//
// Changed is false when the source can tell that nothing has moved since
// knownRevision, in which case YAML is empty and must not be applied. That
// distinction is the point of the interface: a Nebula config embeds the host's
// private key, so a source that can answer "unchanged" without sending the body
// keeps key material off the network on the routine path.
type Fetched struct {
	YAML     string
	Revision string
	Changed  bool
}

// Source supplies this agent's Nebula config.
//
// Deliberately free of HTTP: the platform implementation lives in
// internal/platform beside the credential code it shares authentication with,
// and this package stays a Nebula runtime that can be tested without a server.
type Source interface {
	// Fetch returns the config, or Changed=false if knownRevision is still
	// current. An empty knownRevision means the caller has nothing yet.
	Fetch(knownRevision string) (Fetched, error)

	// Name identifies the source in logs and in the health payload.
	Name() string
}

// FileSource reads a Nebula config from a path on disk.
//
// It has no revision and reports every read as changed, which is the whole of
// its contract: a file source does not poll, does not cache, and does not roll
// back. It exists so the feature works without the platform, and so a device can
// join the mesh before it has a thing record.
type FileSource struct {
	Path string
}

func (s FileSource) Name() string { return "file" }

func (s FileSource) Fetch(string) (Fetched, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		return Fetched{}, fmt.Errorf("read nebula config %s: %w", s.Path, err)
	}
	return Fetched{YAML: string(raw), Changed: true}, nil
}
