// package checks: the disk plugin's tests run entirely against injected
// fixtures — no real disk.Partitions or disk.Usage call.
package checks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
)

type fixturePartitions struct {
	parts []disk.PartitionStat
	err   error
}

func (f fixturePartitions) Partitions(context.Context, bool) ([]disk.PartitionStat, error) {
	return f.parts, f.err
}

// fixtureUsage answers disk.Usage per mount point from a map, so a test can
// arm exactly one mount to fail without touching the others.
type fixtureUsage struct {
	byMount map[string]diskUsageResult
}

type diskUsageResult struct {
	stat *disk.UsageStat
	err  error
}

func (f fixtureUsage) Usage(_ context.Context, path string) (*disk.UsageStat, error) {
	r, ok := f.byMount[path]
	if !ok {
		return nil, fmt.Errorf("fixture: no usage armed for %q", path)
	}
	return r.stat, r.err
}

func metricItems(obs []Observation, metric string) []string {
	var items []string
	for _, o := range obs {
		if o.Metric == metric {
			items = append(items, o.Item)
		}
	}
	sort.Strings(items)
	return items
}

func TestDiskCollectDiscoveryFiltersPseudoFilesystems(t *testing.T) {
	p := diskPlugin{
		Partitions: fixturePartitions{parts: []disk.PartitionStat{
			{Mountpoint: "/", Fstype: "ext4"},
			{Mountpoint: "/data", Fstype: "xfs"},
			{Mountpoint: "/dev", Fstype: "devtmpfs"},
			{Mountpoint: "/dev/shm", Fstype: "tmpfs"},
			{Mountpoint: "/var/lib/docker/overlay2/abc/merged", Fstype: "overlay"},
			{Mountpoint: "/snap/core/1", Fstype: "squashfs"},
		}},
		Usage: fixtureUsage{byMount: map[string]diskUsageResult{
			"/":     {stat: &disk.UsageStat{UsedPercent: 10, Free: 100, InodesUsedPercent: 1}},
			"/data": {stat: &disk.UsageStat{UsedPercent: 20, Free: 200, InodesUsedPercent: 2}},
		}},
	}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := metricItems(obs, "disk.used_pct")
	want := []string{"/", "/data"}
	if len(got) != len(want) {
		t.Fatalf("mounts observed = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mounts observed = %v, want %v", got, want)
		}
	}
	if len(obs) != 6 { // 3 metrics * 2 real mounts
		t.Fatalf("got %d observations, want 6: %+v", len(obs), obs)
	}
}

func TestDiskCollectPartitionsFaultFailsTheWholePlugin(t *testing.T) {
	boom := errors.New("boom")
	p := diskPlugin{
		Partitions: fixturePartitions{err: boom},
		Usage:      fixtureUsage{byMount: map[string]diskUsageResult{}},
	}
	obs, err := p.Collect(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want it to carry boom", err)
	}
	if obs != nil {
		t.Fatalf("obs = %+v, want none — there is no mount set to iterate", obs)
	}
}

// TestDiskCollectOneUnreadableMountDoesNotLoseTheOthers is the
// failure-tolerance case the brief calls out by name: a mount that errors
// must not cost the mounts that succeeded.
func TestDiskCollectOneUnreadableMountDoesNotLoseTheOthers(t *testing.T) {
	boom := errors.New("permission denied")
	p := diskPlugin{
		Usage: fixtureUsage{byMount: map[string]diskUsageResult{
			"/":       {stat: &disk.UsageStat{UsedPercent: 33, Free: 999, InodesUsedPercent: 4}},
			"/broken": {err: boom},
			"/var":    {stat: &disk.UsageStat{UsedPercent: 44, Free: 111, InodesUsedPercent: 5}},
		}},
	}
	obs, err := p.Collect(context.Background(), map[string]any{"mounts": []string{"/", "/broken", "/var"}})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want it to carry the broken mount's fault", err)
	}
	if !strings.Contains(err.Error(), "/broken") {
		t.Errorf("err = %v, must name the mount that failed", err)
	}
	got := metricItems(obs, "disk.used_pct")
	want := []string{"/", "/var"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("mounts observed = %v, want %v (the failed mount excluded, the others intact)", got, want)
	}
}

func TestDiskCollectExplicitMountsBypassesDiscoveryAndPseudoFilter(t *testing.T) {
	// Partitions is armed to fail — proof the explicit allow-list path never
	// consults it at all.
	p := diskPlugin{
		Partitions: fixturePartitions{err: errors.New("must not be called")},
		Usage: fixtureUsage{byMount: map[string]diskUsageResult{
			"/dev/shm": {stat: &disk.UsageStat{UsedPercent: 1, Free: 2, InodesUsedPercent: 3}},
		}},
	}
	obs, err := p.Collect(context.Background(), map[string]any{"mounts": []string{"/dev/shm"}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 3 {
		t.Fatalf("got %d observations, want 3: %+v", len(obs), obs)
	}
	findObs(t, obs, "/dev/shm", "disk.used_pct")
}

func TestDiskCollectExplicitEmptyMountsCollectsNothing(t *testing.T) {
	p := diskPlugin{
		Partitions: fixturePartitions{err: errors.New("must not be called")},
		Usage:      fixtureUsage{byMount: map[string]diskUsageResult{}},
	}
	obs, err := p.Collect(context.Background(), map[string]any{"mounts": []string{}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("obs = %+v, want none", obs)
	}
}

func TestDiskCollectAcceptsJSONRoundTripMountsShape(t *testing.T) {
	p := diskPlugin{
		Usage: fixtureUsage{byMount: map[string]diskUsageResult{
			"/": {stat: &disk.UsageStat{UsedPercent: 1, Free: 2, InodesUsedPercent: 3}},
		}},
	}
	obs, err := p.Collect(context.Background(), map[string]any{"mounts": []any{"/"}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	findObs(t, obs, "/", "disk.used_pct")
}

func TestDiskCollectMalformedMountsParamIsAnError(t *testing.T) {
	p := diskPlugin{Usage: fixtureUsage{byMount: map[string]diskUsageResult{}}}
	_, err := p.Collect(context.Background(), map[string]any{"mounts": 5})
	if err == nil {
		t.Fatal("a malformed mounts param must be an error")
	}
}

func TestDiskCollectNilProvidersContributeNothing(t *testing.T) {
	obs, err := (diskPlugin{}).Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("obs = %+v, want none", obs)
	}
}

func TestDiskCollectContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := diskPlugin{
		Partitions: fixturePartitions{},
		Usage:      fixtureUsage{byMount: map[string]diskUsageResult{}},
	}
	obs, err := p.Collect(ctx, nil)
	if obs != nil {
		t.Fatalf("cancelled collection returned %v", obs)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestDiskName(t *testing.T) {
	if (diskPlugin{}).Name() != "disk" {
		t.Fatalf("Name() = %q, want disk", (diskPlugin{}).Name())
	}
}

var (
	_ partitionLister = fixturePartitions{}
	_ usageStater     = fixtureUsage{}
	_ Plugin          = diskPlugin{}
)
