package docker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

var (
	statsT0       = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	withResources = map[string]any{"resources": true}
	// rateMetrics need two readings; usageMetrics need one.
	rateMetrics  = []string{"docker.cpu_percent", "docker.net_rx_bytes_per_s", "docker.net_tx_bytes_per_s", "docker.block_read_bytes_per_s", "docker.block_write_bytes_per_s"}
	usageMetrics = []string{"docker.memory_bytes", "docker.memory_percent"}
)

// reading is a Linux reading at statsT0+at with the given counters, 256 of
// 1024 bytes of memory in use.
func reading(at time.Duration, cpuNanos, rx, tx, blockRead, blockWrite uint64) containerStats {
	return containerStats{
		Read: statsT0.Add(at), CPUNanos: cpuNanos,
		Memory: 256, MemoryKnown: true, MemoryLimit: 1024,
		Net: true, NetRx: rx, NetTx: tx, BlockRead: blockRead, BlockWrite: blockWrite,
	}
}

// resourcesFake watches one running container, web (ID a), on a bridge
// network.
func resourcesFake() *fakeDockerEngine {
	return &fakeDockerEngine{
		containers: []containerSummary{{ID: "a", Name: "web", State: "running", NetworkMode: "bridge"}},
		details:    map[string]containerDetail{"a": {Health: "none"}},
		stats:      map[string]containerStats{"a": reading(0, 0, 0, 0, 0, 0)},
	}
}

func collect(t *testing.T, p *dockerPlugin, params map[string]any) ([]checks.Observation, error) {
	t.Helper()
	return p.Collect(context.Background(), params)
}

func valueOf(t *testing.T, obs []checks.Observation, item, metric string) float64 {
	t.Helper()
	o := findObs(t, obs, item, metric)
	if o.Value == nil {
		t.Fatalf("%s %s: no value in %+v", item, metric, o)
	}
	return *o.Value
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// resourceMetricNames are every metric the resources option adds.
func resourceMetricNames() []string {
	return append(append(slices.Clone(rateMetrics), usageMetrics...), "docker.containers_unsampled")
}

func TestDockerResourcesOffByDefault(t *testing.T) {
	for _, params := range []map[string]any{nil, {}, {"resources": false}, {"resources": "true"}} {
		f := resourcesFake()
		obs, err := collect(t, newTestDocker(f, nil), params)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.statsCalls) != 0 {
			t.Errorf("%v: stats asked for %v", params, f.statsCalls)
		}
		for _, m := range resourceMetricNames() {
			if items := metricItems(obs, m); len(items) != 0 {
				t.Errorf("%v: %s reported for %v", params, m, items)
			}
		}
	}
}

func TestDockerResourcesFirstRoundReportsMemoryOnly(t *testing.T) {
	f := resourcesFake()
	obs, err := collect(t, newTestDocker(f, nil), withResources)
	if err != nil {
		t.Fatal(err)
	}
	if v := valueOf(t, obs, "web", "docker.memory_bytes"); v != 256 {
		t.Errorf("memory = %v", v)
	}
	if v := valueOf(t, obs, "web", "docker.memory_percent"); v != 25 {
		t.Errorf("memory percent = %v", v)
	}
	for _, m := range rateMetrics {
		if hasObs(obs, "web", m) {
			t.Errorf("%s reported without a baseline", m)
		}
	}
	if v := valueOf(t, obs, "", "docker.containers_unsampled"); v != 0 {
		t.Errorf("unsampled = %v", v)
	}
	if l := findObs(t, obs, "web", "docker.state").Label; l == nil || *l != "running" {
		t.Errorf("state = %v", l)
	}
}

// TestDockerResourcesWithoutLimit: a reading without a memory limit (a
// Windows container) reports the bytes and no percent.
func TestDockerResourcesWithoutLimit(t *testing.T) {
	f := resourcesFake()
	st := reading(0, 0, 0, 0, 0, 0)
	st.MemoryLimit = 0
	f.stats["a"] = st
	obs, _ := collect(t, newTestDocker(f, nil), withResources)
	if !hasObs(obs, "web", "docker.memory_bytes") || hasObs(obs, "web", "docker.memory_percent") {
		t.Errorf("obs = %+v", obs)
	}
}

