package probes

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probeconfig"
)

// runOnce applies sp to r, makes it due and returns the one round's results.
func runOnce(t *testing.T, r *Runner, clock *fakeClock, sp probeconfig.Spec) []Result {
	t.Helper()
	r.Apply([]probeconfig.Spec{sp})
	clock.advance(time.Hour)
	s := newSink()
	r.dispatch(t.Context(), s.add)
	r.wg.Wait()
	all := s.all()
	if len(all) != 1 {
		t.Fatalf("batches = %d, want 1", len(all))
	}
	return all[0]
}

func metricsOf(results []Result) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Metric
	}
	return out
}

func descriptorMetrics(t *testing.T, plugin string) []string {
	t.Helper()
	d, ok := checkplugins.Get(plugin)
	if !ok {
		t.Fatalf("no descriptor for %s", plugin)
	}
	out := make([]string, len(d.Metrics))
	for i, m := range d.Metrics {
		out[i] = m.Name
	}
	return out
}

func allErrors(t *testing.T, results []Result, want string) {
	t.Helper()
	for _, r := range results {
		if r.Err == "" || r.Value != nil || r.Label != nil {
			t.Errorf("result %+v is not an error", r)
		}
		if !strings.Contains(r.Err, want) {
			t.Errorf("error %q does not say %q", r.Err, want)
		}
		if r.TargetHostID != targetA || r.CheckKey != "web" {
			t.Errorf("result %+v does not name its slot", r)
		}
	}
}

func TestUnknownPluginYieldsItsMetricsAsErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, plugin, params string
		want                 []string
	}{
		// A synthetic plugin this agent's registry lacks: its descriptor's
		// metrics, item "".
		{"descriptor fallback", "icmp", `{"count":3}`, descriptorMetrics(t, "icmp")},
		// tcp, dns and tls have their own since backlog 3.87 (assertion
		// items): without assertions, their certain whole-host metrics.
		{"tls's own fail-all", "tls", `{"port":443}`,
			[]string{"tls.up", "tls.failure", "tls.handshake_ms", "tls.cert_valid", "tls.cert_expiry_days", "tls.cert_issuer"}},
		// A plugin with its own FailAll: what its params would produce.
		{"plugin's own fail-all", "http", `{"url":"http://{address}/","body_contains":"ok"}`,
			[]string{
				"http.up", "http.failure", "http.status_code", "http.response_ms",
				"http.connect_ms", "http.ttfb_ms", "http.transfer_ms", "http.redirects", "http.content_bytes",
				"http.body_match",
			}},
		// Nothing anywhere knows it.
		{"no descriptor", "gopher", `{}`, []string{"gopher.up"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, clock, _ := newTestRunner(probe.Registry{})
			sp := spec(targetA, "web", tc.plugin, 60)
			sp.ParamsJSON = tc.params
			got := runOnce(t, r, clock, sp)
			allErrors(t, got, "unknown probe plugin "+tc.plugin)
			if !slices.Equal(metricsOf(got), tc.want) {
				t.Fatalf("metrics = %v, want %v", metricsOf(got), tc.want)
			}
			for _, res := range got {
				if res.Item != "" {
					t.Errorf("item = %q, want empty", res.Item)
				}
			}
		})
	}
}

