package disk

import (
	"context"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shirou/gopsutil/v4/disk"
	"google.golang.org/protobuf/proto"

	"github.com/noixdev/pulsix/internal/agent/checks"
	managerchecks "github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
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
		GOOS: "linux",
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
	if checks.MaxItemLen != managerchecks.MaxItemLen {
		t.Fatalf("MaxItemLen = %d, the manager's contract says %d", checks.MaxItemLen, managerchecks.MaxItemLen)
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
		if got := checks.ReportableItem(item); got != want {
			t.Errorf("ReportableItem(%q) = %v, want %v", item, got, want)
		}
	}
}

// TestReportableItemRefusesInvalidUTF8 is backlog 7.52. The item travels
// as a proto3 string, and gRPC refuses to marshal one that is not valid
// UTF-8 before anything reaches the wire, tearing the stream down — so a
// mount point that is not valid UTF-8 is outside the contract as surely as
// one carrying a control character.
func TestReportableItemRefusesInvalidUTF8(t *testing.T) {
	if checks.ReportableItem("/home/u/q\"\xffz") {
		t.Error("ReportableItem accepted a mount point that is not valid UTF-8")
	}
	if !checks.ReportableItem("/home/u/ok") {
		t.Error("ReportableItem refused a plain mount point")
	}
	if !checks.ReportableItem("/mnt/données") {
		t.Error("ReportableItem refused a mount point that is valid non-ASCII UTF-8")
	}
}

// TestDiskCollectSkipsInvalidUTF8Mount: gopsutil unescapes a mountinfo
// mount point through strconv.Unquote and hands back the raw bytes when
// that fails, which a '"' in the path makes it do — so a byte 0xFF next to
// a quote reaches the plugin unchanged. Such a mount is left out, and
// everything Collect does return still encodes as the CheckResult it is
// sent as.
func TestDiskCollectSkipsInvalidUTF8Mount(t *testing.T) {
	bad := "/home/u/q\"\xffz"
	usage := &disk.UsageStat{UsedPercent: 10, Free: 1, InodesUsedPercent: 2}
	p := diskPlugin{
		GOOS: "linux",
		Partitions: fixturePartitions{parts: []disk.PartitionStat{
			{Mountpoint: "/", Fstype: "ext4"},
			{Mountpoint: bad, Fstype: "fuse.sshfs"},
		}},
		Usage: fixtureUsage{byMount: map[string]diskUsageResult{"/": {stat: usage}, bad: {stat: usage}}},
	}

	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("Collect = %v; a mount it cannot report is not a fault", err)
	}
	if got := metricItems(obs, "disk.used_pct"); !slices.Equal(got, []string{"/"}) {
		t.Fatalf("reported mounts = %q, want only /", got)
	}
	for _, o := range obs {
		if _, err := proto.Marshal(&agentpb.CheckResult{CheckKey: "disk", Item: o.Item, Metric: o.Metric}); err != nil {
			t.Fatalf("observation for item %q does not encode: %v", o.Item, err)
		}
	}
}

// TestDiskCollectExplicitInvalidUTF8MountIsAnErrorNotAnItem: a "mounts"
// allow-list naming a mount point that is not valid UTF-8 cannot be
// honoured; the plugin says so in its error, which must itself encode.
func TestDiskCollectExplicitInvalidUTF8MountIsAnErrorNotAnItem(t *testing.T) {
	bad := "/home/u/q\"\xffz"
	usage := &disk.UsageStat{UsedPercent: 10, Free: 1, InodesUsedPercent: 2}
	p := diskPlugin{GOOS: "linux", Usage: fixtureUsage{byMount: map[string]diskUsageResult{"/": {stat: usage}, bad: {stat: usage}}}}

	obs, err := p.Collect(context.Background(), map[string]any{"mounts": []string{"/", bad}})
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("Collect error = %v, want one naming the mount as not valid UTF-8", err)
	}
	if !utf8.ValidString(err.Error()) {
		t.Fatalf("the error names the mount in bytes that are not valid UTF-8: %q", err.Error())
	}
	if got := metricItems(obs, "disk.used_pct"); !slices.Equal(got, []string{"/"}) {
		t.Fatalf("reported mounts = %q, want only /", got)
	}
}