func TestDockerResourcesRates(t *testing.T) {
	f := resourcesFake()
	f.stats["a"] = reading(0, 1e9, 1000, 0, 0, 100)
	p := newTestDocker(f, nil)
	if _, err := collect(t, p, withResources); err != nil {
		t.Fatal(err)
	}
	// Ten seconds later: half a core, 1000 B/s in, 500 B/s out, 2 KiB/s read.
	f.stats["a"] = reading(10*time.Second, 6e9, 11000, 5000, 20480, 100)
	obs, err := collect(t, p, withResources)
	if err != nil {
		t.Fatal(err)
	}
	for metric, want := range map[string]float64{
		"docker.cpu_percent":             50,
		"docker.net_rx_bytes_per_s":      1000,
		"docker.net_tx_bytes_per_s":      500,
		"docker.block_read_bytes_per_s":  2048,
		"docker.block_write_bytes_per_s": 0,
		"docker.memory_bytes":            256,
	} {
		if v := valueOf(t, obs, "web", metric); !near(v, want) {
			t.Errorf("%s = %v, want %v", metric, v, want)
		}
	}
}

// TestDockerResourcesCpuAboveOneCore: 100 means one core, as docker stats
// shows it, so two busy cores read 200.
func TestDockerResourcesCpuAboveOneCore(t *testing.T) {
	f := resourcesFake()
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	f.stats["a"] = reading(5*time.Second, 10e9, 0, 0, 0, 0)
	obs, _ := collect(t, p, withResources)
	if v := valueOf(t, obs, "web", "docker.cpu_percent"); !near(v, 200) {
		t.Errorf("cpu = %v", v)
	}
}

// TestDockerResourcesCounterResetSkipsRate: a counter that went backwards
// reports no rate for its group that round, the others still do, and the
// new reading is the next baseline.
func TestDockerResourcesCounterResetSkipsRate(t *testing.T) {
	f := resourcesFake()
	f.stats["a"] = reading(0, 5e9, 100, 100, 100, 100)
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	f.stats["a"] = reading(10*time.Second, 1e9, 50, 200, 200, 300)
	obs, _ := collect(t, p, withResources)
	for _, m := range []string{"docker.cpu_percent", "docker.net_rx_bytes_per_s", "docker.net_tx_bytes_per_s"} {
		if hasObs(obs, "web", m) {
			t.Errorf("%s reported across a reset", m)
		}
	}
	if v := valueOf(t, obs, "web", "docker.block_write_bytes_per_s"); !near(v, 20) {
		t.Errorf("block write = %v", v)
	}
	f.stats["a"] = reading(20*time.Second, 2e9, 150, 300, 200, 300)
	obs, _ = collect(t, p, withResources)
	if v := valueOf(t, obs, "web", "docker.cpu_percent"); !near(v, 10) {
		t.Errorf("cpu after reset = %v", v)
	}
	if v := valueOf(t, obs, "web", "docker.net_rx_bytes_per_s"); !near(v, 10) {
		t.Errorf("rx after reset = %v", v)
	}
}

// TestDockerResourcesNoElapsedTimeNoRate: two readings with the same time
// give no rate.
func TestDockerResourcesNoElapsedTimeNoRate(t *testing.T) {
	f := resourcesFake()
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	f.stats["a"] = reading(0, 5e9, 100, 100, 100, 100)
	obs, _ := collect(t, p, withResources)
	for _, m := range rateMetrics {
		if hasObs(obs, "web", m) {
			t.Errorf("%s reported without elapsed time", m)
		}
	}
}

// TestDockerResourcesRecreatedContainerRebaselines: a container recreated
// under the same name has a new ID and starts over.
func TestDockerResourcesRecreatedContainerRebaselines(t *testing.T) {
	f := resourcesFake()
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	f.containers = []containerSummary{{ID: "a2", Name: "web", State: "running", NetworkMode: "bridge"}}
	f.details["a2"] = containerDetail{Health: "none"}
	f.stats["a2"] = reading(10*time.Second, 9e9, 9000, 9000, 9000, 9000)
	obs, _ := collect(t, p, withResources)
	for _, m := range rateMetrics {
		if hasObs(obs, "web", m) {
			t.Errorf("%s reported against the old container's baseline", m)
		}
	}
	if !hasObs(obs, "web", "docker.memory_bytes") {
		t.Error("memory missing")
	}
}

