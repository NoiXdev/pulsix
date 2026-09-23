// Package facts collects the host facts the Pulsix agent reports to the
// manager (v0.3a plan, Task 8): operating system, kernel, architecture,
// CPU, memory, virtualization, agent version, hostname and IP addresses.
// The manager stores the map in hosts.facts (merged) and derives the
// os/arch/virt automatic tags from it.
//
// Every piece of information is collected independently through a narrow
// provider interface — exactly the slice of gopsutil v4 collection uses —
// so a fault in one piece (a broken /proc mount, a platform quirk)
// contributes nothing while every other piece still reports. The map is
// authoritative and the joined error advisory: the caller should always
// report whatever map came back and log the error, never skip the push
// because one probe hiccuped. Only a dead context fails the whole
// collection.
//
// The provider seam is what keeps the tests offline: fixture providers
// stand in for gopsutil, so the tables never touch a real system. The
// live gopsutil path is exercised end to end by the v0.3a e2e gate (agent
// container on Linux, Task 11).
package facts

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

// The plan's fact keys (binding). Values are plain strings; an empty
// value means "unknown" and is simply absent from the map.
const (
	KeyOS           = "os"
	KeyOSVersion    = "os_version"
	KeyKernel       = "kernel"
	KeyArch         = "arch"
	KeyCPUModel     = "cpu_model"
	KeyCPUCount     = "cpu_count"
	KeyMemTotalMB   = "mem_total_mb"
	KeyVirt         = "virt"
	KeyAgentVersion = "agent_version"
	KeyHostname     = "hostname"
	KeyIPs          = "ips"
)

// AgentVersion is reported as the agent_version fact. It is the build's
// ldflags injection point (cmd/agent, Task 9); a source build without one
// reports "dev".
var AgentVersion = "dev"

// The facts contract's bounds: keys are lowercase [a-z0-9_], 1..MaxKeyLen
// runes; values at most MaxValueLen characters; at most MaxKeys keys.
// The manager enforces the same bounds (agents.Service.RecordFacts) and
// REJECTS violations — the collector's normalization is what keeps a
// healthy push from ever being refused.
const (
	MaxKeyLen   = 32
	MaxValueLen = 512
	MaxKeys     = 64
)

// keyPattern is the key shape normalize guarantees and the manager
// accepts; invalid characters become '_' rather than dropping the entry.
var keyPattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// The narrow provider seam. gopsutil exposes package-level functions
// (InfoWithContext & co), so these interfaces are declared here over the
// shapes collection needs and satisfied by the default adapters below —
// tests inject fixtures instead of a live /proc.
type (
	// HostInfoer is the host.Info probe (os, platform, kernel,
	// virtualization, hostname).
	HostInfoer interface {
		Info(ctx context.Context) (*host.InfoStat, error)
	}
	// CPUInfoer is the cpu.Info probe (the model name).
	CPUInfoer interface {
		Info(ctx context.Context) ([]cpu.InfoStat, error)
	}
	// CPUCounter is the cpu.Counts probe (logical, as psutil counts).
	CPUCounter interface {
		Counts(ctx context.Context, logical bool) (int, error)
	}
	// MemoryInfoer is the mem.VirtualMemory probe (total bytes).
	MemoryInfoer interface {
		VirtualMemory(ctx context.Context) (*mem.VirtualMemoryStat, error)
	}
	// AddrsProvider is the interface-address probe (stdlib net.Addr
	// shapes, as net.InterfaceAddrs reports them).
	AddrsProvider interface {
		InterfaceAddrs(ctx context.Context) ([]net.Addr, error)
	}
)

// The default adapters — thin wrappers over the gopsutil package
// functions (the stdlib address probe), keeping the production wiring a
// one-liner and the test seam package-local.
type (
	gopsutilHost struct{}
	gopsutilCPU  struct{}
	gopsutilMem  struct{}
	stdlibAddrs  struct{}
)

func (gopsutilHost) Info(ctx context.Context) (*host.InfoStat, error) {
	return host.InfoWithContext(ctx)
}

func (gopsutilCPU) Info(ctx context.Context) ([]cpu.InfoStat, error) {
	return cpu.InfoWithContext(ctx)
}

func (gopsutilCPU) Counts(ctx context.Context, logical bool) (int, error) {
	return cpu.CountsWithContext(ctx, logical)
}

func (gopsutilMem) VirtualMemory(ctx context.Context) (*mem.VirtualMemoryStat, error) {
	return mem.VirtualMemoryWithContext(ctx)
}

func (stdlibAddrs) InterfaceAddrs(context.Context) ([]net.Addr, error) {
	return net.InterfaceAddrs()
}

// Collector gathers facts from its providers. A nil provider contributes
// nothing and no error (a partially built collector is legal — handy for
// focused tests); New wires the full production set.
type Collector struct {
	Host   HostInfoer
	CPU    CPUInfoer
	Counts CPUCounter
	Memory MemoryInfoer
	Addrs  AddrsProvider
	// Version becomes the agent_version fact (empty drops it).
	Version string
}

// New builds the default gopsutil-backed collector reporting version.
func New(version string) *Collector {
	return &Collector{
		Host:    gopsutilHost{},
		CPU:     gopsutilCPU{},
		Counts:  gopsutilCPU{},
		Memory:  gopsutilMem{},
		Addrs:   stdlibAddrs{},
		Version: version,
	}
}

// Collect runs one facts collection with the package defaults (gopsutil,
// AgentVersion as the agent_version fact).
func Collect(ctx context.Context) (map[string]string, error) {
	return New(AgentVersion).Collect(ctx)
}

