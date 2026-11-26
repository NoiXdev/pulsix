package docker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// statsBody answers every request with body.
func statsBody(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
}

// TestClientStatsAsksOneShot: one reading, without the engine's wait for a
// second CPU sample (backlog 3.12a, R2), on the unversioned path.
func TestClientStatsAsksOneShot(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/c1/stats" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("stream") != "false" || q.Get("one-shot") != "true" {
			t.Errorf("query = %v", q)
		}
		_, _ = w.Write([]byte(`{"read":"2026-11-26T10:00:00.5Z"}`))
	}))
	got, err := e.Stats(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 26, 10, 0, 0, 500_000_000, time.UTC); !got.Read.Equal(want) {
		t.Errorf("read = %v", got.Read)
	}
}

func TestClientStatsParsesCgroupV2(t *testing.T) {
	e := engineFor(t, statsBody(`{
	  "read":"2026-11-26T10:00:00Z",
	  "cpu_stats":{"cpu_usage":{"total_usage":5000000000},"system_cpu_usage":1,"online_cpus":4},
	  "memory_stats":{"usage":300,"limit":1000,"stats":{"inactive_file":100,"anon":7}},
	  "networks":{"eth0":{"rx_bytes":10,"tx_bytes":20},"eth1":{"rx_bytes":1,"tx_bytes":2}},
	  "blkio_stats":{"io_service_bytes_recursive":[
	    {"major":8,"minor":0,"op":"read","value":40},
	    {"major":8,"minor":0,"op":"write","value":50},
	    {"major":8,"minor":16,"op":"read","value":2}]}}`))
	got, err := e.Stats(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	want := containerStats{
		Read: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC), CPUNanos: 5e9,
		MemoryKnown: true, Memory: 200, MemoryLimit: 1000,
		Net: true, NetRx: 11, NetTx: 22, BlockRead: 42, BlockWrite: 50,
	}
	if !got.Read.Equal(want.Read) {
		t.Errorf("read = %v", got.Read)
	}
	got.Read = want.Read
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// TestClientStatsParsesCgroupV1: total_inactive_file wins over
// inactive_file, the ops are capitalised, and a container without its own
// network carries no networks.
func TestClientStatsParsesCgroupV1(t *testing.T) {
	e := engineFor(t, statsBody(`{
	  "read":"2026-11-26T10:00:00Z",
	  "cpu_stats":{"cpu_usage":{"total_usage":7}},
	  "memory_stats":{"usage":500,"limit":2000,"stats":{"total_inactive_file":150,"inactive_file":10}},
	  "blkio_stats":{"io_service_bytes_recursive":[
	    {"op":"Read","value":3},{"op":"Write","value":4},{"op":"Sync","value":99},{"op":"Total","value":7}]}}`))
	got, err := e.Stats(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUNanos != 7 || !got.MemoryKnown || got.Memory != 350 || got.MemoryLimit != 2000 {
		t.Errorf("cpu/memory = %+v", got)
	}
	if got.Net {
		t.Errorf("net = %+v", got)
	}
	if got.BlockRead != 3 || got.BlockWrite != 4 {
		t.Errorf("block = %d/%d", got.BlockRead, got.BlockWrite)
	}
}

// TestClientStatsCacheLargerThanUsage: a cache figure that is not below the
// usage is ignored, as docker stats does, instead of wrapping around.
func TestClientStatsCacheLargerThanUsage(t *testing.T) {
	e := engineFor(t, statsBody(`{"read":"2026-11-26T10:00:00Z","memory_stats":{"usage":100,"stats":{"inactive_file":100}}}`))
	got, err := e.Stats(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if !got.MemoryKnown || got.Memory != 100 || got.MemoryLimit != 0 {
		t.Errorf("got %+v", got)
	}
}

// TestClientStatsWithoutMemory: an engine that reports no memory usage (a
// missing cgroup controller) leaves memory unknown rather than zero.
func TestClientStatsWithoutMemory(t *testing.T) {
	e := engineFor(t, statsBody(`{"read":"2026-11-26T10:00:00Z","memory_stats":{}}`))
	got, err := e.Stats(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryKnown {
		t.Errorf("got %+v", got)
	}
}

// TestClientStatsParsesWindows: a Windows container (num_procs set) counts
// CPU time in 100 ns units, its memory is the private working set, without
// a limit, and its block I/O is in storage_stats.
func TestClientStatsParsesWindows(t *testing.T) {
	e := engineFor(t, statsBody(`{
	  "read":"2026-11-26T10:00:00Z","num_procs":2,
	  "cpu_stats":{"cpu_usage":{"total_usage":30}},
	  "memory_stats":{"privateworkingset":4096,"commitbytes":9999},
	  "storage_stats":{"read_size_bytes":5,"write_size_bytes":6},
	  "networks":{"abc":{"rx_bytes":1,"tx_bytes":2}}}`))
	got, err := e.Stats(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUNanos != 3000 || !got.MemoryKnown || got.Memory != 4096 || got.MemoryLimit != 0 {
		t.Errorf("cpu/memory = %+v", got)
	}
	if !got.Net || got.NetRx != 1 || got.NetTx != 2 || got.BlockRead != 5 || got.BlockWrite != 6 {
		t.Errorf("net/block = %+v", got)
	}
}

// TestClientStatsStoppedContainer: the engine answers a stopped container's
// stats with an empty reading whose time is the zero time.
func TestClientStatsStoppedContainer(t *testing.T) {
	e := engineFor(t, statsBody(`{"read":"0001-01-01T00:00:00Z","memory_stats":{},"cpu_stats":{"cpu_usage":{"total_usage":0}}}`))
	got, err := e.Stats(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Read.IsZero() {
		t.Errorf("read = %v", got.Read)
	}
}

func TestClientStatsGone(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such container", http.StatusNotFound)
	}))
	if _, err := e.Stats(context.Background(), "x"); !errors.Is(err, errContainerGone) {
		t.Errorf("err = %v", err)
	}
}

func TestClientStatsMalformed(t *testing.T) {
	for _, body := range []string{`[]`, `{"read":"yesterday"}`, `not json`} {
		e := engineFor(t, statsBody(body))
		_, err := e.Stats(context.Background(), "x")
		if err == nil || !strings.HasPrefix(err.Error(), "docker: container stats: malformed response") {
			t.Errorf("%s: err = %v", body, err)
		}
	}
}

func TestClientStatsNeverQuotesTheBody(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("TOPSECRET"))
	}))
	_, err := e.Stats(context.Background(), "x")
	if err == nil || err.Error() != "docker: container stats: status 500" {
		t.Errorf("err = %v", err)
	}
}

func TestClientCapsTheStatsBody(t *testing.T) {
	e := engineFor(t, objectBody(maxStatsBody+1))
	_, err := e.Stats(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "over") {
		t.Errorf("err = %v", err)
	}
}

// TestClientListReadsNetworkMode: the list carries each container's network
// mode, which says whether it has a network of its own (R8).
func TestClientListReadsNetworkMode(t *testing.T) {
	e := engineFor(t, statsBody(`[
	  {"Id":"a","Names":["/a"],"State":"running","HostConfig":{"NetworkMode":"host"}},
	  {"Id":"b","Names":["/b"],"State":"running"}]`))
	got, err := e.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].NetworkMode != "host" || got[1].NetworkMode != "" {
		t.Errorf("modes = %q, %q", got[0].NetworkMode, got[1].NetworkMode)
	}
}
