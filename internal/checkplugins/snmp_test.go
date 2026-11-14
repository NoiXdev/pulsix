package checkplugins_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// snmpOK is the smallest valid snmp parameter set plus extra.
func snmpOK(extra map[string]any) map[string]any {
	params := map[string]any{"secret": "sw-core", "interfaces": true}
	for k, v := range extra {
		params[k] = v
	}
	return params
}

// oidList is n distinct valid oids entries.
func oidList(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = "v" + strings.Repeat("x", i) + "=1.3.6.1.2.1.1.3.0"
	}
	return out
}

// TestValidateSNMPParams: every rule of the spec's descriptor table
// (docs/superpowers/specs/2026-11-14-snmp-checks-design.md, "The snmp
// plugin") and of its Check hook.
func TestValidateSNMPParams(t *testing.T) {
	runParamCases(t, []paramCase{
		{"interfaces only", "snmp", snmpOK(nil), true},
		{"oids only", "snmp", map[string]any{"secret": "sw", "oids": []any{"load=1.3.6.1.4.1.2021.10.1.3.1"}}, true},
		{"defaults spelled out", "snmp", snmpOK(map[string]any{"version": "v2c", "port": 161.0, "timeout_seconds": 5.0, "interfaces": false, "oids": []any{"up=1.3.6.1.2.1.1.3.0"}}), true},
		{"v3", "snmp", snmpOK(map[string]any{"version": "v3"}), true},
		{"port bounds", "snmp", snmpOK(map[string]any{"port": 1.0}), true},
		{"port upper bound", "snmp", snmpOK(map[string]any{"port": 65535.0}), true},
		{"timeout bounds", "snmp", snmpOK(map[string]any{"timeout_seconds": 1.0}), true},
		{"timeout upper bound", "snmp", snmpOK(map[string]any{"timeout_seconds": 30.0}), true},
		{"Go ints", "snmp", snmpOK(map[string]any{"port": 1161, "timeout_seconds": int64(2)}), true},
		{"32 oids", "snmp", snmpOK(map[string]any{"oids": oidList(32)}), true},
		{"Go string list", "snmp", snmpOK(map[string]any{"oids": []string{"a=1.3", "b=.1.3"}}), true},
		{"empty oids with interfaces", "snmp", snmpOK(map[string]any{"oids": []any{}}), true},

		{"no params", "snmp", nil, false},
		{"no secret", "snmp", map[string]any{"interfaces": true}, false},
		{"empty secret", "snmp", snmpOK(map[string]any{"secret": ""}), false},
		{"secret with a space", "snmp", snmpOK(map[string]any{"secret": "my secret"}), false},
		{"secret of 65 bytes", "snmp", snmpOK(map[string]any{"secret": strings.Repeat("s", 65)}), false},
		{"v1", "snmp", snmpOK(map[string]any{"version": "v1"}), false},
		{"upper-case version", "snmp", snmpOK(map[string]any{"version": "V2C"}), false},
		{"port 0", "snmp", snmpOK(map[string]any{"port": 0.0}), false},
		{"port 65536", "snmp", snmpOK(map[string]any{"port": 65536.0}), false},
		{"port as string", "snmp", snmpOK(map[string]any{"port": "161"}), false},
		{"timeout 0", "snmp", snmpOK(map[string]any{"timeout_seconds": 0.0}), false},
		{"timeout 31", "snmp", snmpOK(map[string]any{"timeout_seconds": 31.0}), false},
		{"fractional timeout", "snmp", snmpOK(map[string]any{"timeout_seconds": 1.5}), false},
		{"timeout_s is not its spelling", "snmp", snmpOK(map[string]any{"timeout_s": 5.0}), false},
		{"33 oids", "snmp", snmpOK(map[string]any{"oids": oidList(33)}), false},
		{"oids not a list", "snmp", snmpOK(map[string]any{"oids": "a=1.3"}), false},
		{"oids entry not a string", "snmp", snmpOK(map[string]any{"oids": []any{1.0}}), false},
		{"oids empty entry", "snmp", snmpOK(map[string]any{"oids": []any{""}}), false},
		{"oids entry without a name", "snmp", snmpOK(map[string]any{"oids": []any{"1.3.6.1"}}), false},
		{"oids entry with a bad name", "snmp", snmpOK(map[string]any{"oids": []any{"a b=1.3.6.1"}}), false},
		{"oids entry with a symbolic OID", "snmp", snmpOK(map[string]any{"oids": []any{"up=sysUpTime.0"}}), false},
		{"interfaces as string", "snmp", snmpOK(map[string]any{"interfaces": "true"}), false},
		{"unknown key", "snmp", snmpOK(map[string]any{"community": "public"}), false},
		// The Check hook.
		{"nothing to poll", "snmp", map[string]any{"secret": "sw"}, false},
		{"nothing to poll, interfaces false", "snmp", map[string]any{"secret": "sw", "interfaces": false}, false},
		{"nothing to poll, empty oids", "snmp", map[string]any{"secret": "sw", "oids": []any{}, "interfaces": false}, false},
		{"duplicate oid names", "snmp", snmpOK(map[string]any{"oids": []any{"a=1.3.6.1", "a=1.3.6.2"}}), false},
		{"duplicate oid names, same OID", "snmp", snmpOK(map[string]any{"oids": []any{"a=1.3.6.1", "a=1.3.6.1"}}), false},
	})
}