// TestDockerResourcesStoppedContainerReadsZero: a container that stops
// uses nothing; it reads zero, without a stats request, on the values its
// last running reading had, so they do not turn stale while it is stopped
// (R7).
func TestDockerResourcesStoppedContainerReadsZero(t *testing.T) {
	for _, state := range []string{"exited", "created", "restarting", "dead"} {
		f := resourcesFake()
		p := newTestDocker(f, nil)
		_, _ = collect(t, p, withResources)
		f.containers[0].State = state
		f.statsCalls = nil
		obs, err := collect(t, p, withResources)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.statsCalls) != 0 {
			t.Errorf("%s: stats asked for a stopped container", state)
		}
		for _, m := range append(slices.Clone(rateMetrics), usageMetrics...) {
			if v := valueOf(t, obs, "web", m); v != 0 {
				t.Errorf("%s: %s = %v", state, m, v)
			}
		}
		// Still stopped a round later: still zero.
		obs, _ = collect(t, p, withResources)
		if v := valueOf(t, obs, "web", "docker.cpu_percent"); v != 0 {
			t.Errorf("%s: cpu a round later = %v", state, v)
		}
	}
}

// TestDockerResourcesNeverSeenRunningReportsNothing: a container this check
// never saw running has no usage values to keep from going stale; it
// reports none.
func TestDockerResourcesNeverSeenRunningReportsNothing(t *testing.T) {
	f := resourcesFake()
	f.containers = append(f.containers, containerSummary{ID: "b", Name: "job", State: "exited", NetworkMode: "bridge"})
	f.details["b"] = containerDetail{Health: "none"}
	obs, _ := collect(t, newTestDocker(f, nil), withResources)
	for _, m := range append(slices.Clone(rateMetrics), usageMetrics...) {
		if hasObs(obs, "job", m) {
			t.Errorf("%s reported for a container never seen running", m)
		}
	}
	if slices.Contains(f.statsCalls, "b") {
		t.Error("stats asked for a stopped container")
	}
}

// TestDockerResourcesStoppedWindowsContainer: a Windows container reports
// no memory percent while it runs (its reading has no limit), so it reports
// none while stopped either.
func TestDockerResourcesStoppedWindowsContainer(t *testing.T) {
	f := resourcesFake()
	st := reading(0, 0, 0, 0, 0, 0)
	st.MemoryLimit = 0
	f.stats["a"] = st
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	f.containers[0].State = "exited"
	obs, _ := collect(t, p, withResources)
	if hasObs(obs, "web", "docker.memory_percent") {
		t.Error("memory percent reported while stopped, never while running")
	}
	if v := valueOf(t, obs, "web", "docker.memory_bytes"); v != 0 {
		t.Errorf("memory = %v", v)
	}
}

// TestDockerResourcesStoppedWithoutNetworksInTheReading: a container with
// a network mode of its own whose readings carry no networks reports no
// traffic while stopped either.
func TestDockerResourcesStoppedWithoutNetworksInTheReading(t *testing.T) {
	f := resourcesFake()
	st := reading(0, 0, 0, 0, 0, 0)
	st.Net = false
	f.stats["a"] = st
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	f.containers[0].State = "exited"
	obs, _ := collect(t, p, withResources)
	if hasObs(obs, "web", "docker.net_rx_bytes_per_s") || hasObs(obs, "web", "docker.net_tx_bytes_per_s") {
		t.Error("traffic reported while stopped, never while running")
	}
	if v := valueOf(t, obs, "web", "docker.cpu_percent"); v != 0 {
		t.Errorf("cpu = %v", v)
	}
}

// TestDockerResourcesForgetsWhatAVanishedContainerReported: the remembered
// values hold only listed containers, so a container that went and came
// back stopped reports nothing.
func TestDockerResourcesForgetsWhatAVanishedContainerReported(t *testing.T) {
	f := resourcesFake()
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	all := f.containers
	f.containers = nil
	_, _ = collect(t, p, withResources)
	if len(p.reported) != 0 {
		t.Errorf("remembered = %v", p.reported)
	}
	f.containers = all
	f.containers[0].State = "exited"
	obs, _ := collect(t, p, withResources)
	if hasObs(obs, "web", "docker.cpu_percent") {
		t.Error("a forgotten container reported usage")
	}
}

