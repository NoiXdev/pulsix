package secrets

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

// TestParseProbes: the probes section (backlog 3.82) — absent or empty
// is the defaults (probing on, no deny list); enabled and deny_cidrs are
// read as written, and Probes hands out a copy.
func TestParseProbes(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want ProbePolicy
	}{
		{"absent", "secrets: {}\n", ProbePolicy{}},
		{"empty file", "", ProbePolicy{}},
		{"empty section", "probes:\n", ProbePolicy{}},
		{"empty mapping", "probes: {}\n", ProbePolicy{}},
		{"disabled", "probes:\n  enabled: false\n", ProbePolicy{Disabled: true}},
		{"enabled", "probes:\n  enabled: true\n", ProbePolicy{}},
		{"enabled capitalised", "probes:\n  enabled: False\n", ProbePolicy{Disabled: true}},
		{"empty deny list", "probes:\n  deny_cidrs: []\n", ProbePolicy{}},
		{"null deny list", "probes:\n  deny_cidrs:\n", ProbePolicy{}},
		{"deny list", "probes:\n  deny_cidrs:\n    - 10.0.0.0/8\n    - fd00::/8\n    - \"192.168.1.7/32\"\n",
			ProbePolicy{DenyCIDRs: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8"), netip.MustParsePrefix("192.168.1.7/32"),
			}}},
		{"flow deny list with the switch", "probes: {enabled: true, deny_cidrs: [10.0.0.0/8]}\n",
			ProbePolicy{DenyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}},
		{"beside the other sections", "secrets: {}\ncommands: {}\nchecks: {}\nprobes:\n  enabled: false\n", ProbePolicy{Disabled: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set, err := parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("parse = %v", err)
			}
			if got := set.Probes(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Probes = %+v, want %+v", got, tc.want)
			}
		})
	}

	set, err := parse([]byte("probes:\n  deny_cidrs: [10.0.0.0/8]\n"))
	if err != nil {
		t.Fatal(err)
	}
	set.Probes().DenyCIDRs[0] = netip.MustParsePrefix("192.0.2.0/24")
	if got := set.Probes().DenyCIDRs[0]; got != netip.MustParsePrefix("10.0.0.0/8") {
		t.Fatalf("Probes handed out the set's own slice: %v", got)
	}
}

// TestParseRefusesProbes: every mistake in the probes section refuses the
// whole file with a line, a column and a fixed reason that never quotes
// what was written.
func TestParseRefusesProbes(t *testing.T) {
	const planted = "10.9.8.7"
	many := "probes:\n  deny_cidrs:\n" + strings.Repeat("    - 10.0.0.0/8\n", maxDenyCIDRs+1)
	tests := []struct {
		name, src, want string
	}{
		{"not a mapping", "probes: [x]\n",
			"line 1, column 9: probes must map enabled and deny_cidrs to their values"},
		{"scalar", "probes: off\n",
			"line 1, column 9: probes must map enabled and deny_cidrs to their values"},
		{"unknown key", "probes:\n  " + planted + ": true\n",
			"line 2, column 3: unknown key under probes; only enabled and deny_cidrs are allowed"},
		{"duplicate key", "probes:\n  enabled: true\n  enabled: false\n",
			"line 3, column 3: enabled is already set at line 2"},
		{"enabled not a boolean", "probes:\n  enabled: " + planted + "\n",
			"line 2, column 12: enabled must be true or false"},
		{"enabled quoted", "probes:\n  enabled: \"false\"\n",
			"line 2, column 12: enabled must be true or false"},
		{"enabled yes", "probes:\n  enabled: yes\n",
			"line 2, column 12: enabled must be true or false"},
		{"enabled null", "probes:\n  enabled:\n",
			"line 2, column 3: enabled must be true or false"},
		{"enabled list", "probes:\n  enabled: [true]\n",
			"line 2, column 12: enabled must be true or false"},
		{"deny list a scalar", "probes:\n  deny_cidrs: " + planted + "/8\n",
			"line 2, column 15: deny_cidrs must be a list of address ranges"},
		{"bare address", "probes:\n  deny_cidrs:\n    - " + planted + "\n",
			"line 3, column 7: a deny_cidrs entry must be an address range such as 10.0.0.0/8 or fd00::/8 (write /32 or /128 for one address)"},
		{"bad prefix length", "probes:\n  deny_cidrs: [" + planted + "/33]\n",
			"line 2, column 16: a deny_cidrs entry must be an address range such as 10.0.0.0/8 or fd00::/8 (write /32 or /128 for one address)"},
		{"zoned", "probes:\n  deny_cidrs: [\"fe80::1%eth0/64\"]\n",
			"line 2, column 16: a deny_cidrs entry must be an address range such as 10.0.0.0/8 or fd00::/8 (write /32 or /128 for one address)"},
		{"null entry", "probes:\n  deny_cidrs:\n    -\n",
			"line 3, column 6: a deny_cidrs entry must be an address range such as 10.0.0.0/8 or fd00::/8 (write /32 or /128 for one address)"},
		{"nested entry", "probes:\n  deny_cidrs:\n    - [10.0.0.0/8]\n",
			"line 3, column 7: a deny_cidrs entry must be an address range such as 10.0.0.0/8 or fd00::/8 (write /32 or /128 for one address)"},
		{"too many", many, "line 2, column 3: more than 256 deny_cidrs entries"},
		{"twice", "probes: {}\nprobes: {}\n", "line 2, column 1: probes is already set at line 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse([]byte(tc.src))
			if err == nil {
				t.Fatal("parse accepted the file")
			}
			if err.Error() != tc.want {
				t.Fatalf("parse = %q\nwant      %q", err, tc.want)
			}
			if strings.Contains(err.Error(), planted) {
				t.Fatalf("the error quotes the file: %q", err)
			}
		})
	}
}
