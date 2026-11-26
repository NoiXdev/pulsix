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
	at time.Time
	// startedAt is the container's State.StartedAt when it was read ("" if
	// the inspect failed); a different one next round is a restart under
	// the same ID, whose counters started over.
	startedAt             string
	cpu                   uint64
	net                   bool
	rx, tx                uint64
	blockRead, blockWrite uint64
}

func sampleOf(s containerStats, startedAt string) statsSample {
	return statsSample{
		at: s.Read, startedAt: startedAt, cpu: s.CPUNanos,
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

// readingMetrics are the usage metrics a running container with reading st
// reports, once it has a baseline: CPU and block I/O always, memory when
// the reading has it (its percent only with a limit, so not on Windows),
// traffic only for a container with a network of its own whose reading
// carries networks.
func readingMetrics(c containerSummary, st containerStats) []string {
	ms := []string{metricCPU, metricBlockRead, metricBlockWrite}
	if st.MemoryKnown {
		ms = append(ms, metricMemory)
		if st.MemoryLimit > 0 {
			ms = append(ms, metricMemoryPct)
		}
	}
	if hasOwnNetwork(c.NetworkMode) && st.Net {
		ms = append(ms, metricNetRx, metricNetTx)
	}
	return ms
}

// errorMetrics are the metrics a failed reading of a container is reported
// on: what its last running reading had, or, for a container not read
// yet, the ones every reading has.
func errorMetrics(known []string, seen bool) []string {
	if seen {
		return known
	}
	return []string{metricCPU, metricBlockRead, metricBlockWrite}
}

// zeroUsage is the usage of a container that is not running: nothing, on
// the metrics ms its last running reading had. Reporting it, rather than
// nothing, keeps a stopped container's usage from turning stale while it
// is stopped; a metric it never reported while running is not reported
// while stopped either, or that one would turn stale once it runs again.
func zeroUsage(name string, ms []string) []checks.Observation {
	obs := make([]checks.Observation, 0, len(ms))
	for _, m := range ms {
		obs = append(obs, checks.ValueObservation(name, m, 0))
	}
	return obs
}

type statsResult struct {
	stats containerStats
	err   error
}

// sampleResources reports the usage of cs, the containers this round
// counted; started holds their State.StartedAt by ID, from the inspects.
//
// At most dockerMaxSampled containers carry usage per round: the running
// and paused ones first, by name, then the stopped ones whose last running
// reading this instance remembers, by name. The rest are counted
// (unsampled), named by count in err, and lose their baseline and
// remembered metrics — so a host that piles up exited containers never
// carries usage for more than the cap.
//
// A running or paused container is read, at most dockerMaxStats at once.
// Memory comes from one reading; CPU, network and block I/O are rates
// against the container's previous reading (by ID, so a container
// recreated under the same name starts over, and only while its start time
// is unchanged, so a restart under the same ID does too), each group left
// out for a round in which a counter went backwards or no time passed. A
// failed reading is an error on each of the container's metrics and keeps
// its old baseline; a container that is gone reports nothing.
//
// A stopped container reads zero without a request, on the metrics its
// last running reading had: a value it never reported while running is not
// reported while stopped either. The baselines and remembered metrics are
// replaced each round by those of the containers within the cap.
func (p *dockerPlugin) sampleResources(ctx context.Context, engine dockerEngine, cs []containerSummary, started map[string]string) (obs []checks.Observation, unsampled int, err error) {
	byName := func(s []containerSummary) {
		sort.SliceStable(s, func(i, j int) bool { return s[i].Name < s[j].Name })
	}
	live := make([]containerSummary, 0, len(cs))
	var stopped []containerSummary
	for _, c := range cs {
		switch {
		case c.State == "running" || c.State == "paused":
			live = append(live, c)
		case p.reported[c.ID] != nil:
			stopped = append(stopped, c)
		}
	}
	byName(live)
	byName(stopped)
	if over := len(live) + len(stopped) - dockerMaxSampled; over > 0 {
		unsampled = over
		if len(live) > dockerMaxSampled {
			live = live[:dockerMaxSampled]
		}
		stopped = stopped[:dockerMaxSampled-len(live)]
		err = fmt.Errorf("docker: resource usage is read for at most %d containers (running first, then stopped ones, each by name); %d left out", dockerMaxSampled, unsampled)
	}

	reported := make(map[string][]string, len(live)+len(stopped))
	for _, c := range stopped {
		ms := p.reported[c.ID]
		reported[c.ID] = ms
		obs = append(obs, zeroUsage(c.Name, ms)...)
	}

	res := make([]statsResult, len(live))
	forEachLimited(len(live), dockerMaxStats, func(i int) {
		res[i].stats, res[i].err = engine.Stats(ctx, live[i].ID)
	})

	next := make(map[string]statsSample, len(live))
	for i, c := range live {
		r := res[i]
		known, wasRead := p.reported[c.ID]
		switch {
		case errors.Is(r.err, errContainerGone):
			continue
		case r.err != nil:
			if old, ok := p.prev[c.ID]; ok {
				next[c.ID] = old
			}
			if wasRead {
				reported[c.ID] = known
			}
			e := statsError(ctx, c.Name, r.err)
			for _, m := range errorMetrics(known, wasRead) {
				obs = append(obs, checks.ErrObservation(c.Name, m, e))
			}
			continue
		case r.stats.Read.IsZero():
			// Stopped after the list: the engine answered an empty reading.
			if wasRead {
				reported[c.ID] = known
				obs = append(obs, zeroUsage(c.Name, known)...)
			}
			continue
		}
		cur := sampleOf(r.stats, started[c.ID])
		next[c.ID] = cur
		reported[c.ID] = readingMetrics(c, r.stats)
		old, seen := p.prev[c.ID]
		if seen && old.startedAt != "" && cur.startedAt != "" && old.startedAt != cur.startedAt {
			seen = false // restarted under the same ID: its counters started over
		}
		obs = append(obs, usage(c, r.stats, cur, old, seen)...)
	}
	p.prev, p.reported = next, reported
	return obs, unsampled, err
}

// statsError names the container in a failed stats request; one that
// missed the collect deadline reads "engine did not answer in time" rather
// than the raw context error.
func statsError(ctx context.Context, name string, err error) error {
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return fmt.Errorf("docker: stats %s: engine did not answer in time", name)
	}
	return containerError("stats", opStats, name, err)
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
