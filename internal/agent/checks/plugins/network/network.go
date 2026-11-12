// Package network is the agent's network check plugin: traffic, error and
// drop rates per interface. It is the one stateful plugin
// (checks.StatefulPlugin): rates need the previous counters, so the
// scheduler clones it per check key. The Plugin contract and the shared
// observation helpers it builds on are in internal/agent/checks.
package network

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/net"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

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
// is a StatefulPlugin: every check key collects with its own instance
// (NewInstance), so the baselines are always this key's own previous
// collect, taken one interval ago — never another key's. An instance is
// collected by its key's one goroutine only, so it holds no lock.
type networkPlugin struct {
	Counters   netCounterReader
	Interfaces netInterfaceLister
	Now        func() time.Time
	// ExcludedPrefixes are the virtual-interface name prefixes the default
	// interface set skips. An explicit "interfaces" list bypasses them.
	ExcludedPrefixes []string

	prev map[string]netSample
}

// New returns the network plugin wired to gopsutil. It is a
// checks.StatefulPlugin prototype: the scheduler collects only the
// instances NewInstance hands out, one per check key.
func New() checks.Plugin {
	return &networkPlugin{
		Counters:   gopsutilNetCounters{},
		Interfaces: gopsutilNetInterfaces{},
		Now:        time.Now,

		ExcludedPrefixes: excludedInterfacePrefixesFor(runtime.GOOS),
	}
}

// NewInstance returns a plugin with the same providers and no baselines.
func (p *networkPlugin) NewInstance() checks.Plugin {
	return &networkPlugin{Counters: p.Counters, Interfaces: p.Interfaces, Now: p.Now, ExcludedPrefixes: p.ExcludedPrefixes}
}

func (*networkPlugin) Name() string { return "network" }

// discoveredInterface reports whether s is in the default interface set:
// up, not loopback and not named with one of the excluded prefixes. The
// prefixes are those of the system (excludedInterfacePrefixesFor).
func discoveredInterface(s net.InterfaceStat, excluded []string) bool {
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
	for _, p := range excluded {
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
// sample still becomes the new baseline. The baselines are replaced by
// exactly this collect's samples, so an interface not collected this time
// (down, gone, no longer listed) loses its baseline and, when it is back,
// re-baselines instead of reporting an average over its absence. An
// explicitly named interface the counters do not know reports an error
// observation per metric.
func (p *networkPlugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Counters == nil || p.Interfaces == nil || p.Now == nil {
		return nil, nil
	}

	names, explicit, err := checks.ParamStrings(params, "interfaces")
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}

	var errs []error
	var candidates []string
	if explicit {
		for _, n := range names {
			if !checks.ReportableItem(n) {
				errs = append(errs, fmt.Errorf("network: interface %q cannot be reported: longer than %d bytes, not valid UTF-8 or carrying a control character", n, checks.MaxItemLen))
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
			if discoveredInterface(s, p.ExcludedPrefixes) && checks.ReportableItem(s.Name) {
				candidates = append(candidates, s.Name)
			}
		}
		sort.Strings(candidates)
	}

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
	next := make(map[string]netSample, len(candidates))
	var obs []checks.Observation
	for _, name := range candidates {
		st, ok := byName[name]
		if !ok {
			if explicit {
				wrapped := fmt.Errorf("network: interface %q: not found in the interface counters", name)
				errs = append(errs, wrapped)
				for _, m := range networkMetrics {
					obs = append(obs, checks.ErrObservation(name, m, wrapped))
				}
			}
			continue
		}
		cur := sampleOf(st, now)
		next[name] = cur
		old, seen := p.prev[name]
		if !seen {
			continue
		}
		elapsed := cur.at.Sub(old.at).Seconds()
		if elapsed <= 0 {
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
			obs = append(obs, checks.ValueObservation(name, m, float64(cur.counters[i]-old.counters[i])/elapsed))
		}
	}
	p.prev = next

	return obs, errors.Join(errs...)
}

var _ checks.Plugin = (*networkPlugin)(nil)