// Collect gathers every piece through its own provider, tolerant of
// faults as the package comment describes. The returned map is always
// normalized (bounded keys and values); the returned error joins every
// piece fault and is advisory — the map is authoritative.
func (c *Collector) Collect(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw := make(map[string]string, 11)
	var errs []error
	if info, err := c.hostInfo(ctx); err != nil {
		errs = append(errs, err)
	} else if info != nil {
		raw[KeyOS] = info.OS
		raw[KeyOSVersion] = osVersion(info)
		raw[KeyKernel] = info.KernelVersion
		raw[KeyVirt] = info.VirtualizationSystem
		raw[KeyHostname] = info.Hostname
	}
	raw[KeyArch] = runtime.GOARCH
	if model, err := c.cpuModel(ctx); err != nil {
		errs = append(errs, err)
	} else {
		raw[KeyCPUModel] = model
	}
	if n, err := c.cpuCount(ctx); err != nil {
		errs = append(errs, err)
	} else {
		raw[KeyCPUCount] = n
	}
	if mb, err := c.memTotal(ctx); err != nil {
		errs = append(errs, err)
	} else {
		raw[KeyMemTotalMB] = mb
	}
	if ips, err := c.reportableIPs(ctx); err != nil {
		errs = append(errs, err)
	} else {
		raw[KeyIPs] = strings.Join(ips, ",")
	}
	raw[KeyAgentVersion] = c.Version
	return normalize(raw), errors.Join(errs...)
}

// hostInfo is the one host.Info call behind five facts; its fault drops
// all of them (they share the single probe).
func (c *Collector) hostInfo(ctx context.Context) (*host.InfoStat, error) {
	if c.Host == nil {
		return nil, nil
	}
	info, err := c.Host.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("host info: %w", err)
	}
	return info, nil
}

// osVersion is platform + version ("ubuntu 22.04"); a missing version
// leaves the fact empty — "os_version" without a version is noise, and
// the platform family is what os already hints at.
func osVersion(info *host.InfoStat) string {
	if info.Platform == "" || info.PlatformVersion == "" {
		return ""
	}
	return info.Platform + " " + info.PlatformVersion
}

func (c *Collector) cpuModel(ctx context.Context) (string, error) {
	if c.CPU == nil {
		return "", nil
	}
	infos, err := c.CPU.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("cpu info: %w", err)
	}
	// The first CPU's model name, whitespace-trimmed (some platforms pad).
	for _, i := range infos {
		if m := strings.TrimSpace(i.ModelName); m != "" {
			return m, nil
		}
	}
	return "", nil
}

func (c *Collector) cpuCount(ctx context.Context) (string, error) {
	if c.Counts == nil {
		return "", nil
	}
	n, err := c.Counts.Counts(ctx, true) // logical, as psutil counts
	if err != nil {
		return "", fmt.Errorf("cpu counts: %w", err)
	}
	if n <= 0 {
		return "", nil
	}
	return strconv.Itoa(n), nil
}

func (c *Collector) memTotal(ctx context.Context) (string, error) {
	if c.Memory == nil {
		return "", nil
	}
	m, err := c.Memory.VirtualMemory(ctx)
	if err != nil {
		return "", fmt.Errorf("memory: %w", err)
	}
	if m == nil {
		return "", nil
	}
	return strconv.FormatUint((m.Total+1<<19)/(1<<20), 10), nil // bytes → MiB, half up
}

// reportableIPs extracts the addresses worth reporting: loopback and
// link-local (169.254/16, fe80::/10) never help target or group a host,
// the unspecified address is no address, nils and non-IP shapes are
// skipped. Duplicates collapse; the list sorts lexicographically so the
// value is stable across pushes.
func (c *Collector) reportableIPs(ctx context.Context) ([]string, error) {
	if c.Addrs == nil {
		return nil, nil
	}
	addrs, err := c.Addrs.InterfaceAddrs(ctx)
	if err != nil {
		return nil, fmt.Errorf("interface addresses: %w", err)
	}
	seen := make(map[string]bool, len(addrs))
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP == nil {
			continue
		}
		ip := ipnet.IP
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		s := ip.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

// normalize enforces the facts contract on whatever was collected:
// keys lowercased with punctuation mapped to '_' and clipped to
// MaxKeyLen runes, values clipped to MaxValueLen bytes on a rune
// boundary, empty values dropped (empty means unknown), at most MaxKeys
// entries. It is deterministic where map collisions appear: keys are
// walked in sorted order and the first occurrence wins.
func normalize(raw map[string]string) map[string]string {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if len(out) >= MaxKeys {
			break
		}
		key := sanitizeKey(k)
		if key == "" {
			continue
		}
		if _, exists := out[key]; exists {
			continue
		}
		value := clampValue(raw[k])
		if value == "" {
			continue
		}
		out[key] = value
	}
	return out
}

// sanitizeKey lowercases and keeps [a-z0-9_] (everything else becomes
// '_'), clipping to MaxKeyLen runes; an input that leaves nothing usable
// yields the empty key (the entry is dropped).
func sanitizeKey(k string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.ToLower(k) {
		if n == MaxKeyLen {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		n++
	}
	return b.String()
}

// clampValue clips v to MaxValueLen bytes without splitting a rune
// (facts values are platform strings; the clip is a guard, and the three
// tagged facts never approach the limit).
func clampValue(v string) string {
	if len(v) <= MaxValueLen {
		return v
	}
	last := 0
	for i := range v { // i is the byte offset of rune i
		if i > MaxValueLen {
			break
		}
		last = i
	}
	return v[:last]
}
