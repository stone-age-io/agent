package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// collector reports what this site's own NATS server knows about itself.
//
// The series the config mirror used to publish — mirrored record counts, cycle
// totals, last-cycle timestamps and error counts — went with the mirror. What
// is left is the part that was always the point: is the bus up, is the uplink
// attached, how many devices are actually here, and how full is JetStream on a
// box with a small disk.
type collector struct {
	state *state
}

var (
	descConnected = prometheus.NewDesc(
		"agent_edge_nats_connected",
		"1 when the agent is connected to the local NATS leaf.",
		nil, nil,
	)
	descUplink = prometheus.NewDesc(
		"agent_edge_hub_uplink_connected",
		"1 when this leaf server holds an outbound leaf connection to the hub. 0 means the site is islanded: "+
			"local NATS keeps working, nothing reaches the platform.",
		nil, nil,
	)
	descConns = prometheus.NewDesc(
		"agent_edge_nats_connections",
		"Client connections to the local NATS leaf — the devices actually attached at this site.",
		nil, nil,
	)
	descJetStream = prometheus.NewDesc(
		"agent_edge_jetstream_bytes",
		"JetStream usage on the local leaf, by storage tier. The number to watch on an edge box with a small disk.",
		[]string{"tier"}, nil,
	)
)

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descConnected
	ch <- descUplink
	ch <- descConns
	ch <- descJetStream
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	g := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}

	g(descConnected, boolGauge(c.state.connected()))

	// The local server's own view. Skipped silently when the monitoring
	// endpoint is not reachable — emitting zeros would report an islanded site
	// with no devices, which is a much more alarming claim than "not scraped".
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if vz, err := fetchVarz(ctx, monitorURL); err == nil {
		g(descConns, float64(vz.Connections))
		if vz.JetStream.Stats != nil {
			g(descJetStream, float64(vz.JetStream.Stats.Memory), "memory")
			g(descJetStream, float64(vz.JetStream.Stats.Store), "file")
		}
	}
	if lz, err := fetchLeafz(ctx, monitorURL); err == nil {
		uplink := false
		for _, l := range lz.Leafs {
			if l.IsSpoke {
				uplink = true
				break
			}
		}
		g(descUplink, boolGauge(uplink))
	}
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// ------------------------------------------------- local NATS monitoring

// The monitoring endpoint is the `http:` line buildLeafConf writes, bound to
// loopback. It needs no credential precisely because it is loopback-only, which
// is why the edge can read its own server's state without ever holding a $SYS
// identity.

// Only the fields actually reported are declared. encoding/json ignores the
// rest of the payload, and a field parsed but never read is a promise to a
// reader that something uses it.
type varzResponse struct {
	Connections int `json:"connections"`
	JetStream   struct {
		Stats *struct {
			Memory uint64 `json:"memory"`
			Store  uint64 `json:"storage"`
		} `json:"stats"`
	} `json:"jetstream"`
}

type leafzResponse struct {
	Leafs []leafInfo `json:"leafs"`
}

type leafInfo struct {
	Name    string `json:"name"`
	IsSpoke bool   `json:"is_spoke"`
	RTT     string `json:"rtt"`
}

func fetchVarz(ctx context.Context, base string) (*varzResponse, error) {
	var out varzResponse
	return &out, fetchJSON(ctx, base, "varz", &out)
}

func fetchLeafz(ctx context.Context, base string) (*leafzResponse, error) {
	var out leafzResponse
	return &out, fetchJSON(ctx, base, "leafz", &out)
}

var monitorClient = &http.Client{Timeout: 2 * time.Second}

func fetchJSON(ctx context.Context, base, path string, into any) error {
	if base == "" {
		return fmt.Errorf("the local NATS monitoring endpoint is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/"+path, nil)
	if err != nil {
		return err
	}
	resp, err := monitorClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}
