package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

type fixtureNetCounters struct {
	stats []net.IOCountersStat
	err   error
}

func (f *fixtureNetCounters) IOCounters(context.Context, bool) ([]net.IOCountersStat, error) {
	return f.stats, f.err
}

type fixtureNetInterfaces struct {
	list net.InterfaceStatList
	err  error
}

func (f fixtureNetInterfaces) Interfaces(context.Context) (net.InterfaceStatList, error) {
	return f.list, f.err
}

type fakeNow struct{ t time.Time }

func (f *fakeNow) now() time.Time { return f.t }

func iface(name string, flags ...string) net.InterfaceStat {
	return net.InterfaceStat{Name: name, Flags: flags}
}

func counters(name string, n uint64) net.IOCountersStat {
	return net.IOCountersStat{
		Name: name, BytesRecv: n * 1000, BytesSent: n * 500,
		Errin: n * 4, Errout: n * 2, Dropin: n * 10, Dropout: n * 20,
	}
}

var netMetrics = []string{
	"net.rx_bytes_per_s", "net.tx_bytes_per_s", "net.rx_errors_per_s",
	"net.tx_errors_per_s", "net.rx_drops_per_s", "net.tx_drops_per_s",
}

func newTestNetwork(c *fixtureNetCounters, ifs fixtureNetInterfaces, clock *fakeNow) *networkPlugin {
	p := newNetworkPlugin().(*networkPlugin)
	p.Counters, p.Interfaces, p.Now = c, ifs, clock.now
	return p
}

func upIfaces(names ...string) fixtureNetInterfaces {
	var l net.InterfaceStatList
	for _, n := range names {
		l = append(l, iface(n, "up", "broadcast"))
	}
	return fixtureNetInterfaces{list: l}
}

func TestNetworkFirstCollectReportsNothing(t *testing.T) {
	clock := &fakeNow{t: time.Unix(1000, 0)}
	p := newTestNetwork(&fixtureNetCounters{stats: []net.IOCountersStat{counters("eth0", 10)}}, upIfaces("eth0"), clock)
	obs, err := p.Collect(context.Background(), nil)
	if err != nil || len(obs) != 0 {
		t.Fatalf("first collect = %+v, %v; want nothing", obs, err)
	}
}

func TestNetworkSecondCollectReportsRates(t *testing.T) {
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{stats: []net.IOCountersStat{counters("eth0", 10)}}
	p := newTestNetwork(c, upIfaces("eth0"), clock)
	_, _ = p.Collect(context.Background(), nil)

	clock.t = clock.t.Add(10 * time.Second)
	c.stats = []net.IOCountersStat{counters("eth0", 20)}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := map[string]float64{
		"net.rx_bytes_per_s":  1000,
		"net.tx_bytes_per_s":  500,
		"net.rx_errors_per_s": 4,
		"net.tx_errors_per_s": 2,
		"net.rx_drops_per_s":  10,
		"net.tx_drops_per_s":  20,
	}
	if len(obs) != 6 {
		t.Fatalf("got %d observations, want 6: %+v", len(obs), obs)
	}
	for m, w := range want {
		o := findObs(t, obs, "eth0", m)
		if o.Value == nil || *o.Value != w {
			t.Errorf("%s = %+v, want %v", m, o, w)
		}
	}
}

func TestNetworkCounterGoingBackwardsReportsNothingAndRebaselines(t *testing.T) {
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{stats: []net.IOCountersStat{counters("eth0", 100)}}
	p := newTestNetwork(c, upIfaces("eth0"), clock)
	_, _ = p.Collect(context.Background(), nil)

	clock.t = clock.t.Add(10 * time.Second)
	c.stats = []net.IOCountersStat{counters("eth0", 5)} // reset
	obs, _ := p.Collect(context.Background(), nil)
	if len(obs) != 0 {
		t.Fatalf("after a reset got %+v, want nothing", obs)
	}

	clock.t = clock.t.Add(10 * time.Second)
	c.stats = []net.IOCountersStat{counters("eth0", 15)}
	obs, _ = p.Collect(context.Background(), nil)
	o := findObs(t, obs, "eth0", "net.rx_bytes_per_s")
	if o.Value == nil || *o.Value != 1000 {
		t.Fatalf("after re-baseline rx = %+v, want 1000", o)
	}
}