// TestValidateSNMPMessages pins the Check hook's refusals and the format's.
func TestValidateSNMPMessages(t *testing.T) {
	cases := []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"secret": "sw"}, "snmp: nothing to poll: list at least one entry in oids or set interfaces to true"},
		{snmpOK(map[string]any{"oids": []any{"a=1.3", "b=1.4", "a=1.5"}}), `snmp: oids names "a" twice`},
		{snmpOK(map[string]any{"oids": []any{"a b=1.3"}}), "snmp: oids entries must be name=OID, the name 1 to 64 letters, digits, '_', '.' or '-'"},
		{snmpOK(map[string]any{"oids": []any{"a=1"}}), "snmp: oids entries must be name=OID, the OID numeric and dotted such as 1.3.6.1.2.1.1.3.0: 2 to 128 whole numbers, each at most 4294967295 and without leading zeros, the first 0, 1 or 2, the second at most 39 after 0 or 1 and at most 4294967215 after 2, an optional leading dot"},
	}
	for _, tc := range cases {
		err := checkplugins.Validate("snmp", tc.params)
		if !errors.Is(err, checkplugins.ErrInvalidParams) || err.Error() != tc.want {
			t.Errorf("Validate(snmp, %v) = %v, want %q", tc.params, err, tc.want)
		}
	}
}

// TestSNMPDescriptor pins what a parameter case cannot see: where it runs,
// the item list, the defaults and the metrics with their kinds.
func TestSNMPDescriptor(t *testing.T) {
	d, ok := checkplugins.Get("snmp")
	if !ok {
		t.Fatal("Get(snmp) found nothing")
	}
	if d.Runs != checkplugins.Server || d.ItemParam != "oids" || !d.HostItemAlwaysAsked || d.ItemAsked == nil || d.ItemEntry != nil || d.Check == nil {
		t.Fatalf("snmp descriptor = %+v", d)
	}
	if !d.UsesSecret() {
		t.Fatal("snmp must take the shared secret parameter")
	}
	var names []string
	defaults := map[string]any{}
	required := map[string]bool{}
	for _, p := range d.Params {
		names = append(names, p.Name)
		defaults[p.Name] = p.Default
		required[p.Name] = p.Required
	}
	if want := []string{"secret", "version", "port", "timeout_seconds", "oids", "interfaces"}; !slices.Equal(names, want) {
		t.Fatalf("params = %v, want %v", names, want)
	}
	if !required["secret"] || len(required) != 6 || required["oids"] || required["interfaces"] {
		t.Fatalf("required = %v, want only secret", required)
	}
	if defaults["version"] != "v2c" || defaults["port"] != int64(161) || defaults["timeout_seconds"] != int64(5) ||
		defaults["interfaces"] != false || defaults["oids"] != nil || defaults["secret"] != nil {
		t.Fatalf("defaults = %v", defaults)
	}
	oids := d.Params[4]
	if oids.Type != checkplugins.TypeList || oids.MaxEntries != 32 || oids.Format != checkplugins.FormatSNMPOID || oids.NonEmpty {
		t.Fatalf("oids = %+v", oids)
	}
	want := []checkplugins.MetricSpec{
		{Name: "snmp.up", Kind: checkplugins.Numeric},
		{Name: "snmp.response_ms", Kind: checkplugins.Numeric},
		{Name: "snmp.uptime_seconds", Kind: checkplugins.Numeric},
		{Name: "snmp.value", Kind: checkplugins.Numeric},
		{Name: "snmp.text", Kind: checkplugins.Discrete},
		{Name: "snmp.if.status", Kind: checkplugins.Discrete},
		{Name: "snmp.if.in_bps", Kind: checkplugins.Numeric},
		{Name: "snmp.if.out_bps", Kind: checkplugins.Numeric},
		{Name: "snmp.if.errors", Kind: checkplugins.Numeric},
	}
	if !slices.Equal(d.Metrics, want) {
		t.Fatalf("metrics = %v, want %v", d.Metrics, want)
	}
}

