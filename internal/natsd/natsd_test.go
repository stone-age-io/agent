package natsd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// freePort returns a TCP port nothing is listening on. Racy in principle, fine
// in practice, and unavoidable here: a cluster route has to name a port before
// the peer is running, so -1 (let the kernel choose) is not an option.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not pick a free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func writeConf(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nats.conf")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	return path
}

// The embedded server clusters, and this test is the reason the package comment
// no longer claims otherwise.
//
// Start() hands the parsed config straight to nats-server: it sets NoSigs and
// nothing else, so every directive in the file — `cluster` included — is
// honoured exactly as `nats-server -c nats.conf` would honour it. That is the
// package's stated design ("no embedded-only mode, no options derived in Go"),
// and a clustered Control Plane peered with external nodes is a supported
// topology rather than a thing to be talked out of.
//
// It also justifies Stats().Routes existing. A route count that could only ever
// be zero would be a metric nobody can act on; this proves it is not.
func TestEmbeddedServerClustersWithAPeer(t *testing.T) {
	clientA, clientB := freePort(t), freePort(t)
	routeA, routeB := freePort(t), freePort(t)

	conf := func(name string, clientPort, routePort, peerRoutePort int) string {
		return fmt.Sprintf(`
server_name: %q
port: %d
cluster {
  name: "stone-age-test"
  listen: "127.0.0.1:%d"
  routes: ["nats://127.0.0.1:%d"]
}
`, name, clientPort, routePort, peerRoutePort)
	}

	srvA, err := Start(
		writeConf(t, conf("node-a", clientA, routeA, routeB)),
		fmt.Sprintf("nats://127.0.0.1:%d", clientA),
		"nats.server_url",
	)
	if err != nil {
		t.Fatalf("node A refused to start with a cluster block: %v", err)
	}
	defer srvA.Stop()

	srvB, err := Start(
		writeConf(t, conf("node-b", clientB, routeB, routeA)),
		fmt.Sprintf("nats://127.0.0.1:%d", clientB),
		"nats.server_url",
	)
	if err != nil {
		t.Fatalf("node B refused to start with a cluster block: %v", err)
	}
	defer srvB.Stop()

	// Route establishment is asynchronous; poll rather than sleep a fixed span.
	deadline := time.Now().Add(10 * time.Second)
	for {
		stats, ok := srvA.Stats()
		if ok && stats.Routes > 0 {
			return // clustered
		}
		if time.Now().After(deadline) {
			routes := -1
			if ok {
				routes = stats.Routes
			}
			t.Fatalf("node A never peered with node B (Routes = %d)", routes)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Stats reports nothing at all when there is no embedded server, so the caller
// can omit the series rather than publish zeros that read as a quiet bus.
func TestStatsOnNoServer(t *testing.T) {
	var nilSrv *Server
	if _, ok := nilSrv.Stats(); ok {
		t.Error("Stats on a nil Server should report ok == false")
	}
	if _, ok := (&Server{}).Stats(); ok {
		t.Error("Stats on a zero Server should report ok == false")
	}
}
