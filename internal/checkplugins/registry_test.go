package checkplugins_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestNamesInTheSPAsOrder: the order CheckDialog.tsx and
// SharedCheckFormPage.tsx offered the plugins in before the descriptors.
func TestNamesInTheSPAsOrder(t *testing.T) {
	want := []string{"cpu", "memory", "disk", "service", "network", "uptime", "docker",
		"temperature", "smart", "raid", "zfs", "postgres", "mysql", "redis", "nginx", "apache", "caddy", "phpfpm", "script", "icmp", "tcp", "http", "tls", "dns", "snmp"}
	if got := checkplugins.Names(); !slices.Equal(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestRuns(t *testing.T) {
	server := []string{"icmp", "tcp", "http", "tls", "dns", "snmp"}
	for _, d := range checkplugins.All() {
		want := checkplugins.Agent
		if slices.Contains(server, d.Name) {
			want = checkplugins.Server
		}
		if d.Runs != want {
			t.Errorf("%s runs on %q, want %q", d.Name, d.Runs, want)
		}
	}
}

func TestGet(t *testing.T) {
	d, ok := checkplugins.Get("smart")
	if !ok || d.Name != "smart" || d.ItemParam != "devices" || !d.HostItemAlwaysAsked || d.ItemEntry == nil {
		t.Fatalf("Get(smart) = %+v, %v", d, ok)
	}
	if _, ok := checkplugins.Get("no-such-plugin"); ok {
		t.Fatal("Get(no-such-plugin) found a plugin")
	}
}

// TestGetEveryName: the registry spells each plugin's name next to its
// constructor, so every listed name must lead to the descriptor carrying it
// (an entry wired to the wrong constructor, or a constructor whose Name
// differs from its entry, would otherwise go unnoticed), and no name may
// be listed twice (Get would never reach the second entry).
func TestGetEveryName(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range checkplugins.Names() {
		if seen[name] {
			t.Errorf("%q is registered twice", name)
		}
		seen[name] = true
		d, ok := checkplugins.Get(name)
		if !ok || d.Name != name {
			t.Errorf("Get(%q) = %q, %v", name, d.Name, ok)
		}
	}
}

// TestAllIsFreshEveryCall: a caller changing what All returns changes
// nothing for the next caller.
func TestAllIsFreshEveryCall(t *testing.T) {
	first := checkplugins.All()
	first[0].Name = "changed"
	first[2].Params[0].Name = "changed"
	second := checkplugins.All()
	if second[0].Name != "cpu" || second[2].Params[0].Name != "mounts" {
		t.Fatalf("All() returned shared values: %s, %s", second[0].Name, second[2].Params[0].Name)
	}
}

// TestDescriptorsAreWellFormed: what the generic validator and the SPA's
// renderer rely on, for every descriptor.
func TestDescriptorsAreWellFormed(t *testing.T) {
	metrics := map[string]string{}
	for _, d := range checkplugins.All() {
		if d.Params == nil {
			t.Errorf("%s: Params is nil, want empty", d.Name)
		}
		if len(d.Metrics) == 0 {
			t.Errorf("%s: no metrics", d.Name)
		}
		for _, m := range d.Metrics {
			if other, dup := metrics[m.Name]; dup {
				t.Errorf("metric %s belongs to %s and %s", m.Name, other, d.Name)
			}
			metrics[m.Name] = d.Name
			if m.Kind != checkplugins.Numeric && m.Kind != checkplugins.Discrete {
				t.Errorf("%s: metric %s has kind %q", d.Name, m.Name, m.Kind)
			}
		}
		names := map[string]bool{}
		for _, p := range d.Params {
			if names[p.Name] {
				t.Errorf("%s: parameter %s twice", d.Name, p.Name)
			}
			names[p.Name] = true
			checkSpec(t, d.Name, p)
		}
		if d.ItemParam != "" {
			i := slices.IndexFunc(d.Params, func(p checkplugins.ParamSpec) bool { return p.Name == d.ItemParam })
			if i < 0 || d.Params[i].Type != checkplugins.TypeList {
				t.Errorf("%s: item param %q is not one of its list parameters", d.Name, d.ItemParam)
			}
		} else if d.HostItemAlwaysAsked || d.ItemEntry != nil || d.ItemAsked != nil {
			t.Errorf("%s: item rules without an item param", d.Name)
		}
		if d.ItemEntry != nil && d.ItemAsked != nil {
			t.Errorf("%s: ItemAsked replaces the list comparison ItemEntry feeds; set one of them", d.Name)
		}
	}
}

// listParamsNotItems are the "<plugin>.<param>" list parameters that do
// not enumerate their plugin's items. Empty today: every list parameter
// names the items its plugin reports on. A plugin whose list parameter
// really is something else (a list of headers, say) adds it here with a
// comment saying why; otherwise the list must be the plugin's ItemParam.
var listParamsNotItems = map[string]bool{
	// The vantage parameters of every synthetic plugin (backlog 3.13a):
	// monitoring group ids, not items.
	"icmp.vantage_groups": true, "tcp.vantage_groups": true, "http.vantage_groups": true,
	"tls.vantage_groups": true, "dns.vantage_groups": true, "snmp.vantage_groups": true,
	// http's assertions name items too, but json_values is the ItemParam;
	// the ItemAsked hook reads both lists (backlog 3.13b).
	"http.assertions": true,
	// http's request headers (backlog 3.84): sent, not reported on.
	"http.headers": true,
	// script's values for placeholders (backlog 3.6j): passed, not
	// reported on.
	"script.args": true,
	// zfs's optional pools name items too, but pools is the ItemParam; the
	// ItemAsked hook reads both lists (backlog 3.74).
	"zfs.optional_pools": true,
}

// TestListParamsAreItemParams: a list parameter left out of ItemParam makes
// AsksForItem answer true for every item, so an item the operator removed
// from the list still reads as asked for rather than "you stopped asking"
// (backlog 3.28) — the itemParam entries backlog 3.9 missed, which no test
// caught. Every list parameter must be its descriptor's ItemParam unless
// listParamsNotItems names it.
func TestListParamsAreItemParams(t *testing.T) {
	for _, d := range checkplugins.All() {
		for _, p := range d.Params {
			if p.Type != checkplugins.TypeList || listParamsNotItems[d.Name+"."+p.Name] {
				continue
			}
			if d.ItemParam != p.Name {
				t.Errorf("%s: list parameter %q is not its ItemParam (%q); set ItemParam or add %s.%s to listParamsNotItems",
					d.Name, p.Name, d.ItemParam, d.Name, p.Name)
			}
		}
	}
	for key := range listParamsNotItems {
		plugin, param, _ := strings.Cut(key, ".")
		d, ok := checkplugins.Get(plugin)
		if !ok || !slices.ContainsFunc(d.Params, func(p checkplugins.ParamSpec) bool {
			return p.Name == param && p.Type == checkplugins.TypeList
		}) {
			t.Errorf("listParamsNotItems names %s, which is not a list parameter", key)
		}
	}
}

func checkSpec(t *testing.T, plugin string, p checkplugins.ParamSpec) {
	t.Helper()
	switch p.Type {
	case checkplugins.TypeInteger:
		if p.Min == nil || p.Max == nil || *p.Min > *p.Max {
			t.Errorf("%s.%s: an integer needs Min <= Max", plugin, p.Name)
		}
	case checkplugins.TypeEnum:
		if len(p.Options) == 0 {
			t.Errorf("%s.%s: an enum needs options", plugin, p.Name)
		}
	case checkplugins.TypeString, checkplugins.TypeList, checkplugins.TypeBool:
	default:
		t.Errorf("%s.%s: unknown type %q", plugin, p.Name, p.Type)
	}
	if p.Type != checkplugins.TypeString && p.Type != checkplugins.TypeList && (p.Format != "" || p.MaxBytes != 0 || p.NonEmpty) {
		t.Errorf("%s.%s: a %s takes no format, byte bound or non-empty rule", plugin, p.Name, p.Type)
	}
	if p.Type != checkplugins.TypeList && p.MaxEntries != 0 {
		t.Errorf("%s.%s: only a list has max entries", plugin, p.Name)
	}
	// A default or an example the plugin itself would refuse is a lie the
	// SPA would show as a placeholder.
	for _, v := range []any{p.Default, p.Example} {
		if v == nil || v == "" {
			continue
		}
		// A list's example is one entry (phpfpm's default pool).
		if s, ok := v.(string); ok && p.Type == checkplugins.TypeList {
			v = []any{s}
		}
		if err := validateOne(plugin, p, v); err != nil {
			t.Errorf("%s.%s: default or example %v is refused: %v", plugin, p.Name, v, err)
		}
	}
	if p.Required && p.Default != nil {
		t.Errorf("%s.%s: a required parameter has no default", plugin, p.Name)
	}
}

// validateOne validates v as p's value inside an otherwise minimal, valid
// parameter set of plugin.
func validateOne(plugin string, p checkplugins.ParamSpec, v any) error {
	params := map[string]any{p.Name: v}
	switch plugin {
	case "tcp":
		if p.Name != "port" {
			params["port"] = 22.0
		}
	case "http":
		if p.Name != "url" {
			params["url"] = "http://x"
		}
		// A content type goes with a body, a body with POST (backlog 3.84).
		if p.Name == "content_type" {
			params["method"], params["body"] = "POST", "x"
		}
	case "script":
		if p.Name != "commands" {
			params["commands"] = []any{"x"}
		}
		// args name a listed command (backlog 3.6j).
		if p.Name == "args" {
			params["commands"] = []any{"x", "tcp_port"}
		}
	case "mysql":
		if p.Name != "secret" {
			params["secret"] = "db"
		}
	case "snmp":
		if p.Name != "secret" {
			params["secret"] = "sw"
		}
		if p.Name != "oids" {
			params["oids"] = []any{"x=1.3"}
		}
	}
	return checkplugins.Validate(plugin, params)
}

// TestDescriptorsMarshalWithoutHooks: the JSON view the API serves.
func TestDescriptorsMarshalWithoutHooks(t *testing.T) {
	raw, err := json.Marshal(checkplugins.All())
	if err != nil {
		t.Fatalf("Marshal(All()) = %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(checkplugins.Names()) {
		t.Fatalf("%d descriptors marshalled, want %d", len(got), len(checkplugins.Names()))
	}
	cpu := got[0]
	if cpu["name"] != "cpu" || cpu["runs"] != "agent" {
		t.Fatalf("cpu = %v", cpu)
	}
	if params, ok := cpu["params"].([]any); !ok || len(params) != 0 {
		t.Fatalf("cpu params = %#v, want []", cpu["params"])
	}
	for _, d := range got {
		for _, hook := range []string{"ItemEntry", "ItemAsked", "MetricAsked", "Check", "item_entry", "item_asked", "metric_asked", "check"} {
			if _, ok := d[hook]; ok {
				t.Fatalf("%s marshals its %s hook", d["name"], hook)
			}
		}
	}
	text := string(raw)
	for _, want := range []string{
		`{"name":"devices","type":"list","max_entries":64,"max_bytes":255,"format":"devicePath"}`,
		`"item_param":"devices","host_item_always_asked":true`,
		`{"name":"timeout_s","type":"integer","default":10,"min":1,"max":60}`,
		`{"name":"method","type":"enum","default":"GET","options":["GET","HEAD","POST","PUT","PATCH","DELETE","OPTIONS"]}`,
		`{"name":"url","type":"string","required":true,"example":"https://{address}/","max_bytes":500,"format":"url"}`,
		`{"name":"units","type":"list","required":true,"non_empty":true,"format":"notBlank"}`,
		`{"name":"raid.state","kind":"discrete"}`,
		`{"name":"zfs.state","kind":"discrete"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("JSON lacks %s", want)
		}
	}
}

// TestMetricOwners: every descriptor metric maps to the plugin that reports it.
func TestMetricOwners(t *testing.T) {
	owners := checkplugins.MetricOwners()
	n := 0
	for _, d := range checkplugins.All() {
		for _, m := range d.Metrics {
			n++
			if owners[m.Name] != d.Name {
				t.Errorf("owner of %q = %q, want %q", m.Name, owners[m.Name], d.Name)
			}
		}
	}
	if n == 0 || len(owners) != n {
		t.Fatalf("%d owners for %d metrics", len(owners), n)
	}
	if _, ok := owners["no.such_metric"]; ok {
		t.Fatal("an unknown metric has an owner")
	}
}
