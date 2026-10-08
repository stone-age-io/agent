package nats

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
	"github.com/stone-age-io/agent/internal/config"
	"github.com/stone-age-io/agent/internal/tasks"
	"go.uber.org/zap"
)

// These run against a real nats-server, in process. What they pin is wire
// behaviour -- which subjects answer, which headers a reply carries, what the
// server refuses -- and a fake connection would only assert what we believe
// that behaviour to be.

// runServer starts a nats-server with two users: "agent", allowed to subscribe
// to agentSubscribe and nothing else, and "ops", allowed everything. It returns
// the client URL.
func runServer(t *testing.T, agentSubscribe []string) string {
	t.Helper()

	s, err := server.NewServer(&server.Options{
		Host:   "127.0.0.1",
		Port:   -1,
		NoLog:  true,
		NoSigs: true,
		Users: []*server.User{
			{
				Username: "agent",
				Password: "agent",
				Permissions: &server.Permissions{
					Publish:   &server.SubjectPermission{Allow: []string{">"}},
					Subscribe: &server.SubjectPermission{Allow: agentSubscribe},
				},
			},
			{Username: "ops", Password: "ops"},
		},
	})
	if err != nil {
		t.Fatalf("failed to create nats-server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats-server did not start")
	}
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

// startAgent registers the command service the way agent.New does, on a
// client connected as the "agent" user. No command is allowlisted, so cmd.exec
// refuses everything -- which is the error path these tests want.
func startAgent(t *testing.T, url string) *Client {
	t.Helper()

	cfg := &config.Config{
		Code:          "srv-01",
		Location:      "hq",
		SubjectPrefix: "agents",
		NATS: config.NATSConfig{
			URLs:          []string{url},
			Auth:          config.AuthConfig{Type: "userpass", Username: "agent", Password: "agent"},
			MaxReconnects: -1,
			ReconnectWait: time.Second,
			DrainTimeout:  time.Second,
		},
		Commands: config.CommandsConfig{Timeout: 5 * time.Second},
	}

	client, err := NewClient(&cfg.NATS, zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}
	t.Cleanup(client.Close)

	executor := tasks.NewExecutor(zap.NewNop(), 5*time.Second, context.Background())
	handlers := NewCommandHandlers(zap.NewNop(), cfg, executor, client, "v1.2.3", nil, nil, nil)
	if err := handlers.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// The server has seen every subscription once a flush round-trips, so a
	// request sent after this cannot overtake one.
	if err := client.Flush(); err != nil {
		t.Fatalf("Flush() error: %v", err)
	}
	return client
}

func connectOps(t *testing.T, url string) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url, nats.UserInfo("ops", "ops"))
	if err != nil {
		t.Fatalf("ops connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func request(t *testing.T, nc *nats.Conn, subject, data string) *nats.Msg {
	t.Helper()
	msg, err := nc.Request(subject, []byte(data), 5*time.Second)
	if err != nil {
		t.Fatalf("request to %s: %v", subject, err)
	}
	return msg
}

// Discovery finds the agent by its shared service name, says which agent it is,
// and lists the command subjects exactly as they were before the agent was a
// micro service -- with no queue group, so the wire behaviour is unchanged.
func TestServiceDiscovery(t *testing.T) {
	url := runServer(t, []string{">"})
	startAgent(t, url)
	ops := connectOps(t, url)

	var ping micro.Ping
	if err := json.Unmarshal(request(t, ops, "$SRV.PING."+ServiceName, "").Data, &ping); err != nil {
		t.Fatalf("unmarshal ping: %v", err)
	}
	if ping.Name != ServiceName {
		t.Errorf("ping name = %q, want %q", ping.Name, ServiceName)
	}
	if ping.Version != "1.2.3" {
		t.Errorf("ping version = %q, want 1.2.3 (the build version without its v)", ping.Version)
	}
	if ping.Metadata["code"] != "srv-01" || ping.Metadata["location"] != "hq" {
		t.Errorf("ping metadata = %v, want code srv-01 and location hq", ping.Metadata)
	}

	var info micro.Info
	if err := json.Unmarshal(request(t, ops, "$SRV.INFO."+ServiceName, "").Data, &info); err != nil {
		t.Fatalf("unmarshal info: %v", err)
	}
	want := []string{"ping", "service", "logs", "exec", "health", "rotate_creds", "nebula"}
	if len(info.Endpoints) != len(want) {
		t.Errorf("info lists %d endpoints, want %d: %+v", len(info.Endpoints), len(want), info.Endpoints)
	}
	got := map[string]micro.EndpointInfo{}
	for _, e := range info.Endpoints {
		got[e.Name] = e
	}
	for _, name := range want {
		e, ok := got[name]
		if !ok {
			t.Errorf("no %q endpoint", name)
			continue
		}
		if wantSubject := "agents.srv-01.cmd." + name; e.Subject != wantSubject {
			t.Errorf("%s subject = %q, want %q", name, e.Subject, wantSubject)
		}
		if e.QueueGroup != "" {
			t.Errorf("%s queue group = %q, want none", name, e.QueueGroup)
		}
	}
}

// An error reply carries the same JSON body it always has -- callers reading
// "status" see no difference -- plus micro's error headers, and it is counted
// in $SRV.STATS. A success carries no error header and is not counted.
func TestErrorRepliesKeepTheirBodyAndAreCounted(t *testing.T) {
	url := runServer(t, []string{">"})
	startAgent(t, url)
	ops := connectOps(t, url)

	tests := []struct {
		name       string
		subject    string
		data       string
		wantStatus string
		wantCode   string // "" means no error header at all
	}{
		{"refused by the executor", "agents.srv-01.cmd.exec", `{"command":"not-allowlisted"}`, "error", codeInternal},
		{"unparseable request", "agents.srv-01.cmd.service", `not json`, "error", codeBadRequest},
		{"success", "agents.srv-01.cmd.ping", ``, "pong", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := request(t, ops, tt.subject, tt.data)

			var body map[string]any
			if err := json.Unmarshal(msg.Data, &body); err != nil {
				t.Fatalf("reply is not JSON: %q", msg.Data)
			}
			if body["status"] != tt.wantStatus {
				t.Errorf("status = %v, want %s", body["status"], tt.wantStatus)
			}
			if code := msg.Header.Get(micro.ErrorCodeHeader); code != tt.wantCode {
				t.Errorf("%s = %q, want %q", micro.ErrorCodeHeader, code, tt.wantCode)
			}
			if tt.wantCode != "" && msg.Header.Get(micro.ErrorHeader) == "" {
				t.Errorf("%s is empty on an error reply", micro.ErrorHeader)
			}
		})
	}

	var stats micro.Stats
	if err := json.Unmarshal(request(t, ops, "$SRV.STATS."+ServiceName, "").Data, &stats); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	counts := map[string][2]int{} // name -> requests, errors
	for _, e := range stats.Endpoints {
		counts[e.Name] = [2]int{e.NumRequests, e.NumErrors}
	}
	for name, want := range map[string][2]int{"exec": {1, 1}, "service": {1, 1}, "ping": {1, 0}} {
		if counts[name] != want {
			t.Errorf("%s stats (requests, errors) = %v, want %v", name, counts[name], want)
		}
	}
}