// TestDockerResourcesPausedContainerIsSampled: a paused container still
// holds its memory; it is read like a running one.
func TestDockerResourcesPausedContainerIsSampled(t *testing.T) {
	f := resourcesFake()
	f.containers[0].State = "paused"
	obs, _ := collect(t, newTestDocker(f, nil), withResources)
	if v := valueOf(t, obs, "web", "docker.memory_bytes"); v != 256 {
		t.Errorf("memory = %v", v)
	}
}

// TestDockerResourcesStoppedBetweenListAndStats: the engine answers an
// empty reading for a container that stopped after the list; it reads
// zero, and its baseline is gone.
func TestDockerResourcesStoppedBetweenListAndStats(t *testing.T) {
	f := resourcesFake()
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	f.stats["a"] = containerStats{}
	obs, _ := collect(t, p, withResources)
	if v := valueOf(t, obs, "web", "docker.cpu_percent"); v != 0 {
		t.Errorf("cpu = %v", v)
	}
	if v := valueOf(t, obs, "web", "docker.memory_bytes"); v != 0 {
		t.Errorf("memory = %v", v)
	}
	if len(p.prev) != 0 {
		t.Errorf("baselines = %v", p.prev)
	}
}

// TestDockerResourcesHostNetworkReportsNoTraffic: a container on the
// host's network, on another container's or on none has no traffic of its
// own, running or stopped (R8).
func TestDockerResourcesHostNetworkReportsNoTraffic(t *testing.T) {
	f := resourcesFake()
	f.containers = []containerSummary{
		{ID: "a", Name: "web", State: "running", NetworkMode: "host"},
		{ID: "b", Name: "side", State: "running", NetworkMode: "container:a"},
		{ID: "c", Name: "off", State: "exited", NetworkMode: "none"},
		{ID: "d", Name: "job", State: "running", NetworkMode: "default"},
	}
	for _, id := range []string{"b", "c", "d"} {
		f.details[id] = containerDetail{Health: "none"}
	}
	f.stats["b"] = reading(0, 0, 0, 0, 0, 0)
	f.stats["d"] = reading(0, 0, 0, 0, 0, 0)
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	f.containers[3].State = "exited"
	f.stats["a"] = reading(10*time.Second, 1e9, 500, 500, 0, 0)
	f.stats["b"] = reading(10*time.Second, 1e9, 500, 500, 0, 0)
	obs, _ := collect(t, p, withResources)
	for _, m := range []string{"docker.net_rx_bytes_per_s", "docker.net_tx_bytes_per_s"} {
		if items := metricItems(obs, m); !slices.Equal(items, []string{"job"}) {
			t.Errorf("%s reported for %v, want only job", m, items)
		}
	}
	if v := valueOf(t, obs, "web", "docker.cpu_percent"); !near(v, 10) {
		t.Errorf("cpu = %v", v)
	}
}

// TestDockerResourcesWithoutNetworksInTheReading: a reading without
// networks reports no traffic for that container.
func TestDockerResourcesWithoutNetworksInTheReading(t *testing.T) {
	f := resourcesFake()
	st := reading(0, 0, 0, 0, 0, 0)
	st.Net = false
	f.stats["a"] = st
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	st.Read = st.Read.Add(10 * time.Second)
	f.stats["a"] = st
	obs, _ := collect(t, p, withResources)
	if hasObs(obs, "web", "docker.net_rx_bytes_per_s") || !hasObs(obs, "web", "docker.cpu_percent") {
		t.Errorf("obs = %+v", obs)
	}
}