// TestSNMPOIDFormat: name=OID, the name the secret-name pattern, the OID
// numeric and dotted with an optional leading dot, 2 to 128 arcs, each a
// decimal of at most 2^32-1 without leading zeros, and only what X.690
// can encode: a first arc of 0, 1 or 2, a second arc of at most 39 under
// 0 and 1, and 40*first+second at most 2^32-1 (gosnmp's bound).
func TestSNMPOIDFormat(t *testing.T) {
	arcs := func(n int) string { return strings.TrimSuffix(strings.Repeat("1.", n), ".") }
	cases := []struct {
		in    string
		valid bool
	}{
		{"uptime=1.3.6.1.2.1.1.3.0", true},
		{"uptime=.1.3.6.1.2.1.1.3.0", true},
		{"a=0.0", true},
		{"a=1.3", true},
		{"A_b.c-9=1.3.6.1.4.1.2021.10.1.3.1", true},
		{strings.Repeat("n", 64) + "=1.3", true},
		{"a=" + arcs(128), true},
		{"a=." + arcs(128), true},
		{"a=1.3.4294967295", true},
		{"a=1.0.10", true},
		{"a=0.39", true},
		{"a=1.39", true},
		{"a=2.40", true},
		{"a=2.4294967215", true},
		{"a=2.999.1", true},

		{"", false},
		{"=1.3", false},
		{"a=", false},
		{"a", false},
		{"1.3.6.1", false},
		{strings.Repeat("n", 65) + "=1.3", false},
		{"a b=1.3", false},
		{"a:b=1.3", false},
		{"a=1", false},
		{"a=.1", false},
		{"a=" + arcs(129), false},
		{"a=1.4294967296", false},
		{"a=1.4294967295", false},
		{"a=3.1", false},
		{"a=3.6.1.2", false},
		{"a=4294967295.1", false},
		{"a=0.40", false},
		{"a=1.40.1", false},
		{"a=2.4294967216", false},
		{"a=2.4294967295", false},
		{"a=1.99999999999", false},
		{"a=1.03", false},
		{"a=1.00", false},
		{"a=1..3", false},
		{"a=1.3.", false},
		{"a=..1.3", false},
		{"a=1.-3", false},
		{"a=1.+3", false},
		{"a=1. 3", false},
		{"a=1.3=1.4", false},
		{"a=iso.3", false},
		{"a=1.3\n", false},
		{"a=１.３", false},
	}
	for _, tc := range cases {
		if got := checkplugins.FormatSNMPOID.Valid(tc.in); got != tc.valid {
			t.Errorf("FormatSNMPOID.Valid(%q) = %v, want %v", tc.in, got, tc.valid)
		}
	}
}

// TestSNMPAsksForItem: the oids entries ask for their names; interfaces
// asks for any item, since interface names are the device's; the device's
// own metrics (snmp.up, …) are on the whole-host item "", always asked.
func TestSNMPAsksForItem(t *testing.T) {
	oids := []any{"load=1.3.6.1.4.1.2021.10.1.3.1", "ups.temp=.1.3.6.1.2.1.33.1.2.7.0"}
	cases := []struct {
		name   string
		params map[string]any
		item   string
		want   bool
	}{
		{"an entry's name", map[string]any{"oids": oids}, "load", true},
		{"a name with a dot", map[string]any{"oids": oids}, "ups.temp", true},
		{"a name off the list", map[string]any{"oids": oids}, "cpu", false},
		{"not the whole entry", map[string]any{"oids": oids}, "load=1.3.6.1.4.1.2021.10.1.3.1", false},
		{"not the OID", map[string]any{"oids": oids}, "1.3.6.1.4.1.2021.10.1.3.1", false},
		{"not a prefix of a name", map[string]any{"oids": oids}, "lo", false},
		{"an interface without interfaces", map[string]any{"oids": oids, "interfaces": false}, "eth0", false},
		{"any interface with interfaces", map[string]any{"oids": oids, "interfaces": true}, "Gi0/1", true},
		{"interfaces only", map[string]any{"interfaces": true}, "Gi0/1", true},
		{"the whole-host item with oids", map[string]any{"oids": oids}, "", true},
		{"the whole-host item with interfaces", map[string]any{"interfaces": true}, "", true},
		{"a Go string list", map[string]any{"oids": []string{"load=1.3"}}, "load", true},
		{"interfaces not a bool bounds nothing extra", map[string]any{"oids": oids, "interfaces": "true"}, "eth0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkplugins.AsksForItem("snmp", tc.params, tc.item); got != tc.want {
				t.Fatalf("AsksForItem(snmp, %v, %q) = %v, want %v", tc.params, tc.item, got, tc.want)
			}
		})
	}
}
