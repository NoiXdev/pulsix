package checks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

// excludedInterfacePrefixes are skipped from the discovered interface set
// by default: virtual and overlay devices (containers, bridges, tunnels)
// are churn, not host network health. An explicit "interfaces" list
// bypasses this, like disk's "mounts" does for pseudo filesystems.
var excludedInterfacePrefixes = []string{
	"veth", "docker", "br-", "virbr", "cni", "flannel", "cali", "vxlan", "kube-", "tun", "tap",
}

// netCounterReader is the net.IOCounters probe (per-interface counters).
type netCounterReader interface {
	IOCounters(ctx context.Context, pernic bool) ([]net.IOCountersStat, error)
}

// netInterfaceLister is the net.Interfaces probe (flags: up, loopback).
type netInterfaceLister interface {
	Interfaces(ctx context.Context) (net.InterfaceStatList, error)
}

type gopsutilNetCounters struct{}

func (gopsutilNetCounters) IOCounters(ctx context.Context, pernic bool) ([]net.IOCountersStat, error) {
	return net.IOCountersWithContext(ctx, pernic)
}

type gopsutilNetInterfaces struct{}

func (gopsutilNetInterfaces) Interfaces(ctx context.Context) (net.InterfaceStatList, error) {
	return net.InterfacesWithContext(ctx)
}

// maxBaselineAge bounds how old a baseline may be and still yield a rate.
// An interface can sit in the counters yet outside the reported set (down,
// or not in this key's list) for a long time; a rate against such a stale
// baseline would be averaged over the whole gap and mean nothing, so an
// older baseline is replaced without reporting.
const maxBaselineAge = 15 * time.Minute

// netSample is one interface's counters at one collection.
type netSample struct {
	at       time.Time
	counters [6]uint64 // rx bytes, tx bytes, rx errors, tx errors, rx drops, tx drops
}

// networkMetrics lines up with netSample.counters.
var networkMetrics = [6]string{
	"net.rx_bytes_per_s", "net.tx_bytes_per_s",
	"net.rx_errors_per_s", "net.tx_errors_per_s",
	"net.rx_drops_per_s", "net.tx_drops_per_s",
}

func sampleOf(s net.IOCountersStat, at time.Time) netSample {
	return netSample{at: at, counters: [6]uint64{s.BytesRecv, s.BytesSent, s.Errin, s.Errout, s.Dropin, s.Dropout}}
}

// networkPlugin reports per-interface rates (item = interface name). The
// kernel only exposes monotonic counters, so the plugin keeps the previous
// sample of every interface between calls and reports delta / elapsed. It
// must therefore be one long-lived instance (Registry builds it once).
type networkPlugin struct {
	Counters   netCounterReader
	Interfaces netInterfaceLister
	Now        func() time.Time

	mu   sync.Mutex
	prev map[string]netSample
}

func newNetworkPlugin() Plugin {
	return &networkPlugin{
		Counters:   gopsutilNetCounters{},
		Interfaces: gopsutilNetInterfaces{},
		Now:        time.Now,
		prev:       make(map[string]netSample),
	}
}

func (*networkPlugin) Name() string { return "network" }

func discoveredInterface(s net.InterfaceStat) bool {
	up, loopback := false, false
	for _, f := range s.Flags {
		switch f {
		case "up":
			up = true
		case "loopback":
			loopback = true
		}
	}
	if !up || loopback {
		return false
	}
	for _, p := range excludedInterfacePrefixes {
		if strings.HasPrefix(s.Name, p) {
			return false
		}
	}
	return true
}

// Collect resolves the interface set — the "interfaces" param verbatim when
// given, otherwise every up, non-loopback interface minus the virtual
// prefixes — and reports six rates per interface. The first sight of an
// interface, a counter that went backwards (wrap, reset, re-created
// interface) and a non-positive elapsed time report nothing for it; the
// sample still becomes the new baseline. An explicitly named interface the
// counters do not know reports an error observation per metric.
func (p *networkPlugin) Collect(ctx context.Context, params map[string]any) ([]Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Counters == nil || p.Interfaces == nil || p.Now == nil {
		return nil, nil
	}

	names, explicit, err := paramStrings(params, "interfaces")
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}

	var errs []error
	var candidates []string
	if explicit {
		for _, n := range names {
			if !reportableItem(n) {
				errs = append(errs, fmt.Errorf("network: interface %q cannot be reported: longer than %d bytes, not valid UTF-8 or carrying a control character", n, maxItemLen))
				continue
			}
			candidates = append(candidates, n)
		}
	} else {
		list, err := p.Interfaces.Interfaces(ctx)
		if err != nil {
			return nil, fmt.Errorf("network: interfaces: %w", err)
		}
		for _, s := range list {
			if discoveredInterface(s) && reportableItem(s.Name) {
				candidates = append(candidates, s.Name)
			}
		}
		sort.Strings(candidates)
	}

	// The counters and the clock are read in one critical section: one
	// instance serves every check key, each in its own goroutine, and a
	// sample taken earlier must never be stored over a later baseline.
	p.mu.Lock()
	defer p.mu.Unlock()
	stats, err := p.Counters.IOCounters(ctx, true)
	if err != nil {
		// Keep the baselines: the next good tick still reports a rate.
		return nil, errors.Join(append(errs, fmt.Errorf("network: counters: %w", err))...)
	}
	byName := make(map[string]net.IOCountersStat, len(stats))
	for _, s := range stats {
		byName[s.Name] = s
	}

	now := p.Now()
	if p.prev == nil {
		p.prev = make(map[string]netSample)
	}

	var obs []Observation
	for _, name := range candidates {
		st, ok := byName[name]
		if !ok {
			if explicit {
				wrapped := fmt.Errorf("network: interface %q: not found in the interface counters", name)
				errs = append(errs, wrapped)
				for _, m := range networkMetrics {
					obs = append(obs, errObservation(name, m, wrapped))
				}
			}
			delete(p.prev, name)
			continue
		}
		cur := sampleOf(st, now)
		old, seen := p.prev[name]
		p.prev[name] = cur
		if !seen {
			continue
		}
		elapsed := cur.at.Sub(old.at).Seconds()
		if elapsed <= 0 || cur.at.Sub(old.at) > maxBaselineAge {
			continue
		}
		reset := false
		for i := range cur.counters {
			if cur.counters[i] < old.counters[i] {
				reset = true
			}
		}
		if reset {
			continue
		}
		for i, m := range networkMetrics {
			obs = append(obs, valueObservation(name, m, float64(cur.counters[i]-old.counters[i])/elapsed))
		}
	}
	// One instance serves every check key, so baselines are merged, not
	// replaced: another key's interfaces keep theirs. Only an interface the
	// counters no longer know is forgotten.
	for name := range p.prev {
		if _, ok := byName[name]; !ok {
			delete(p.prev, name)
		}
	}

	return obs, errors.Join(errs...)
}

var _ Plugin = (*networkPlugin)(nil)