// TestDockerResourcesCapsSampledContainers: at most dockerMaxSampled
// running containers are read, the first by name; the rest are counted and
// named by count in the advisory, and stopped ones do not count (R6).
func TestDockerResourcesCapsSampledContainers(t *testing.T) {
	f := &fakeDockerEngine{details: map[string]containerDetail{}, stats: map[string]containerStats{}}
	n := dockerMaxSampled + 2
	for i := n - 1; i >= 0; i-- { // listed in reverse: the order is the plugin's own
		id := fmt.Sprintf("id%03d", i)
		f.containers = append(f.containers, containerSummary{ID: id, Name: fmt.Sprintf("c%03d", i), State: "running"})
		f.details[id] = containerDetail{Health: "none"}
		f.stats[id] = reading(0, 0, 0, 0, 0, 0)
	}
	f.containers = append(f.containers, containerSummary{ID: "x", Name: "a-stopped", State: "exited"})
	f.details["x"] = containerDetail{Health: "none"}
	obs, err := collect(t, newTestDocker(f, nil), withResources)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at most %d", dockerMaxSampled)) || !strings.Contains(err.Error(), "2 left out") {
		t.Errorf("err = %v", err)
	}
	if len(f.statsCalls) != dockerMaxSampled {
		t.Errorf("stats asked %d times", len(f.statsCalls))
	}
	if slices.Contains(f.statsCalls, "id100") || slices.Contains(f.statsCalls, "id101") || !slices.Contains(f.statsCalls, "id099") {
		t.Errorf("the wrong containers were read: %v", f.statsCalls)
	}
	if v := valueOf(t, obs, "", "docker.containers_unsampled"); v != 2 {
		t.Errorf("unsampled = %v", v)
	}
	if hasObs(obs, "c100", "docker.memory_bytes") || !hasObs(obs, "c099", "docker.memory_bytes") {
		t.Error("memory reported for the wrong containers")
	}
	if hasObs(obs, "a-stopped", "docker.memory_bytes") {
		t.Error("memory reported for a container never seen running")
	}
}

func TestDockerResourcesBoundsConcurrency(t *testing.T) {
	f := &fakeDockerEngine{details: map[string]containerDetail{}, stats: map[string]containerStats{}, statsDelay: 10 * time.Millisecond}
	for i := range 40 {
		id := fmt.Sprintf("id%d", i)
		f.containers = append(f.containers, containerSummary{ID: id, Name: "c" + id, State: "running"})
		f.details[id] = containerDetail{Health: "none"}
	}
	if _, err := collect(t, newTestDocker(f, nil), withResources); err != nil {
		t.Fatal(err)
	}
	if peak := f.statsPeak.Load(); peak > dockerMaxStats || peak < 2 {
		t.Errorf("peak = %d, want 2..%d", peak, dockerMaxStats)
	}
	if len(f.statsCalls) != 40 {
		t.Errorf("stats asked %d times", len(f.statsCalls))
	}
}

// TestDockerResourcesDropsVanishedBaselines: the baselines hold only the
// containers read this round, so they never grow with containers that come
// and go, and a container that comes back starts over.
func TestDockerResourcesDropsVanishedBaselines(t *testing.T) {
	f := resourcesFake()
	f.containers = append(f.containers, containerSummary{ID: "b", Name: "db", State: "running"})
	f.details["b"] = containerDetail{Health: "none"}
	f.stats["b"] = reading(0, 0, 0, 0, 0, 0)
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	if len(p.prev) != 2 {
		t.Fatalf("baselines = %v", p.prev)
	}
	all := f.containers
	f.containers = all[:1]
	f.stats["a"] = reading(10*time.Second, 0, 0, 0, 0, 0)
	_, _ = collect(t, p, withResources)
	if _, ok := p.prev["b"]; ok || len(p.prev) != 1 {
		t.Errorf("baselines = %v", p.prev)
	}
	f.containers = all
	f.stats["b"] = reading(20*time.Second, 5e9, 0, 0, 0, 0)
	obs, _ := collect(t, p, withResources)
	if hasObs(obs, "db", "docker.cpu_percent") {
		t.Error("a returning container reported a rate over its absence")
	}
	// Switching the option off forgets every baseline.
	_, _ = collect(t, p, nil)
	if p.prev != nil {
		t.Errorf("baselines with resources off = %v", p.prev)
	}
}

