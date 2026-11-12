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
		"temperature", "smart", "raid", "icmp", "tcp", "http", "tls", "dns"}
	if got := checkplugins.Names(); !slices.Equal(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestRuns(t *testing.T) {
	server := []string{"icmp", "tcp", "http", "tls", "dns"}
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
	if _, ok := checkplugins.Get("snmp"); ok {
		t.Fatal("Get(snmp) found a plugin")
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
		} else if d.HostItemAlwaysAsked || d.ItemEntry != nil {
			t.Errorf("%s: item rules without an item param", d.Name)
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
		for _, hook := range []string{"ItemEntry", "Check", "item_entry", "check"} {
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
		`{"name":"method","type":"enum","default":"GET","options":["GET","HEAD"]}`,
		`{"name":"url","type":"string","required":true,"example":"https://{address}/","max_bytes":500,"format":"url"}`,
		`{"name":"units","type":"list","required":true,"non_empty":true,"format":"notBlank"}`,
		`{"name":"raid.state","kind":"discrete"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("JSON lacks %s", want)
		}
	}
}
