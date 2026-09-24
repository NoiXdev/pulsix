// Package facts_test: the collector runs on injected fixture providers —
// no gopsutil system probing, no /proc — so the tables below are offline
// and deterministic on every platform.
package facts_test

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/noixdev/pulsix/internal/agent/facts"
)

// --- fixture providers --------------------------------------------------

type fixtureHost struct {
	info *host.InfoStat
	err  error
}

func (f fixtureHost) Info(context.Context) (*host.InfoStat, error) { return f.info, f.err }

type fixtureCPU struct {
	infos []cpu.InfoStat
	err   error
}

func (f fixtureCPU) Info(context.Context) ([]cpu.InfoStat, error) { return f.infos, f.err }

type fixtureCounts struct {
	n   int
	err error
}

func (f fixtureCounts) Counts(_ context.Context, _ bool) (int, error) { return f.n, f.err }

type fixtureMem struct {
	stat *mem.VirtualMemoryStat
	err  error
}

func (f fixtureMem) VirtualMemory(context.Context) (*mem.VirtualMemoryStat, error) {
	return f.stat, f.err
}

type fixtureAddrs struct {
	addrs []net.Addr
	err   error
}

func (f fixtureAddrs) InterfaceAddrs(context.Context) ([]net.Addr, error) { return f.addrs, f.err }

// cidr builds one interface address the way net.InterfaceAddrs reports it
// (an *net.IPNet carrying the address and its prefix mask).
func cidr(ip string) net.Addr {
	return &net.IPNet{IP: net.ParseIP(ip), Mask: net.CIDRMask(24, 32)}
}

// --- the full map -------------------------------------------------------

// TestCollectReportsEveryFact walks the happy path: every provider answers
// and the map carries the plan's complete fact key set.
func TestCollectReportsEveryFact(t *testing.T) {
	got, err := fullCollector().Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := map[string]string{
		"os":            "linux",
		"os_version":    "ubuntu 22.04",
		"kernel":        "5.15.0-generic",
		"arch":          runtime.GOARCH,
		"cpu_model":     "AMD EPYC 7443P",
		"cpu_count":     "16",
		"mem_total_mb":  "32768",
		"virt":          "kvm",
		"agent_version": "v0.3.0-test",
		"hostname":      "web-1",
		"ips":           "10.1.2.3,192.168.7.9,2001:db8::5",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d facts %v, want %d", len(got), got, len(want))
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("fact %q = %q, want %q", k, got[k], w)
		}
	}
}

// TestCollectDropsAbsentPieces: empty platform data, an empty agent
// version and empty CPU model names mean "unknown" — the key is absent,
// never carried as an empty string.
func TestCollectDropsAbsentPieces(t *testing.T) {
	got, err := (&facts.Collector{
		Host:   fixtureHost{info: &host.InfoStat{Hostname: "bare", OS: "linux", Platform: "debian"}},
		Counts: fixtureCounts{n: 2},
	}).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, absent := range []string{"os_version", "kernel", "virt", "agent_version", "cpu_model", "mem_total_mb", "ips"} {
		if v, ok := got[absent]; ok {
			t.Errorf("absent fact %q reported as %q", absent, v)
		}
	}
	if got["hostname"] != "bare" || got["os"] != "linux" || got["cpu_count"] != "2" {
		t.Fatalf("present facts = %v", got)
	}
}

// --- failure tolerance --------------------------------------------------

