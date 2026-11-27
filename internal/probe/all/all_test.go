package all_test

import (
	"context"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/all"
)

// awaitingProbe are server plugins whose descriptor landed before their
// probe (snmp's did in backlog 3.13d, task 3, its probe in task 4). Until
// then Run answers "unknown probe plugin" for them, which is what the
// runner reports on every metric. TestAwaitingProbeIsCurrent fails as soon
// as such a probe is registered, so an entry cannot outlive the gap.
var awaitingProbe = map[string]bool{}

// TestAwaitingProbeIsCurrent: every awaitingProbe entry is a server plugin
// that has no probe yet.
func TestAwaitingProbeIsCurrent(t *testing.T) {
	reg := all.Registry(nil)
	for name := range awaitingProbe {
		d, ok := checkplugins.Get(name)
		if !ok || d.Runs != checkplugins.Server {
			t.Errorf("awaitingProbe names %s, which is not a server plugin", name)
		}
		if _, ok := reg[name]; ok {
			t.Errorf("%s has its probe now; remove it from awaitingProbe", name)
		}
	}
}

// TestRunKnowsExactlyTheServerPlugins pins the registry to the descriptors:
// every plugin with Runs == Server must be handled, and no agent plugin may be.
// No packet leaves the host: the context is already cancelled, the params
// are empty and the address is TEST-NET-1. tcp (no port), http (no url),
// snmp (no secret) and dns (its default name is the address, an IP literal
// it refuses to look up) fail on their empty params before any dial; tls dials with the
// cancelled context, which the dialer checks before connecting; icmp opens
// a local socket and closes it again without sending, because its send
// loop checks the context first.
func TestRunKnowsExactlyTheServerPlugins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := probe.NewGuard(nil, nil)
	reg := all.Registry(nil)
	servers := 0
	for _, d := range checkplugins.All() {
		if d.Runs == checkplugins.Server {
			servers++
		}
		t.Run(d.Name, func(t *testing.T) {
			obs := reg.Run(ctx, g, d.Name, map[string]any{}, "192.0.2.1", nil)
			unknown := false
			for _, o := range obs {
				if strings.Contains(o.Err, "unknown probe plugin") {
					unknown = true
				}
			}
			if want := d.Runs != checkplugins.Server || awaitingProbe[d.Name]; unknown != want {
				t.Fatalf("plugin %s (runs %s): unknown probe plugin = %v, want %v; observations %+v", d.Name, d.Runs, unknown, want, obs)
			}
		})
	}
	// Without a server plugin the loop above would only check that Run
	// refuses agent plugins, and pass whatever Run's switch says.
	if servers == 0 {
		t.Fatal("no descriptor runs on the server")
	}
}

func TestRunUnknownPluginIsAnError(t *testing.T) {
	obs := all.Registry(nil).Run(context.Background(), probe.NewGuardForTest(true, nil), "nosuch", nil, "127.0.0.1", nil)
	if len(obs) != 1 || obs[0].Metric != "nosuch.up" || !strings.Contains(obs[0].Err, "unknown probe plugin") {
		t.Fatalf("want one error for nosuch.up, got %+v", obs)
	}
}

func TestRegistryDispatchesEachProbeByName(t *testing.T) {
	var called string
	fake := func(name string) probe.Func {
		return func(context.Context, *probe.Guard, map[string]any, string, map[string]string) []probe.Observation {
			called = name
			return nil
		}
	}
	reg := probe.Registry{"a": fake("a"), "b": fake("b")}
	for _, name := range []string{"a", "b"} {
		reg.Run(context.Background(), nil, name, nil, "", nil)
		if called != name {
			t.Fatalf("Run(%q) called %q", name, called)
		}
	}
}

func TestRegistryNamesAreTheServerPlugins(t *testing.T) {
	reg := all.Registry(nil)
	for _, d := range checkplugins.All() {
		if _, ok := reg[d.Name]; ok != (d.Runs == checkplugins.Server && !awaitingProbe[d.Name]) {
			t.Fatalf("plugin %s (runs %s): in registry = %v", d.Name, d.Runs, ok)
		}
	}
}

// TestFailAllCoversPluginsWithParamDependentMetrics: a server plugin whose
// metrics depend on its params (items from a list, a secret that can be
// missing) has its own FailAll, so an unattempted probe reports exactly
// what the params would have produced; any other plugin falls back to its
// descriptor's metrics.
func TestFailAllCoversPluginsWithParamDependentMetrics(t *testing.T) {
	for _, d := range checkplugins.All() {
		if d.Runs != checkplugins.Server {
			continue
		}
		_, ok := all.FailAll(d.Name, map[string]any{}, "boom")
		if want := d.ItemParam != "" || d.UsesSecret(); ok != want {
			t.Errorf("plugin %s: has FailAll = %v, want %v", d.Name, ok, want)
		}
	}
}

// TestFailAllReportsTheDescriptorsMetrics: every observation FailAll
// returns is one of the plugin's metrics, with the error.
func TestFailAllReportsTheDescriptorsMetrics(t *testing.T) {
	for plugin, params := range map[string]map[string]any{
		"http": {"url": "http://x/", "body_contains": "ok", "json_values": []any{"a=$.a"}},
		"snmp": {"secret": "sw", "oids": []any{"name=1.3.6.1.2.1.1.5.0"}, "assertions": []any{"up=uptime > 1"}},
		"tcp":  {"port": float64(22), "expect": "SSH", "assertions": []any{"a=connect_ms < 5"}},
		"dns":  {"expect": "x", "assertions": []any{"a=answer_count > 0"}},
		"tls":  {"assertions": []any{"a=cert_days > 5"}},
	} {
		d, _ := checkplugins.Get(plugin)
		metrics := map[string]bool{}
		for _, m := range d.Metrics {
			metrics[m.Name] = true
		}
		obs, ok := all.FailAll(plugin, params, "boom")
		if !ok || len(obs) == 0 {
			t.Fatalf("%s: FailAll = %+v, %v", plugin, obs, ok)
		}
		items := map[string]bool{}
		for _, o := range obs {
			if !metrics[o.Metric] || o.Err != "boom" || o.Value != nil || o.Label != nil {
				t.Fatalf("%s: observation %+v, want an error on a descriptor metric", plugin, o)
			}
			if o.Item != "" {
				items[o.Item] = true
			}
		}
		// One item per list entry: snmp's oids entry and its assertion.
		want := 1
		if plugin == "snmp" {
			want = 2
		}
		if len(items) != want {
			t.Fatalf("%s: items %v, want the %d list entries", plugin, items, want)
		}
	}
	if obs, ok := all.FailAll("icmp", map[string]any{}, "boom"); ok || obs != nil {
		t.Fatalf("icmp: FailAll = %+v, %v; want none of its own", obs, ok)
	}
}
