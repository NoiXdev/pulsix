package checkplugins_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

var vantageParamNames = []string{"vantage_manager", "vantage_groups", "agents_per_group", "fail_policy"}

func TestSyntheticDescriptorsCarryVantageParams(t *testing.T) {
	for _, d := range checkplugins.All() {
		var names []string
		for _, p := range d.Params {
			if slices.Contains(vantageParamNames, p.Name) {
				names = append(names, p.Name)
			}
		}
		if d.Runs == checkplugins.Agent {
			if len(names) != 0 {
				t.Errorf("%s: agent plugin carries vantage params %v", d.Name, names)
			}
			continue
		}
		if !slices.Equal(names, vantageParamNames) {
			t.Errorf("%s: vantage params = %v, want %v", d.Name, names, vantageParamNames)
		}
		// Appended after the plugin's own parameters.
		tail := d.Params[len(d.Params)-4:]
		for i, p := range tail {
			if p.Name != vantageParamNames[i] {
				t.Errorf("%s: vantage params are not the last four", d.Name)
			}
		}
	}
}

func TestVantageParamSpecs(t *testing.T) {
	d, _ := checkplugins.Get("icmp")
	by := map[string]checkplugins.ParamSpec{}
	for _, p := range d.Params {
		by[p.Name] = p
	}
	if p := by["vantage_manager"]; p.Type != checkplugins.TypeBool || p.Default != true {
		t.Errorf("vantage_manager = %+v", p)
	}
	if p := by["vantage_groups"]; p.Type != checkplugins.TypeList || p.MaxEntries != 8 || p.Format != checkplugins.FormatUUID {
		t.Errorf("vantage_groups = %+v", p)
	}
	if p := by["agents_per_group"]; p.Type != checkplugins.TypeInteger || *p.Min != 1 || *p.Max != 10 || p.Default != int64(3) {
		t.Errorf("agents_per_group = %+v", p)
	}
	if p := by["fail_policy"]; p.Type != checkplugins.TypeEnum || !slices.Equal(p.Options, []string{"any", "majority", "all"}) || p.Default != "any" {
		t.Errorf("fail_policy = %+v", p)
	}
}

const (
	g1     = "11111111-1111-4111-8111-111111111111"
	g2     = "22222222-2222-4222-8222-222222222222"
	upperG = "ABCDEF01-2222-4222-8222-222222222222"
)

func TestVantageHook(t *testing.T) {
	runParamCases(t, []paramCase{
		{"defaults", "icmp", map[string]any{}, true},
		{"groups only", "icmp", map[string]any{"vantage_groups": []any{g1}}, true},
		{"no manager with group", "icmp", map[string]any{"vantage_manager": false, "vantage_groups": []any{g1}}, true},
		{"no vantage at all", "icmp", map[string]any{"vantage_manager": false}, false},
		{"no vantage, empty groups", "tcp", map[string]any{"port": 22, "vantage_manager": false, "vantage_groups": []any{}}, false},
		{"duplicate groups", "icmp", map[string]any{"vantage_groups": []any{g1, g2, g1}}, false},
		{"nine groups", "icmp", map[string]any{"vantage_groups": []any{
			"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000003",
			"00000000-0000-4000-8000-000000000004", "00000000-0000-4000-8000-000000000005", "00000000-0000-4000-8000-000000000006",
			"00000000-0000-4000-8000-000000000007", "00000000-0000-4000-8000-000000000008", "00000000-0000-4000-8000-000000000009"}}, false},
		{"upper-case group", "icmp", map[string]any{"vantage_groups": []any{upperG}}, false},
		{"not a uuid", "icmp", map[string]any{"vantage_groups": []any{"nope"}}, false},
		{"secret with groups", "http", map[string]any{"url": "https://x/", "secret": "tok", "vantage_groups": []any{g1}}, false},
		{"secret without groups", "http", map[string]any{"url": "https://x/", "secret": "tok"}, true},
		{"snmp with groups", "snmp", map[string]any{"secret": "sw", "interfaces": true, "vantage_groups": []any{g1}}, false},
		{"snmp manager only", "snmp", map[string]any{"secret": "sw", "interfaces": true}, true},
		{"snmp own hook still runs", "snmp", map[string]any{"secret": "sw"}, false},
		{"http own hook still runs", "http", map[string]any{"url": "https://x/", "json_values": []any{"a=$.x", "a=$.y"}, "vantage_groups": []any{g1}}, false},
		{"per group without groups", "icmp", map[string]any{"agents_per_group": 5, "fail_policy": "all"}, true},
		{"per group 0", "icmp", map[string]any{"agents_per_group": 0}, false},
		{"per group 11", "icmp", map[string]any{"agents_per_group": 11}, false},
		{"bad policy", "icmp", map[string]any{"fail_policy": "some"}, false},
		{"agent plugin refuses", "cpu", map[string]any{"vantage_manager": true}, false},
	})
}

func TestFormatUUIDVectors(t *testing.T) {
	for in, want := range map[string]bool{
		g1: true, "00000000-0000-0000-0000-000000000000": true,
		upperG: false, "": false, "{" + g1 + "}": false, "urn:uuid:" + g1: false,
		strings.ReplaceAll(g1, "-", ""): false, g1 + " ": false,
	} {
		if got := checkplugins.FormatUUID.Valid(in); got != want {
			t.Errorf("FormatUUID.Valid(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestVantageOfDefaults(t *testing.T) {
	want := checkplugins.Vantage{Manager: true, PerGroup: 3, Policy: "any"}
	for _, in := range []map[string]any{nil, {}, {"url": "x"}} {
		if got := checkplugins.VantageOf(in); !reflect.DeepEqual(got, want) {
			t.Errorf("VantageOf(%v) = %+v, want %+v", in, got, want)
		}
	}
}

func TestVantageOfReadsValues(t *testing.T) {
	a, b := uuid.MustParse(g1), uuid.MustParse(g2)
	for _, groups := range []any{[]any{g1, g2}, []string{g1, g2}} {
		got := checkplugins.VantageOf(map[string]any{
			"vantage_manager": false, "vantage_groups": groups,
			"agents_per_group": float64(5), "fail_policy": "majority",
		})
		want := checkplugins.Vantage{Manager: false, Groups: []uuid.UUID{a, b}, PerGroup: 5, Policy: "majority"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("VantageOf = %+v, want %+v", got, want)
		}
	}
	if got := checkplugins.VantageOf(map[string]any{"agents_per_group": 7}); got.PerGroup != 7 {
		t.Errorf("int agents_per_group = %d", got.PerGroup)
	}
}

func TestStripVantage(t *testing.T) {
	in := map[string]any{
		"url": "https://x/", "vantage_manager": false, "vantage_groups": []any{g1},
		"agents_per_group": 2, "fail_policy": "all",
	}
	got := checkplugins.StripVantage(in)
	if want := (map[string]any{"url": "https://x/"}); !reflect.DeepEqual(got, want) {
		t.Errorf("StripVantage = %v, want %v", got, want)
	}
	if len(in) != 5 {
		t.Errorf("StripVantage modified its input: %v", in)
	}
	if got := checkplugins.StripVantage(nil); got == nil || len(got) != 0 {
		t.Errorf("StripVantage(nil) = %v, want empty non-nil map", got)
	}
}
