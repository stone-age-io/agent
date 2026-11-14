package edge

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// A separately supervised leaf reads the conf the agent writes, so the agent
// usually starts before the leaf is listening. The edge's first connection must
// wait for it. It used to fail: nats.Connect returned an error, Run returned,
// and the edge never ran again until the agent was restarted.
func TestLocalConnectionWaitsForALeafThatIsNotUpYet(t *testing.T) {
	port := freeLocalPort(t)
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	cfg := &Config{CredsFile: writeTestCreds(t)}

	// Nothing is listening yet.
	nc, err := nats.Connect(url, localConnectOptions(cfg)...)
	if err != nil {
		t.Fatalf("the first connection failed instead of waiting for the leaf: %v", err)
	}
	t.Cleanup(nc.Close)
	if nc.IsConnected() {
		t.Fatal("connected with no server listening; the test is not testing anything")
	}

	// The leaf comes up.
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: port, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	t.Cleanup(srv.Shutdown)
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats-server did not become ready")
	}

	deadline := time.Now().Add(15 * time.Second)
	for !nc.IsConnected() {
		if time.Now().After(deadline) {
			t.Fatalf("the edge connection never reached the leaf once it was up (status %v)", nc.Status())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// writeTestCreds writes a well-formed .creds file. The server in the test has
// no authorization, so it accepts any identity; the file only has to parse.
func writeTestCreds(t *testing.T) string {
	t.Helper()
	account, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	user, err := nkeys.CreateUser()
	if err != nil {
		t.Fatal(err)
	}
	userPub, err := user.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	userJWT, err := jwt.NewUserClaims(userPub).Encode(account)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := user.Seed()
	if err != nil {
		t.Fatal(err)
	}
	creds, err := jwt.FormatUserConfig(userJWT, seed)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "edge.creds")
	if err := os.WriteFile(path, creds, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
