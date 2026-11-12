package checks

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/net"
)

func TestCPUOnWindowsReportsNoLoad(t *testing.T) {
	p := newCPUPluginFor("windows")
	if p.Load != nil {
		t.Fatal("windows cpu plugin has a load probe")
	}
	p.Percent = fixturePercent{pct: []float64{12.5}}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 || obs[0].Metric != "cpu.used_pct" {
		t.Fatalf("obs = %+v, want only cpu.used_pct", obs)
	}
}

func TestCPUOnDarwinKeepsLoad(t *testing.T) {
	p := newCPUPluginFor("darwin")
	if p.Load == nil {
		t.Fatal("darwin cpu plugin has no load probe")
	}
	p.Load = fixtureLoad{avg: &load.AvgStat{Load1: 1, Load5: 2, Load15: 3}}
	p.Percent = fixturePercent{pct: []float64{5}}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 4 {
		t.Fatalf("obs = %+v, want load1/5/15 and used_pct", obs)
	}
}

func TestDiscoveredMountDarwin(t *testing.T) {
	tests := []struct {
		mount, fstype string
		want          bool
	}{
		{"/", "apfs", true},
		{"/System/Volumes/Data", "apfs", true},
		{"/Volumes/Backup", "apfs", true},
		{"/System/Volumes/VM", "apfs", false},
		{"/System/Volumes/Preboot", "apfs", false},
		{"/System/Volumes/Update/mnt1", "apfs", false},
		{"/dev", "devfs", false},
		{"/Volumes/Auto", "autofs", false},
		{"/Volumes/Null", "nullfs", false},
		{"/private/var/vm", "apfs", false},
	}
	for _, tt := range tests {
		t.Run(tt.mount, func(t *testing.T) {
			got := discoveredMount("darwin", disk.PartitionStat{Mountpoint: tt.mount, Fstype: tt.fstype}, nil)
			if got != tt.want {
				t.Errorf("discoveredMount(darwin, %s %s) = %v, want %v", tt.mount, tt.fstype, got, tt.want)
			}
		})
	}
}

func fakeDriveKind(m string) string {
	switch m {
	case "C:":
		return "fixed"
	case "D:":
		return "cdrom"
	case "Z:":
		return "remote"
	}
	return "other"
}

func TestDiscoveredMountWindows(t *testing.T) {
	for m, want := range map[string]bool{"C:": true, "D:": false, "Z:": false} {
		if got := discoveredMount("windows", disk.PartitionStat{Mountpoint: m, Fstype: "NTFS"}, fakeDriveKind); got != want {
			t.Errorf("discoveredMount(windows, %s) = %v, want %v", m, got, want)
		}
	}
	if discoveredMount("windows", disk.PartitionStat{Mountpoint: "C:"}, nil) {
		t.Error("a nil DriveKind must keep nothing")
	}
}

func windowsDisk(usage fixtureUsage) diskPlugin {
	return diskPlugin{
		GOOS:      "windows",
		DriveKind: fakeDriveKind,
		Partitions: fixturePartitions{parts: []disk.PartitionStat{
			{Mountpoint: "C:", Fstype: "NTFS"}, {Mountpoint: "D:", Fstype: "CDFS"}, {Mountpoint: "Z:", Fstype: "NTFS"},
		}},
		Usage: usage,
	}
}

func TestDiskOnWindowsOmitsInodes(t *testing.T) {
	p := windowsDisk(fixtureUsage{byMount: map[string]diskUsageResult{
		"C:": {stat: &disk.UsageStat{UsedPercent: 40, Free: 100, InodesUsedPercent: 0}},
	}})
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 || obs[0].Item != "C:" || obs[0].Metric != "disk.used_pct" || obs[1].Metric != "disk.free_bytes" {
		t.Fatalf("obs = %+v, want exactly used_pct and free_bytes for C:", obs)
	}
}

func TestDiskOnWindowsUnreadableDriveOmitsInodesError(t *testing.T) {
	p := windowsDisk(fixtureUsage{byMount: map[string]diskUsageResult{}})
	obs, err := p.Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("want the read error")
	}
	if got := metricItems(obs, "disk.inodes_used_pct"); len(got) != 0 {
		t.Errorf("inodes observations = %v, want none", got)
	}
	if len(obs) != 2 {
		t.Errorf("obs = %+v, want error observations for used_pct and free_bytes only", obs)
	}
}

func TestDiskExplicitMountsBypassTheWindowsFilter(t *testing.T) {
	p := windowsDisk(fixtureUsage{byMount: map[string]diskUsageResult{
		"D:": {stat: &disk.UsageStat{UsedPercent: 100, Free: 0}},
	}})
	obs, err := p.Collect(context.Background(), map[string]any{"mounts": []string{"D:"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := metricItems(obs, "disk.used_pct"); !reflect.DeepEqual(got, []string{"D:"}) {
		t.Fatalf("used_pct items = %v, want [D:]", got)
	}
}

func TestExcludedInterfacesPerSystem(t *testing.T) {
	up := func(name string) net.InterfaceStat { return iface(name, "up", "broadcast") }
	tests := []struct {
		goos, name string
		want       bool
	}{
		{"darwin", "utun3", false}, {"darwin", "awdl0", false}, {"darwin", "bridge100", false}, {"darwin", "en0", true},
		{"windows", "isatap.{ABC-123}", false}, {"windows", "Local Area Connection* 2", false}, {"windows", "Ethernet 2", true},
		{"linux", "ap0", true}, {"linux", "docker0", false},
	}
	for _, tt := range tests {
		got := discoveredInterface(up(tt.name), excludedInterfacePrefixesFor(tt.goos))
		if got != tt.want {
			t.Errorf("%s %q discovered = %v, want %v", tt.goos, tt.name, got, tt.want)
		}
	}
}

func TestNewInstanceKeepsExcludedPrefixes(t *testing.T) {
	p := newNetworkPlugin().(*networkPlugin)
	p.ExcludedPrefixes = []string{"x"}
	if got := p.newInstance().(*networkPlugin).ExcludedPrefixes; !strings.EqualFold(strings.Join(got, ","), "x") {
		t.Errorf("instance prefixes = %v", got)
	}
}

func TestReportsInodes(t *testing.T) {
	if reportsInodes("windows") || !reportsInodes("linux") || !reportsInodes("darwin") {
		t.Error("only windows lacks inodes")
	}
}