func TestUnreadableParamsYieldErrorsWithoutProbing(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(false, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	sp := spec(targetA, "web", "tcp", 60)
	sp.ParamsJSON = `{"port":`
	got := runOnce(t, r, clock, sp)
	allErrors(t, got, "probe params")
	// tcp's own FailAll (backlog 3.87) without params: its certain
	// whole-host metrics.
	if want := []string{"tcp.up", "tcp.failure", "tcp.connect_ms"}; !slices.Equal(metricsOf(got), want) {
		t.Fatalf("metrics = %v, want %v", metricsOf(got), want)
	}
	if n := b.callCount(); n != 0 {
		t.Fatalf("probed %d times with unreadable params", n)
	}
	// Params that are JSON but no object are unreadable too.
	sp.ParamsJSON = `[1,2]`
	got = runOnce(t, r, clock, sp)
	allErrors(t, got, "probe params")
}

func TestParamsReachTheProbeDecoded(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(false, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	sp := spec(targetA, "web", "tcp", 60)
	sp.ParamsJSON = `{"port":443,"expect":"SSH"}`
	runOnce(t, r, clock, sp)
	c := b.calls[0]
	if c.params["port"] != float64(443) || c.params["expect"] != "SSH" || c.address != "192.0.2.10" {
		t.Fatalf("probe got params %v address %q", c.params, c.address)
	}
}

func TestDefaultGuardIsTheManagersDefault(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(false, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	runOnce(t, r, clock, spec(targetA, "web", "tcp", 60))
	g := b.calls[0].guard
	if g == nil {
		t.Fatal("the probe got no guard")
	}
	for _, addr := range []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1"} {
		if g.Allowed(netip.MustParseAddr(addr)) {
			t.Errorf("the default guard allows %s", addr)
		}
	}
	if !g.Allowed(netip.MustParseAddr("192.0.2.10")) {
		t.Error("the default guard refuses an ordinary address")
	}
}

// loopbackListener counts the connections it accepts.
func loopbackListener(t *testing.T) (port int, accepted *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted = &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, accepted
}

// TestProductionRegistryRefusesLoopbackTargets runs the real tcp probe with
// the runner's defaults (registry and guard) against a loopback listener:
// the guard refuses it, so the round is errors and nothing connects.
func TestProductionRegistryRefusesLoopbackTargets(t *testing.T) {
	t.Parallel()
	port, accepted := loopbackListener(t)
	clock := newFakeClock()
	r := NewRunner(Deps{Now: clock.Now, Ticker: (&fakeTickers{}).factory})
	sp := spec(targetA, "web", "tcp", 30)
	sp.ParamsJSON = `{"port":` + strconv.Itoa(port) + `,"timeout_s":2}`
	sp.Address = "127.0.0.1"
	got := runOnce(t, r, clock, sp)
	allErrors(t, got, probe.ErrNotAllowed.Error())
	if n := accepted.Load(); n != 0 {
		t.Fatalf("the refused target accepted %d connections", n)
	}
}

// TestPermissiveGuardProbesForReal is the functional counterpart: with a
// guard that allows loopback, the same probe connects and reports up.
func TestPermissiveGuardProbesForReal(t *testing.T) {
	t.Parallel()
	port, accepted := loopbackListener(t)
	clock := newFakeClock()
	r := NewRunner(Deps{Now: clock.Now, Ticker: (&fakeTickers{}).factory, Guard: probe.NewGuardForTest(true, nil)})
	sp := spec(targetA, "web", "tcp", 30)
	sp.ParamsJSON = `{"port":` + strconv.Itoa(port) + `,"timeout_s":2}`
	sp.Address = "127.0.0.1"
	got := runOnce(t, r, clock, sp)
	var upValue *float64
	for _, res := range got {
		if res.Metric == "tcp.up" {
			upValue = res.Value
		}
	}
	if upValue == nil || *upValue != 1 {
		t.Fatalf("tcp.up not 1: %+v", got)
	}
	waitFor(t, func() bool { return accepted.Load() == 1 })
}

func TestDefaultRegistryHasTheAgentProbes(t *testing.T) {
	t.Parallel()
	reg := DefaultRegistry()
	for _, p := range []string{"icmp", "tcp", "http", "tls", "dns"} {
		if reg[p] == nil {
			t.Errorf("default registry lacks %s", p)
		}
	}
	// snmp always names a secret, so it is probed only from the manager.
	if reg["snmp"] != nil {
		t.Error("default registry carries snmp")
	}
	if len(reg) != 5 {
		t.Errorf("default registry has %d probes, want 5", len(reg))
	}
}

func TestProbeHonoursCancellation(t *testing.T) {
	t.Parallel()
	b := newBlockingProbe(true, up("tcp"))
	r, clock, _ := newTestRunner(probe.Registry{"tcp": b.fn("tcp")})
	r.Apply([]probeconfig.Spec{spec(targetA, "web", "tcp", 60)})
	clock.advance(time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	s := newSink()
	r.dispatch(ctx, s.add)
	b.waitStarted(t, 1)
	cancel()
	r.wg.Wait()
	if len(s.all()) != 0 {
		t.Fatal("a round cut short by shutdown was delivered")
	}
}

// TestSNMPSlotFailsWithTheDescriptorsMetrics: snmp's probe is not linked
// into the agent (backlog 3.78), so a slot that named it anyway — the
// manager never assigns one and drops what comes back — reports every
// snmp descriptor metric as an error on item "", dialling nothing.
func TestSNMPSlotFailsWithTheDescriptorsMetrics(t *testing.T) {
	t.Parallel()
	d, ok := checkplugins.Get("snmp")
	if !ok {
		t.Fatal("no snmp descriptor")
	}
	obs := failAll("snmp", map[string]any{"oids": []any{"a=1.3.6.1.2.1.1.3.0"}}, "unknown probe plugin snmp")
	if len(obs) != len(d.Metrics) {
		t.Fatalf("observations = %d, want one per descriptor metric (%d): %+v", len(obs), len(d.Metrics), obs)
	}
	for i, o := range obs {
		if o.Metric != d.Metrics[i].Name || o.Item != "" || o.Err == "" {
			t.Fatalf("observation %d = %+v, want %s on item \"\" as an error", i, o, d.Metrics[i].Name)
		}
	}
}
