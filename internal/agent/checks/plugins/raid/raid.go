// Package raid is the agent's software RAID check plugin: Linux md arrays,
// read from /proc/mdstat. One item per array (md0, md127, …) with raid.state
// (a label: clean, degraded, resyncing, recovering, checking, reshaping or
// inactive), raid.devices_total, raid.devices_active, raid.devices_failed
// and raid.sync_pct. The Plugin contract and the shared observation helpers
// it builds on are in internal/agent/checks.
//
// raid.sync_pct is reported on every run for every array, never only while
// a sync runs: a value the agent stops sending turns stale and leaves the
// host unknown, so a metric that came and went with each resync or check
// would do that after the first one. It is the running resync, recovery,
// check, repair or reshape's progress; 0 while one is queued
// (resync=PENDING or DELAYED); 100 when none runs.
//
// State: inactive wins; else a running or queued (PENDING/DELAYED) sync
// names the state (recovery is recovering, resync resyncing, check and
// repair checking, reshape reshaping) — so a degraded array that is
// rebuilding reads recovering, not degraded; else degraded when a member is
// missing or failed; else clean. raid0 and linear have no redundancy
// brackets: they are clean unless inactive or a member is failed.
//
// Counts: devices_total and devices_active are the n and m of mdstat's
// [n/m]; without it (raid0, linear, inactive arrays) total is the member
// count and active the members that are neither failed nor spare.
// devices_failed counts members marked (F). Spares (S) count toward neither
// active nor failed.
//
// A failed spare (F) marks the array degraded even when [n/n] shows every
// slot up. Write-mostly (W) members count as normal members. An external-
// metadata container (Intel RST/VROC "super external:imsm", DDF) is inactive
// by design and is skipped: it is neither an item nor counted in
// raid.arrays; its subarrays ("super external:/md127/0") are reported
// normally. A header cut off after the name ("md0 :") is skipped.
//
// raid.arrays on item "" is the number of arrays reported this round, after
// the "arrays" filter; it is emitted on every run, including 0, so a rule
// can notice an array that vanished. A host without /proc/mdstat (no md
// module) has zero arrays. Off Linux the plugin reports one collection
// error on that metric: not supported on this platform.
package raid

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// mdstatReader returns the raw /proc/mdstat content (at most 1 MiB). A
// missing file is reported as an error wrapping fs.ErrNotExist.
type mdstatReader interface {
	Read(ctx context.Context) ([]byte, error)
}

type raidPlugin struct {
	Source mdstatReader
}

// New returns the raid plugin wired to /proc/mdstat (a stub off Linux).
func New() checks.Plugin {
	return raidPlugin{Source: newSource()}
}

func (raidPlugin) Name() string { return "raid" }

func (p raidPlugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Source == nil {
		return nil, nil
	}
	filter, _, err := checks.ParamStrings(params, "arrays")
	if err != nil {
		return nil, fmt.Errorf("raid: %w", err)
	}
	var want map[string]bool
	if len(filter) > 0 {
		want = make(map[string]bool, len(filter))
		for _, s := range filter {
			want[s] = true
		}
	}

	data, readErr := p.Source.Read(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var arrays []array
	switch {
	case readErr == nil:
		arrays = parseMdstat(data)
	case errors.Is(readErr, fs.ErrNotExist):
		// No md module loaded: no arrays, not a fault.
	default:
		e := fmt.Errorf("raid: %w", readErr)
		return []checks.Observation{checks.ErrObservation("", "raid.arrays", e)}, e
	}

	var (
		obs  []checks.Observation
		errs []error
		seen = map[string]bool{}
		n    int
	)
	for _, a := range arrays {
		if (want != nil && !want[a.name]) || seen[a.name] {
			continue
		}
		seen[a.name] = true
		if !checks.ReportableItem(a.name) {
			errs = append(errs, fmt.Errorf("raid: array %q cannot be reported: longer than %d bytes, not valid UTF-8 or carrying a control character", a.name, checks.MaxItemLen))
			continue
		}
		n++
		obs = append(obs,
			checks.LabelObservation(a.name, "raid.state", a.state),
			checks.ValueObservation(a.name, "raid.devices_total", float64(a.total)),
			checks.ValueObservation(a.name, "raid.devices_active", float64(a.active)),
			checks.ValueObservation(a.name, "raid.devices_failed", float64(a.failed)),
			checks.ValueObservation(a.name, "raid.sync_pct", a.syncPct),
		)
	}
	obs = append(obs, checks.ValueObservation("", "raid.arrays", float64(n)))
	return obs, errors.Join(errs...)
}

var _ checks.Plugin = raidPlugin{}