// TestCollectToleratesProviderFaults: one failing piece never kills the
// push — its facts are absent, every other piece still reports, and the
// error is the joined piece fault (advisory; the map is authoritative).
// Host info is ONE collector call, so its fault drops all five facts it
// would have produced (os, os_version, kernel, virt, hostname).
func TestCollectToleratesProviderFaults(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name    string
		arm     func(c *facts.Collector)
		missing []string
		present map[string]string
	}{
		{
			name:    "host info fault drops its five facts",
			arm:     func(c *facts.Collector) { c.Host = fixtureHost{err: boom} },
			missing: []string{"os", "os_version", "kernel", "virt", "hostname"},
			present: map[string]string{"cpu_count": "8", "mem_total_mb": "1024", "ips": "10.0.0.1", "agent_version": "v1"},
		},
		{
			name:    "cpu model fault",
			arm:     func(c *facts.Collector) { c.CPU = fixtureCPU{err: boom} },
			missing: []string{"cpu_model"},
			present: map[string]string{"cpu_count": "8", "mem_total_mb": "1024", "ips": "10.0.0.1"},
		},
		{
			name:    "cpu count fault",
			arm:     func(c *facts.Collector) { c.Counts = fixtureCounts{err: boom} },
			missing: []string{"cpu_count"},
			present: map[string]string{"cpu_model": "m", "mem_total_mb": "1024", "ips": "10.0.0.1"},
		},
		{
			name:    "memory fault",
			arm:     func(c *facts.Collector) { c.Memory = fixtureMem{err: boom} },
			missing: []string{"mem_total_mb"},
			present: map[string]string{"cpu_model": "m", "cpu_count": "8", "ips": "10.0.0.1"},
		},
		{
			name:    "address fault",
			arm:     func(c *facts.Collector) { c.Addrs = fixtureAddrs{err: boom} },
			missing: []string{"ips"},
			present: map[string]string{"cpu_model": "m", "cpu_count": "8", "mem_total_mb": "1024"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &facts.Collector{
				Host:    fixtureHost{info: &host.InfoStat{Hostname: "h", OS: "linux"}},
				CPU:     fixtureCPU{infos: []cpu.InfoStat{{ModelName: "m"}}},
				Counts:  fixtureCounts{n: 8},
				Memory:  fixtureMem{stat: &mem.VirtualMemoryStat{Total: 1 << 30}},
				Addrs:   fixtureAddrs{addrs: []net.Addr{cidr("10.0.0.1")}},
				Version: "v1",
			}
			tc.arm(c)
			got, err := c.Collect(context.Background())
			if err == nil {
				t.Fatal("a provider fault must surface through the joined error")
			}
			if !strings.Contains(err.Error(), "boom") {
				t.Errorf("joined error %v must carry the piece fault", err)
			}
			for _, k := range tc.missing {
				if v, ok := got[k]; ok {
					t.Errorf("faulty piece still reported %q=%q", k, v)
				}
			}
			for k, w := range tc.present {
				if got[k] != w {
					t.Errorf("fact %q = %q, want %q", k, got[k], w)
				}
			}
		})
	}
}

// TestCollectNilProvidersContributeNothing: a partially built Collector is
// legal — nil pieces are silent, not errors.
func TestCollectNilProvidersContributeNothing(t *testing.T) {
	got, err := (&facts.Collector{Version: "v1"}).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := map[string]string{"arch": runtime.GOARCH, "agent_version": "v1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("fact %q = %q, want %q", k, got[k], w)
		}
	}
}

