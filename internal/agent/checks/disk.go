package checks

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/shirou/gopsutil/v4/disk"
)

// pseudoFilesystems are skipped from the discovered mount set by default —
// a full tmpfs/devtmpfs/overlay/squashfs is not news, and reporting them
// unasked would just be a full /dev on every host. An explicit "mounts"
// allow-list bypasses this: naming a mount is an explicit request to watch
// it, pseudo or not.
var pseudoFilesystems = map[string]bool{
	"tmpfs":    true,
	"devtmpfs": true,
	"overlay":  true,
	"squashfs": true,
}

// partitionLister is the disk.Partitions probe (the mounted filesystem
// list disk discovery walks when no "mounts" allow-list is configured).
type partitionLister interface {
	Partitions(ctx context.Context, all bool) ([]disk.PartitionStat, error)
}

// usageStater is the disk.Usage probe, one mount point at a time — this is
// the per-item call whose fault must not cost any other mount.
type usageStater interface {
	Usage(ctx context.Context, path string) (*disk.UsageStat, error)
}

type gopsutilPartitions struct{}

func (gopsutilPartitions) Partitions(ctx context.Context, all bool) ([]disk.PartitionStat, error) {
	return disk.PartitionsWithContext(ctx, all)
}

type gopsutilUsage struct{}

func (gopsutilUsage) Usage(ctx context.Context, path string) (*disk.UsageStat, error) {
	return disk.UsageWithContext(ctx, path)
}

// diskPlugin reports per-mount metrics (item = mount point): disk.used_pct,
// disk.free_bytes, disk.inodes_used_pct.
type diskPlugin struct {
	Partitions partitionLister
	Usage      usageStater
}

func newDiskPlugin() Plugin {
	return diskPlugin{Partitions: gopsutilPartitions{}, Usage: gopsutilUsage{}}
}

func (diskPlugin) Name() string { return "disk" }

// Collect resolves the mount set — the "mounts" param verbatim when given
// (bypassing the pseudo-filesystem filter, an explicit ask), otherwise
// every distinct mount point disk.Partitions reports with the pseudo types
// filtered out — then reads each mount's usage independently: one
// unreadable mount becomes that mount's joined error, never the whole
// plugin's.
func (p diskPlugin) Collect(ctx context.Context, params map[string]any) ([]Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Usage == nil {
		return nil, nil
	}

	mounts, explicit, err := paramStrings(params, "mounts")
	if err != nil {
		return nil, fmt.Errorf("disk: %w", err)
	}

	var candidates []string
	if explicit {
		candidates = mounts
	} else {
		if p.Partitions == nil {
			return nil, nil
		}
		parts, err := p.Partitions.Partitions(ctx, true)
		if err != nil {
			return nil, fmt.Errorf("disk: partitions: %w", err)
		}
		seen := make(map[string]bool, len(parts))
		for _, part := range parts {
			if part.Mountpoint == "" || pseudoFilesystems[part.Fstype] || seen[part.Mountpoint] {
				continue
			}
			seen[part.Mountpoint] = true
			candidates = append(candidates, part.Mountpoint)
		}
		sort.Strings(candidates)
	}

	var obs []Observation
	var errs []error
	for _, mp := range candidates {
		usage, err := p.Usage.Usage(ctx, mp)
		if err != nil {
			errs = append(errs, fmt.Errorf("disk: mount %q: %w", mp, err))
			continue
		}
		if usage == nil {
			continue
		}
		obs = append(obs,
			valueObservation(mp, "disk.used_pct", usage.UsedPercent),
			valueObservation(mp, "disk.free_bytes", float64(usage.Free)),
			valueObservation(mp, "disk.inodes_used_pct", usage.InodesUsedPercent),
		)
	}

	return obs, errors.Join(errs...)
}

var _ Plugin = diskPlugin{}