// TestDockerResourcesKeepsBaselineOnFailedStats: a failed reading reports
// an error on each of the container's resource metrics and keeps the old
// baseline, so the next good round reports a rate at once.
func TestDockerResourcesKeepsBaselineOnFailedStats(t *testing.T) {
	f := resourcesFake()
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	f.statsErr = map[string]error{"a": opError(opStats, errors.New("status 500"))}
	obs, _ := collect(t, p, withResources)
	for _, m := range append(slices.Clone(rateMetrics), usageMetrics...) {
		if o := findObs(t, obs, "web", m); o.Err != "docker: stats web: status 500" {
			t.Errorf("%s err = %q", m, o.Err)
		}
	}
	if l := findObs(t, obs, "web", "docker.state").Label; l == nil || *l != "running" {
		t.Errorf("state = %v", l)
	}
	f.statsErr = nil
	f.stats["a"] = reading(20*time.Second, 2e9, 0, 0, 0, 0)
	obs, _ = collect(t, p, withResources)
	if v := valueOf(t, obs, "web", "docker.cpu_percent"); !near(v, 10) {
		t.Errorf("cpu = %v", v)
	}
}

// TestDockerResourcesFailedStatsWithoutOwnNetwork: the error lands only on
// metrics the container would report.
func TestDockerResourcesFailedStatsWithoutOwnNetwork(t *testing.T) {
	f := resourcesFake()
	f.containers[0].NetworkMode = "host"
	f.statsErr = map[string]error{"a": errors.New("boom")}
	obs, _ := collect(t, newTestDocker(f, nil), withResources)
	if hasObs(obs, "web", "docker.net_rx_bytes_per_s") {
		t.Error("error reported on traffic of a host-network container")
	}
	if findObs(t, obs, "web", "docker.cpu_percent").Err == "" {
		t.Error("no error on cpu")
	}
}

// TestDockerResourcesGoneAtStats: a container removed between its inspect
// and its stats reports no usage.
func TestDockerResourcesGoneAtStats(t *testing.T) {
	f := resourcesFake()
	f.statsErr = map[string]error{"a": errContainerGone}
	obs, err := collect(t, newTestDocker(f, nil), withResources)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range append(slices.Clone(rateMetrics), usageMetrics...) {
		if hasObs(obs, "web", m) {
			t.Errorf("%s reported for a gone container", m)
		}
	}
}

// TestDockerResourcesDeadlineKeepsStateMetrics: stats that do not answer
// in time cost only the usage, as errors; the state metrics, read first,
// are reported (R5).
func TestDockerResourcesDeadlineKeepsStateMetrics(t *testing.T) {
	f := resourcesFake()
	f.blockStats = true
	f.statsing = make(chan struct{})
	p := newTestDocker(f, nil)
	p.timeout = 100 * time.Millisecond
	obs, err := collect(t, p, withResources)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if l := findObs(t, obs, "web", "docker.state").Label; l == nil || *l != "running" {
		t.Errorf("state = %v", l)
	}
	// Never read yet: the error lands on the metrics every reading has,
	// in the plugin's own words rather than the raw context error.
	if o := findObs(t, obs, "web", "docker.cpu_percent"); o.Err != "docker: stats web: engine did not answer in time" {
		t.Errorf("cpu = %+v", o)
	}
	if hasObs(obs, "web", "docker.memory_percent") || hasObs(obs, "web", "docker.net_rx_bytes_per_s") {
		t.Error("an error on a metric the container may never report")
	}
	if v := valueOf(t, obs, "", "docker.containers_total"); v != 1 {
		t.Errorf("total = %v", v)
	}
}

func TestDockerResourcesParentCancelledDuringStats(t *testing.T) {
	f := resourcesFake()
	f.blockStats = true
	f.statsing = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-f.statsing
		cancel()
	}()
	obs, err := newTestDocker(f, nil).Collect(ctx, withResources)
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Errorf("obs = %v, err = %v", obs, err)
	}
}

// TestDockerResourcesEngineDown: an unreachable engine is an error on the
// unsampled count too, while resources are on.
func TestDockerResourcesEngineDown(t *testing.T) {
	f := resourcesFake()
	f.listErr = errors.New("docker: cannot reach /x: refused")
	obs, _ := collect(t, newTestDocker(f, nil), withResources)
	if findObs(t, obs, "", "docker.containers_unsampled").Err == "" {
		t.Error("no error on the unsampled count")
	}
	obs, _ = collect(t, newTestDocker(f, nil), nil)
	if hasObs(obs, "", "docker.containers_unsampled") {
		t.Error("unsampled count reported with resources off")
	}
}

