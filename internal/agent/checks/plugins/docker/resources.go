package docker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// Per-container resource usage (backlog 3.12a, plan
// docs/superpowers/plans/2026-11-26-docker-container-stats-3.12a.md).
const (
	// dockerMaxSampled bounds the containers whose stats one round reads:
	// the running and paused ones, first by name. The rest are counted in
	// docker.containers_unsampled.
	dockerMaxSampled = 100

	// dockerMaxStats bounds the stats requests in flight at once.
	dockerMaxStats = 8
)

const (
	metricCPU        = "docker.cpu_percent"
	metricMemory     = "docker.memory_bytes"
	metricMemoryPct  = "docker.memory_percent"
	metricNetRx      = "docker.net_rx_bytes_per_s"
	metricNetTx      = "docker.net_tx_bytes_per_s"
	metricBlockRead  = "docker.block_read_bytes_per_s"
	metricBlockWrite = "docker.block_write_bytes_per_s"
)

// statsSample is the part of a reading the next round's rates need.
type statsSample struct {
	at                    time.Time
	cpu                   uint64
	net                   bool
	rx, tx                uint64
	blockRead, blockWrite uint64
}

func sampleOf(s containerStats) statsSample {
	return statsSample{
		at: s.Read, cpu: s.CPUNanos,
		net: s.Net, rx: s.NetRx, tx: s.NetTx,
		blockRead: s.BlockRead, blockWrite: s.BlockWrite,
	}
}

// hasOwnNetwork reports whether a container with this network mode has a
// network of its own: on the host's, another container's or none, its
// traffic is not its own to report.
func hasOwnNetwork(mode string) bool {
	return mode != "host" && mode != "none" && !strings.HasPrefix(mode, "container:")
}

// resourceMetrics are the per-container usage metrics c reports.
func resourceMetrics(c containerSummary) []string {
	ms := []string{metricCPU, metricMemory, metricMemoryPct, metricBlockRead, metricBlockWrite}
	if hasOwnNetwork(c.NetworkMode) {
		ms = append(ms, metricNetRx, metricNetTx)
	}
	return ms
}

// zeroUsage is the usage of a container that is not running: nothing.
// Reporting it, rather than nothing, keeps a stopped container's usage
// from turning stale while it is stopped.
func zeroUsage(c containerSummary) []checks.Observation {
	ms := resourceMetrics(c)
	obs := make([]checks.Observation, 0, len(ms))
	for _, m := range ms {
		obs = append(obs, checks.ValueObservation(c.Name, m, 0))
	}
	return obs
}

type statsResult struct {
	stats containerStats
	err   error
}

// sampleResources reports the usage of cs, the containers this round
// counted. A container that is neither running nor paused reads zero
// without a request. Of the others, the first dockerMaxSampled by name are
// read, at most dockerMaxStats at once; the rest are only counted
// (unsampled) and named by count in err. Memory is reported from one
// reading; CPU, network and block I/O are rates against the container's
// previous reading (by ID, so a container recreated under the same name
// starts over), each group left out for a round in which a counter went
// backwards or no time passed. A failed reading is an error on each of the
// container's metrics and keeps its old baseline; a container that is gone
// reports nothing. The baselines are replaced by this round's, so they
// never hold more than the containers read.
func (p *dockerPlugin) sampleResources(ctx context.Context, engine dockerEngine, cs []containerSummary) (obs []checks.Observation, unsampled int, err error) {
	live := make([]containerSummary, 0, len(cs))
	for _, c := range cs {
		if c.State == "running" || c.State == "paused" {
			live = append(live, c)
			continue
		}
		obs = append(obs, zeroUsage(c)...)
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].Name < live[j].Name })
	if len(live) > dockerMaxSampled {
		unsampled = len(live) - dockerMaxSampled
		live = live[:dockerMaxSampled]
		err = fmt.Errorf("docker: resource usage is read for at most %d running containers, the first by name; %d left out", dockerMaxSampled, unsampled)
	}

	res := make([]statsResult, len(live))
	forEachLimited(len(live), dockerMaxStats, func(i int) {
		res[i].stats, res[i].err = engine.Stats(ctx, live[i].ID)
	})

	next := make(map[string]statsSample, len(live))
	for i, c := range live {
		r := res[i]
		switch {
		case errors.Is(r.err, errContainerGone):
			continue
		case r.err != nil:
			if old, ok := p.prev[c.ID]; ok {
				next[c.ID] = old
			}
			e := containerError("stats", opStats, c.Name, r.err)
			for _, m := range resourceMetrics(c) {
				obs = append(obs, checks.ErrObservation(c.Name, m, e))
			}
			continue
		case r.stats.Read.IsZero():
			// Stopped after the list: the engine answered an empty reading.
			obs = append(obs, zeroUsage(c)...)
			continue
		}
		cur := sampleOf(r.stats)
		next[c.ID] = cur
		old, seen := p.prev[c.ID]
		obs = append(obs, usage(c, r.stats, cur, old, seen)...)
	}
	p.prev = next
	return obs, unsampled, err
}

// usage is one container's observations from its reading st (cur) and, when
// seen, its previous one.
func usage(c containerSummary, st containerStats, cur, old statsSample, seen bool) []checks.Observation {
	var obs []checks.Observation
	if st.MemoryKnown {
		obs = append(obs, checks.ValueObservation(c.Name, metricMemory, float64(st.Memory)))
		if st.MemoryLimit > 0 {
			obs = append(obs, checks.ValueObservation(c.Name, metricMemoryPct, float64(st.Memory)/float64(st.MemoryLimit)*100))
		}
	}
	if !seen {
		return obs
	}
	elapsed := cur.at.Sub(old.at)
	if elapsed <= 0 {
		return obs
	}
	secs := elapsed.Seconds()
	if cur.cpu >= old.cpu {
		// CPU nanoseconds per elapsed nanosecond: 100 is one core.
		obs = append(obs, checks.ValueObservation(c.Name, metricCPU, float64(cur.cpu-old.cpu)/float64(elapsed.Nanoseconds())*100))
	}
	if hasOwnNetwork(c.NetworkMode) && cur.net && old.net && cur.rx >= old.rx && cur.tx >= old.tx {
		obs = append(obs,
			checks.ValueObservation(c.Name, metricNetRx, float64(cur.rx-old.rx)/secs),
			checks.ValueObservation(c.Name, metricNetTx, float64(cur.tx-old.tx)/secs),
		)
	}
	if cur.blockRead >= old.blockRead && cur.blockWrite >= old.blockWrite {
		obs = append(obs,
			checks.ValueObservation(c.Name, metricBlockRead, float64(cur.blockRead-old.blockRead)/secs),
			checks.ValueObservation(c.Name, metricBlockWrite, float64(cur.blockWrite-old.blockWrite)/secs),
		)
	}
	return obs
}
