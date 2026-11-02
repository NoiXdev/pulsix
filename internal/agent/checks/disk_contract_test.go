package checks

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"

	managerchecks "github.com/noixdev/pulsix/internal/checks"
)

// TestDiskSkipsMountsTheManagerWouldRefuse is backlog 7.40. The manager
// refuses a whole check-results batch, and ends the stream, when one item
// breaks its contract (at most 512 bytes, no control characters or line
// separators), and a mount point is a path whoever can mount may choose. A
// mount the agent cannot name inside the contract is left out, so it
// costs every other mount and check of the host nothing.
func TestDiskSkipsMountsTheManagerWouldRefuse(t *testing.T) {
	long := "/mnt/" + strings.Repeat("d", managerchecks.MaxItemLen)
	usage := &disk.UsageStat{UsedPercent: 10, Free: 1, InodesUsedPercent: 2}
	p := diskPlugin{
		Partitions: fixturePartitions{parts: []disk.PartitionStat{
			{Mountpoint: "/", Fstype: "ext4"},
			{Mountpoint: long, Fstype: "fuse.sshfs"},
			{Mountpoint: "/mnt/tab\there", Fstype: "fuse.sshfs"},
			{Mountpoint: "/mnt/line sep", Fstype: "fuse.sshfs"},
		}},
		Usage: fixtureUsage{byMount: map[string]diskUsageResult{
			"/": {stat: usage}, long: {stat: usage}, "/mnt/tab\there": {stat: usage}, "/mnt/line sep": {stat: usage},
		}},
	}

	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("Collect = %v; a mount it cannot report is not a fault", err)
	}
	if got := metricItems(obs, "disk.used_pct"); !slices.Equal(got, []string{"/"}) {
		t.Fatalf("reported mounts = %q, want only /", got)
	}
}

// TestReportableItemMatchesTheManagersContract keeps the agent's copy of
// the item bound on the manager's.
func TestReportableItemMatchesTheManagersContract(t *testing.T) {
	if maxItemLen != managerchecks.MaxItemLen {
		t.Fatalf("maxItemLen = %d, the manager's contract says %d", maxItemLen, managerchecks.MaxItemLen)
	}
	for item, want := range map[string]bool{
		"/":                      true,
		"/srv/data":              true,
		strings.Repeat("a", 512): true,
		strings.Repeat("a", 513): false,
		"/a\tb":                  false,
		"/a\nb":                  false,
		"/a b":                   false,
		"/a\x7fb":                false,
	} {
		if got := reportableItem(item); got != want {
			t.Errorf("reportableItem(%q) = %v, want %v", item, got, want)
		}
	}
}