// TestDockerNewInstanceStartsEmpty: the plugin is a StatefulPlugin; each
// check key collects with an instance of its own, without another's
// baselines.
func TestDockerNewInstanceStartsEmpty(t *testing.T) {
	f := resourcesFake()
	proto := checks.StatefulPlugin(newTestDocker(f, nil))
	one := proto.NewInstance()
	two := proto.NewInstance()
	ctx := context.Background()
	_, _ = one.Collect(ctx, withResources)
	f.stats["a"] = reading(10*time.Second, 1e9, 0, 0, 0, 0)
	obs, _ := one.Collect(ctx, withResources)
	if !hasObs(obs, "web", "docker.cpu_percent") {
		t.Error("the first instance has no rate")
	}
	obs, _ = two.Collect(ctx, withResources)
	if hasObs(obs, "web", "docker.cpu_percent") {
		t.Error("the second instance shares the first one's baselines")
	}
	if _, ok := New().(checks.StatefulPlugin); !ok {
		t.Error("New is not a StatefulPlugin")
	}
}

// TestDockerResourcesThroughTheClient runs two rounds against an engine
// API served on a Unix socket.
func TestDockerResourcesThroughTheClient(t *testing.T) {
	round := 0
	sock := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"abc","Names":["/web"],"State":"running","HostConfig":{"NetworkMode":"bridge"}}]`))
		case "/containers/abc/json":
			_, _ = w.Write([]byte(`{"RestartCount":0,"State":{"ExitCode":0}}`))
		case "/containers/abc/stats":
			round++
			_, _ = fmt.Fprintf(w, `{"read":"2026-11-26T10:00:%02dZ","cpu_stats":{"cpu_usage":{"total_usage":%d}},
			  "memory_stats":{"usage":2048,"limit":8192,"stats":{"inactive_file":1024}},
			  "networks":{"eth0":{"rx_bytes":%d,"tx_bytes":0}},"blkio_stats":{"io_service_bytes_recursive":null}}`,
				round*10, round*2_000_000_000, round*10_000)
		default:
			t.Errorf("path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	p := New().(checks.StatefulPlugin).NewInstance()
	params := map[string]any{"resources": true, "socket": sock}
	if _, err := p.Collect(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	obs, err := p.Collect(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	for metric, want := range map[string]float64{
		"docker.cpu_percent":            20,
		"docker.memory_bytes":           1024,
		"docker.memory_percent":         12.5,
		"docker.net_rx_bytes_per_s":     1000,
		"docker.block_read_bytes_per_s": 0,
	} {
		if v := valueOf(t, obs, "web", metric); !near(v, want) {
			t.Errorf("%s = %v, want %v", metric, v, want)
		}
	}
}

// TestDockerResourcesCapCountsRememberedStoppedContainers: the cap covers
// the running containers and the remembered stopped ones together —
// running first, then stopped, each by name — so a host that piles up
// exited job containers never carries usage for more than
// dockerMaxSampled containers; a stopped one past the cap is forgotten.
func TestDockerResourcesCapCountsRememberedStoppedContainers(t *testing.T) {
	f := &fakeDockerEngine{details: map[string]containerDetail{}, stats: map[string]containerStats{}}
	add := func(prefix string, n int) {
		for i := range n {
			id := fmt.Sprintf("%s-id%03d", prefix, i)
			f.containers = append(f.containers, containerSummary{ID: id, Name: fmt.Sprintf("%s%03d", prefix, i), State: "running"})
			f.details[id] = containerDetail{Health: "none"}
			f.stats[id] = reading(0, 0, 0, 0, 0, 0)
		}
	}
	usageItems := func(obs []checks.Observation) map[string]bool {
		items := map[string]bool{}
		for _, o := range obs {
			if o.Item != "" && slices.Contains(append(slices.Clone(rateMetrics), usageMetrics...), o.Metric) {
				items[o.Item] = true
			}
		}
		return items
	}
	p := newTestDocker(f, nil)
	add("job", dockerMaxSampled)
	if _, err := collect(t, p, withResources); err != nil {
		t.Fatal(err)
	}
	// Every job exits and stays listed; new containers start.
	for i := range f.containers {
		f.containers[i].State = "exited"
	}
	add("web", 30)
	for round := range 3 {
		f.statsCalls = nil
		// The first round leaves 30 stopped jobs out and forgets them;
		// from then on they are no longer candidates.
		wantUnsampled := 0.0
		obs, err := collect(t, p, withResources)
		if round == 0 {
			wantUnsampled = 30
			if err == nil || !strings.Contains(err.Error(), "30 left out") {
				t.Errorf("round %d: err = %v", round, err)
			}
		} else if err != nil {
			t.Errorf("round %d: err = %v", round, err)
		}
		items := usageItems(obs)
		if len(items) > dockerMaxSampled {
			t.Fatalf("round %d: %d containers carry usage, want at most %d", round, len(items), dockerMaxSampled)
		}
		if !items["web029"] || !items["job069"] || items["job070"] || items["job099"] {
			t.Errorf("round %d: running first, then stopped by name; got %v", round, items)
		}
		if v := valueOf(t, obs, "", "docker.containers_unsampled"); v != wantUnsampled {
			t.Errorf("round %d: unsampled = %v", round, v)
		}
		if len(f.statsCalls) != 30 {
			t.Errorf("round %d: stats asked %d times", round, len(f.statsCalls))
		}
	}
	if _, ok := p.reported["job-id070"]; ok {
		t.Error("a stopped container past the cap is still remembered")
	}
	if len(p.reported) != dockerMaxSampled {
		t.Errorf("remembered %d containers", len(p.reported))
	}
	// Once the running ones are gone, a forgotten job does not come back.
	f.containers = f.containers[:dockerMaxSampled]
	obs, _ := collect(t, p, withResources)
	if items := usageItems(obs); items["job070"] || !items["job069"] {
		t.Errorf("after the running ones left: %v", items)
	}
}

// TestDockerResourcesRunningPastTheCapLosesItsState: a container read
// before that falls past the cap loses its baseline and its remembered
// metrics.
func TestDockerResourcesRunningPastTheCapLosesItsState(t *testing.T) {
	f := resourcesFake()
	f.containers[0].Name = "zz-web"
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	for i := range dockerMaxSampled {
		id := fmt.Sprintf("id%03d", i)
		f.containers = append(f.containers, containerSummary{ID: id, Name: fmt.Sprintf("a%03d", i), State: "running"})
		f.details[id] = containerDetail{Health: "none"}
		f.stats[id] = reading(0, 0, 0, 0, 0, 0)
	}
	_, _ = collect(t, p, withResources)
	if _, ok := p.prev["a"]; ok {
		t.Error("baseline kept past the cap")
	}
	if _, ok := p.reported["a"]; ok {
		t.Error("remembered metrics kept past the cap")
	}
}

// TestDockerResourcesRestartRebaselines: a container restarted under the
// same ID (its start time changed) starts its counters over; the rate
// waits a round instead of reporting the difference to the old run.
func TestDockerResourcesRestartRebaselines(t *testing.T) {
	f := resourcesFake()
	f.details["a"] = containerDetail{Health: "none", StartedAt: "2026-11-26T09:00:00Z"}
	f.stats["a"] = reading(0, 1e9, 0, 0, 0, 0)
	p := newTestDocker(f, nil)
	_, _ = collect(t, p, withResources)
	f.details["a"] = containerDetail{Health: "none", StartedAt: "2026-11-26T10:00:05Z"}
	f.stats["a"] = reading(10*time.Second, 3e9, 0, 0, 0, 0)
	obs, _ := collect(t, p, withResources)
	if hasObs(obs, "web", "docker.cpu_percent") {
		t.Error("a rate across a restart")
	}
	f.stats["a"] = reading(20*time.Second, 4e9, 0, 0, 0, 0)
	obs, _ = collect(t, p, withResources)
	if v := valueOf(t, obs, "web", "docker.cpu_percent"); !near(v, 10) {
		t.Errorf("cpu after the restart = %v", v)
	}
}