// TestCollectContextCancelled fails the whole collection: with no context
// left there is nothing to report at all.
func TestCollectContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := fullCollector().Collect(ctx)
	if got != nil {
		t.Fatalf("cancelled collection returned %v", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// --- memory rounding ----------------------------------------------------

// TestCollectMemTotalRoundsToNearestMB: bytes → nearest MiB, half up —
// a 16 GiB machine reads 16384, a 8.5 MiB oddity reads 9.
func TestCollectMemTotalRoundsToNearestMB(t *testing.T) {
	cases := []struct {
		total uint64
		want  string
	}{
		{total: 8 * 1024 * 1024, want: "8"},
		{total: 8*1024*1024 + 512*1024, want: "9"}, // 8.5 MiB, half rounds up
		{total: 3*1024*1024 + 400*1024, want: "3"}, // 3.39 → 3
		{total: 3*1024*1024 + 700*1024, want: "4"}, // 3.68 → 4
		{total: 16 * 1024 * 1024 * 1024, want: "16384"},
	}
	for _, tc := range cases {
		c := &facts.Collector{Memory: fixtureMem{stat: &mem.VirtualMemoryStat{Total: tc.total}}}
		got, err := c.Collect(context.Background())
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if got["mem_total_mb"] != tc.want {
			t.Errorf("total %d → %q, want %q", tc.total, got["mem_total_mb"], tc.want)
		}
	}
}

// --- the ip list --------------------------------------------------------

// TestCollectIPsSortedDeduped: loopback, link-local (IPv4 + IPv6) and the
// unspecified address never help target or group a host and are skipped;
// duplicates collapse; the list is sorted for a stable value.
func TestCollectIPsSortedDeduped(t *testing.T) {
	c := &facts.Collector{Addrs: fixtureAddrs{addrs: []net.Addr{
		cidr("192.168.1.20"),
		cidr("10.0.0.2"),
		cidr("127.0.0.1"),
		cidr("::1"),
		cidr("fe80::1"),
		cidr("169.254.100.7"),
		cidr("0.0.0.0"),
		cidr("10.0.0.2"),
	}}}
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got["ips"] != "10.0.0.2,192.168.1.20" {
		t.Fatalf("ips = %q", got["ips"])
	}
}

// --- bounds -------------------------------------------------------------

// TestCollectClampsOverlongValues: every value the collector emits is at
// most MaxValueLen characters, whatever a platform reports.
func TestCollectClampsOverlongValues(t *testing.T) {
	long := strings.Repeat("x", facts.MaxValueLen+200)
	c := &facts.Collector{
		Host:    fixtureHost{info: &host.InfoStat{Hostname: long, OS: "linux", KernelVersion: long}},
		Version: long,
	}
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	for k, v := range got {
		if len(v) > facts.MaxValueLen {
			t.Errorf("fact %q is %d chars, above the %d bound", k, len(v), facts.MaxValueLen)
		}
	}
	if got["hostname"] != long[:facts.MaxValueLen] {
		t.Error("an over-long value must be clipped, not dropped or carried whole")
	}
}

// --- the package-level entry point --------------------------------------

// TestPackageCollectUsesDefaultProviders: the real gopsutil path is
// exercised for its hard guarantees (a hostname, an arch, the injected
// version); the full live fact set is the v0.3a e2e gate's business
// (Task 11, agent container on Linux).
func TestPackageCollectUsesDefaultProviders(t *testing.T) {
	// AgentVersion is package state the binary stamps once at startup;
	// restore it so a later test in this package never reads this one's
	// value.
	restore := facts.AgentVersion
	t.Cleanup(func() { facts.AgentVersion = restore })
	facts.AgentVersion = "build-id-42"
	got, err := facts.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect on this machine: %v", err)
	}
	if got["agent_version"] != "build-id-42" {
		t.Fatalf("agent_version = %q", got["agent_version"])
	}
	if got["hostname"] == "" {
		t.Error("the live collector must report a hostname")
	}
	if _, ok := got["arch"]; !ok {
		t.Error("the live collector must report arch")
	}
}

// --- full collector helper ----------------------------------------------

func fullCollector() *facts.Collector {
	return &facts.Collector{
		Host: fixtureHost{info: &host.InfoStat{
			Hostname: "web-1", OS: "linux", Platform: "ubuntu", PlatformVersion: "22.04",
			KernelVersion: "5.15.0-generic", VirtualizationSystem: "kvm",
		}},
		CPU: fixtureCPU{infos: []cpu.InfoStat{
			{ModelName: "  AMD EPYC 7443P "},
			{ModelName: "second entry ignored"},
		}},
		Counts: fixtureCounts{n: 16},
		Memory: fixtureMem{stat: &mem.VirtualMemoryStat{Total: 32 * 1024 * 1024 * 1024}},
		Addrs: fixtureAddrs{addrs: []net.Addr{
			cidr("127.0.0.1"),
			cidr("fe80::1"),
			cidr("169.254.9.9"),
			cidr("0.0.0.0"),
			cidr("10.1.2.3"),
			cidr("10.1.2.3"),
			cidr("192.168.7.9"),
			cidr("2001:db8::5"),
		}},
		Version: "v0.3.0-test",
	}
}

// Compile-time proof the fixture providers satisfy the ports (keeps the
// seam shapes pinned as gopsutil evolves).
var (
	_ facts.HostInfoer    = fixtureHost{}
	_ facts.CPUInfoer     = fixtureCPU{}
	_ facts.CPUCounter    = fixtureCounts{}
	_ facts.MemoryInfoer  = fixtureMem{}
	_ facts.AddrsProvider = fixtureAddrs{}
)
