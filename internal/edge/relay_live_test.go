package edge

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// startPump runs a relay's pump until the test ends.
func startPump(t *testing.T, r *relay) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = r.pump(ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A live change is published without reading the hub first. That read was half
// of the two round trips each change used to cost, and the wait for the answer
// was the other half.
func TestRelayLiveChangesSkipTheHubRead(t *testing.T) {
	local := newFakeTwinKV(nil)
	hub := newFakeTwinKV(nil)
	startPump(t, &relay{src: local, dst: hub})
	time.Sleep(50 * time.Millisecond) // past the (empty) replay

	for i := 0; i < 3; i++ {
		_, _ = local.Put(context.Background(), fmt.Sprintf("thing.S01.k%d", i), []byte("v"))
	}
	waitFor(t, time.Second, "the hub to hold every live change", func() bool { return len(hub.keys()) == 3 })

	if n := hub.getCount(); n != 0 {
		t.Errorf("the relay read the hub %d time(s) for live changes, want 0", n)
	}
}

// A live write the hub rejects comes back on the retry tick, with the value the
// edge holds then.
func TestRelayRetriesAFailedLiveWrite(t *testing.T) {
	withShortRetry(t, 20*time.Millisecond)
	local := newFakeTwinKV(nil)
	hub := newFakeTwinKV(nil)
	startPump(t, &relay{src: local, dst: hub})
	time.Sleep(50 * time.Millisecond)

	hub.setWriteErr(errHubDown)
	_, _ = local.Put(context.Background(), "thing.S01.temp", []byte("21"))
	waitFor(t, time.Second, "the hub to reject the write", func() bool { return hub.writeFailCount() > 0 })

	local.set("thing.S01.temp", "25") // the device reported again meanwhile
	hub.setWriteErr(nil)
	waitFor(t, 2*time.Second, "the retry to land", func() bool {
		v, ok := hub.get("thing.S01.temp")
		return ok && v == "25"
	})
}

// --- against real servers ------------------------------------------------------

// hubAndLeaf starts a hub and one leaf in different JetStream domains, linked,
// with a `twin` bucket on each, and returns a hubKV built exactly as startRelay
// builds it, on a connection to the leaf.
func hubAndLeaf(t *testing.T) (*hubKV, jetstream.KeyValue) {
	t.Helper()
	start := func(o *server.Options) *server.Server {
		s, err := server.NewServer(o)
		if err != nil {
			t.Fatal(err)
		}
		go s.Start()
		if !s.ReadyForConnections(10 * time.Second) {
			t.Fatal("server not ready")
		}
		t.Cleanup(s.Shutdown)
		return s
	}
	ho := &server.Options{Host: "127.0.0.1", Port: -1, ServerName: "hub", JetStream: true,
		JetStreamDomain: "hub", StoreDir: t.TempDir(), NoSigs: true}
	ho.LeafNode.Host, ho.LeafNode.Port = "127.0.0.1", -1
	hub := start(ho)

	u, _ := url.Parse(fmt.Sprintf("nats-leaf://127.0.0.1:%d", ho.LeafNode.Port))
	lo := &server.Options{Host: "127.0.0.1", Port: -1, ServerName: "S01", JetStream: true,
		JetStreamDomain: "S01", StoreDir: t.TempDir(), NoSigs: true}
	lo.LeafNode.Remotes = []*server.RemoteLeafOpts{{URLs: []*url.URL{u}}}
	leaf := start(lo)

	ctx := context.Background()
	hnc, err := nats.Connect(hub.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hnc.Close)
	hjs, _ := jetstream.New(hnc)
	atHub, err := hjs.CreateKeyValue(ctx, bucketConfig("twin", ""))
	if err != nil {
		t.Fatal(err)
	}

	lnc, err := nats.Connect(leaf.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lnc.Close)
	ljs, _ := jetstream.New(lnc)
	if _, err := ljs.CreateKeyValue(ctx, bucketConfig("twin", "")); err != nil {
		t.Fatal(err)
	}

	hubJS, _ := jetstream.NewWithDomain(lnc, "hub", jetstream.WithPublishAsyncTimeout(relayAckTimeout))
	var fromLeaf jetstream.KeyValue
	waitFor(t, 10*time.Second, "the hub's JetStream to be reachable over the leaf", func() bool {
		fromLeaf, err = hubJS.KeyValue(ctx, "twin")
		return err == nil
	})
	return newHubKV(fromLeaf, hubJS, "hub"), atHub
}

// The subject hubKV builds by hand is the one thing the fakes cannot check. A put
// and a delete sent from the leaf must land in the HUB's bucket -- not the leaf's
// same-named one -- and a burst of writes to one key must leave the last one.
func TestHubKVPublishLandsInTheHubBucket(t *testing.T) {
	h, atHub := hubAndLeaf(t)
	ctx := context.Background()

	wait := func(f jetstream.PubAckFuture) {
		t.Helper()
		select {
		case ack := <-f.Ok():
			if ack.Stream != "KV_twin" || ack.Domain != "hub" {
				t.Fatalf("acknowledged by stream %q in domain %q, want KV_twin in hub", ack.Stream, ack.Domain)
			}
		case err := <-f.Err():
			t.Fatalf("publish failed: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("no answer to the publish")
		}
	}

	f, err := h.publish("thing.S01.temp", []byte("21"), false)
	if err != nil {
		t.Fatal(err)
	}
	wait(f)
	if e, err := atHub.Get(ctx, "thing.S01.temp"); err != nil || string(e.Value()) != "21" {
		t.Fatalf("hub bucket: %v %v, want 21", e, err)
	}

	f, err = h.publish("thing.S01.temp", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	wait(f)
	if _, err := atHub.Get(ctx, "thing.S01.temp"); err != jetstream.ErrKeyNotFound {
		t.Fatalf("after a published delete the hub returned %v, want ErrKeyNotFound", err)
	}

	var last jetstream.PubAckFuture
	for i := 1; i <= 200; i++ {
		if last, err = h.publish("thing.S01.count", []byte(fmt.Sprint(i)), false); err != nil {
			t.Fatal(err)
		}
	}
	wait(last)
	if e, err := atHub.Get(ctx, "thing.S01.count"); err != nil || string(e.Value()) != "200" {
		t.Fatalf("after 200 writes in a row the hub holds %v %v, want 200 -- writes landed out of order", e, err)
	}
}