func TestNetworkOneCounterGoingBackwardsSuppressesTheInterface(t *testing.T) {
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{stats: []net.IOCountersStat{counters("eth0", 10)}}
	p := newTestNetwork(c, upIfaces("eth0"), clock)
	_, _ = p.Collect(context.Background(), nil)

	clock.t = clock.t.Add(10 * time.Second)
	next := counters("eth0", 20)
	next.Dropout = 1
	c.stats = []net.IOCountersStat{next}
	obs, _ := p.Collect(context.Background(), nil)
	if len(obs) != 0 {
		t.Fatalf("got %+v, want nothing for an interface with a reset counter", obs)
	}
}

func TestNetworkZeroElapsedTimeReportsNothing(t *testing.T) {
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{stats: []net.IOCountersStat{counters("eth0", 10)}}
	p := newTestNetwork(c, upIfaces("eth0"), clock)
	_, _ = p.Collect(context.Background(), nil)

	c.stats = []net.IOCountersStat{counters("eth0", 20)}
	obs, err := p.Collect(context.Background(), nil) // same instant
	if err != nil || len(obs) != 0 {
		t.Fatalf("zero elapsed = %+v, %v; want nothing", obs, err)
	}
}

func TestNetworkBackwardsClockReportsNothing(t *testing.T) {
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{stats: []net.IOCountersStat{counters("eth0", 10)}}
	p := newTestNetwork(c, upIfaces("eth0"), clock)
	_, _ = p.Collect(context.Background(), nil)

	clock.t = clock.t.Add(-5 * time.Second)
	c.stats = []net.IOCountersStat{counters("eth0", 20)}
	obs, _ := p.Collect(context.Background(), nil)
	if len(obs) != 0 {
		t.Fatalf("negative elapsed = %+v, want nothing", obs)
	}
}

func TestNetworkDefaultSetSkipsLoopbackDownAndPrefixes(t *testing.T) {
	names := []string{"eth0", "lo", "eth1", "veth12", "docker0", "br-abc", "virbr0", "cni0", "flannel.1", "cali1", "vxlan.calico", "kube-ipvs0", "tun0", "tap0", "ens3"}
	ifs := fixtureNetInterfaces{list: net.InterfaceStatList{
		iface("eth0", "up"),
		iface("lo", "up", "loopback"),
		iface("eth1"), // down
		iface("veth12", "up"), iface("docker0", "up"), iface("br-abc", "up"),
		iface("virbr0", "up"), iface("cni0", "up"), iface("flannel.1", "up"),
		iface("cali1", "up"), iface("vxlan.calico", "up"), iface("kube-ipvs0", "up"),
		iface("tun0", "up"), iface("tap0", "up"), iface("ens3", "up"),
	}}
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{}
	for _, n := range names {
		c.stats = append(c.stats, counters(n, 1))
	}
	p := newTestNetwork(c, ifs, clock)
	_, _ = p.Collect(context.Background(), nil)
	clock.t = clock.t.Add(time.Second)
	for i := range c.stats {
		c.stats[i] = counters(c.stats[i].Name, 2)
	}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := metricItems(obs, "net.rx_bytes_per_s")
	if len(got) != 2 || got[0] != "ens3" || got[1] != "eth0" {
		t.Fatalf("interfaces observed = %v, want [ens3 eth0]", got)
	}
}

func TestNetworkExplicitListBypassesFilters(t *testing.T) {
	ifs := fixtureNetInterfaces{list: net.InterfaceStatList{iface("lo", "up", "loopback"), iface("docker0")}}
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{stats: []net.IOCountersStat{counters("lo", 1), counters("docker0", 1), counters("eth0", 1)}}
	p := newTestNetwork(c, ifs, clock)
	params := map[string]any{"interfaces": []any{"lo", "docker0"}}
	_, _ = p.Collect(context.Background(), params)
	clock.t = clock.t.Add(time.Second)
	c.stats = []net.IOCountersStat{counters("lo", 2), counters("docker0", 2), counters("eth0", 2)}
	obs, err := p.Collect(context.Background(), params)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := metricItems(obs, "net.rx_bytes_per_s")
	if len(got) != 2 || got[0] != "docker0" || got[1] != "lo" {
		t.Fatalf("interfaces observed = %v, want [docker0 lo]", got)
	}
}