// A credential without $SRV is refused discovery and nothing else: commands
// still answer, and the refusal is kept for the nats_permissions check.
//
// Before handleAsyncError checked for a nil subscription, this test did not
// fail -- it killed the test binary, because nats.go reports the refusal with
// no subscription attached and reading sub.Subject panicked on a goroutine
// nothing recovers.
func TestRefusedDiscoveryLeavesCommandsWorking(t *testing.T) {
	url := runServer(t, []string{"agents.>", "_INBOX.>"})
	agent := startAgent(t, url)
	ops := connectOps(t, url)

	var pong map[string]any
	if err := json.Unmarshal(request(t, ops, "agents.srv-01.cmd.ping", "").Data, &pong); err != nil || pong["status"] != "pong" {
		t.Fatalf("cmd.ping = %v (%v), want pong", pong, err)
	}

	// The refusal arrives asynchronously, after the subscribe.
	deadline := time.Now().Add(5 * time.Second)
	for agent.LastRefusal() == nil {
		if time.Now().After(deadline) {
			t.Fatal("no refusal kept for a credential without $SRV")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r := agent.LastRefusal(); !strings.Contains(r.Message, "$SRV") {
		t.Errorf("refusal = %q, want it to name a $SRV subject", r.Message)
	}

	if _, err := ops.Request("$SRV.PING."+ServiceName, nil, time.Second); err == nil {
		t.Error("$SRV.PING was answered by an agent whose credential refuses $SRV")
	}
}

func TestServiceVersion(t *testing.T) {
	tests := map[string]string{
		"0.3.2":                   "0.3.2",
		"v0.3.2":                  "0.3.2",
		"v0.3.2-1-gabc1234":       "0.3.2-1-gabc1234", // git describe, one commit past a tag
		"v0.3.2-1-gabc1234-dirty": "0.3.2-1-gabc1234-dirty",
		"dev":                     "0.0.0-dev", // make build and go build
		"abc1234":                 "0.0.0-dev", // git describe with no tag
		"":                        "0.0.0-dev",
	}
	for in, want := range tests {
		if got := serviceVersion(in); got != want {
			t.Errorf("serviceVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// A header value cannot span lines, and micro refuses an empty description.
func TestHeaderLine(t *testing.T) {
	tests := map[string]string{
		"command not in allowed list": "command not in allowed list",
		"exit status 1\nstderr: boom": "exit status 1",
		"  padded  ":                  "padded",
		"":                            "error",
		"\nleading newline":           "error",
	}
	for in, want := range tests {
		if got := headerLine(in); got != want {
			t.Errorf("headerLine(%q) = %q, want %q", in, got, want)
		}
	}
}