func TestNetworkExplicitMissingInterfaceReportsErrPerMetric(t *testing.T) {
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{stats: []net.IOCountersStat{counters("eth0", 1)}}
	p := newTestNetwork(c, upIfaces(), clock)
	obs, err := p.Collect(context.Background(), map[string]any{"interfaces": []string{"nope"}})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want it to name the interface", err)
	}
	if len(obs) != 6 {
		t.Fatalf("got %d observations, want 6: %+v", len(obs), obs)
	}
	for _, m := range netMetrics {
		o := findObs(t, obs, "nope", m)
		if o.Err == "" || o.Value != nil {
			t.Errorf("%s = %+v, want an error observation", m, o)
		}
	}
}

func TestNetworkUnreportableInterfaceNameIsSkipped(t *testing.T) {
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{stats: []net.IOCountersStat{counters("eth0", 1)}}
	p := newTestNetwork(c, upIfaces(), clock)
	obs, err := p.Collect(context.Background(), map[string]any{"interfaces": []string{"bad\nname"}})
	if err == nil {
		t.Fatal("err = nil, want an advisory error")
	}
	if len(obs) != 0 {
		t.Fatalf("got %+v, want nothing for an unreportable name", obs)
	}
}

func TestNetworkVanishedInterfaceIsForgotten(t *testing.T) {
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{stats: []net.IOCountersStat{counters("eth0", 10)}}
	p := newTestNetwork(c, upIfaces("eth0"), clock)
	_, _ = p.Collect(context.Background(), nil)

	clock.t = clock.t.Add(10 * time.Second)
	c.stats = nil // gone
	_, _ = p.Collect(context.Background(), nil)

	clock.t = clock.t.Add(10 * time.Second)
	c.stats = []net.IOCountersStat{counters("eth0", 30)} // back, higher
	obs, _ := p.Collect(context.Background(), nil)
	if len(obs) != 0 {
		t.Fatalf("a returning interface reported %+v, want a fresh baseline", obs)
	}
}

func TestNetworkProviderErrorsAreAdvisory(t *testing.T) {
	clock := &fakeNow{t: time.Unix(1000, 0)}
	c := &fixtureNetCounters{stats: []net.IOCountersStat{counters("eth0", 10)}}
	p := newTestNetwork(c, upIfaces("eth0"), clock)
	_, _ = p.Collect(context.Background(), nil)

	// A counters fault keeps the baseline: the next good tick still reports.
	clock.t = clock.t.Add(10 * time.Second)
	c.err = errors.New("no /proc/net/dev")
	obs, err := p.Collect(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "no /proc/net/dev") || len(obs) != 0 {
		t.Fatalf("counters fault = %+v, %v", obs, err)
	}
	c.err = nil
	clock.t = clock.t.Add(10 * time.Second)
	c.stats = []net.IOCountersStat{counters("eth0", 40)}
	obs, _ = p.Collect(context.Background(), nil)
	o := findObs(t, obs, "eth0", "net.rx_bytes_per_s")
	if o.Value == nil || *o.Value != 1500 {
		t.Fatalf("rx after the fault = %+v, want 1500 over the 20 s since the baseline", o)
	}

	// An interface-list fault in discovery mode: advisory error, nothing else to report.
	p2 := newTestNetwork(c, fixtureNetInterfaces{err: errors.New("netlink")}, clock)
	obs, err = p2.Collect(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "netlink") || len(obs) != 0 {
		t.Fatalf("interfaces fault = %+v, %v", obs, err)
	}
}

func TestNetworkDeadContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := newTestNetwork(&fixtureNetCounters{}, upIfaces(), &fakeNow{t: time.Unix(1, 0)})
	obs, err := p.Collect(ctx, nil)
	if err == nil || obs != nil {
		t.Fatalf("got %+v, %v; want nothing and the context error", obs, err)
	}
}
