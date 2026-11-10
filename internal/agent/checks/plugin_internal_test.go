// package checks: the shared helper's own unit tests (unexported by
// design, exactly the facts_internal_test.go convention this package
// mirrors) plus a check that Registry wires all four plugins under the
// names the manager expects.
package checks

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	managerchecks "github.com/noixdev/pulsix/internal/checks"
)

func TestParamStrings(t *testing.T) {
	cases := []struct {
		name        string
		params      map[string]any
		key         string
		wantValues  []string
		wantPresent bool
		wantErr     bool
	}{
		{
			name:        "absent key",
			params:      map[string]any{},
			key:         "mounts",
			wantPresent: false,
		},
		{
			name:        "nil value treated as absent",
			params:      map[string]any{"mounts": nil},
			key:         "mounts",
			wantPresent: false,
		},
		{
			name:        "native string slice",
			params:      map[string]any{"mounts": []string{"/", "/var"}},
			key:         "mounts",
			wantValues:  []string{"/", "/var"},
			wantPresent: true,
		},
		{
			name:        "json round-trip shape ([]any of strings)",
			params:      map[string]any{"units": []any{"sshd", "cron"}},
			key:         "units",
			wantValues:  []string{"sshd", "cron"},
			wantPresent: true,
		},
		{
			name:        "empty native slice",
			params:      map[string]any{"units": []string{}},
			key:         "units",
			wantValues:  []string{},
			wantPresent: true,
		},
		{
			name:    "non-string element in []any is an error",
			params:  map[string]any{"units": []any{"sshd", 5}},
			key:     "units",
			wantErr: true,
		},
		{
			name:    "wrong shape entirely is an error",
			params:  map[string]any{"units": 5},
			key:     "units",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values, present, err := paramStrings(tc.params, tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("paramStrings: %v", err)
			}
			if present != tc.wantPresent {
				t.Errorf("present = %v, want %v", present, tc.wantPresent)
			}
			if len(values) != len(tc.wantValues) {
				t.Fatalf("values = %v, want %v", values, tc.wantValues)
			}
			for i, w := range tc.wantValues {
				if values[i] != w {
					t.Errorf("values[%d] = %q, want %q", i, values[i], w)
				}
			}
		})
	}
}

// TestParamStringsCopiesNativeSlice: a caller mutating the map's backing
// slice after the call must never observe it through the returned value —
// paramStrings copies rather than aliasing.
func TestParamStringsCopiesNativeSlice(t *testing.T) {
	src := []string{"/"}
	params := map[string]any{"mounts": src}
	values, _, err := paramStrings(params, "mounts")
	if err != nil {
		t.Fatalf("paramStrings: %v", err)
	}
	src[0] = "/mutated"
	if values[0] != "/" {
		t.Errorf("returned slice aliases the caller's backing array: %v", values)
	}
}

func TestValueObservation(t *testing.T) {
	o := valueObservation("/", "disk.used_pct", 42.5)
	if o.Item != "/" || o.Metric != "disk.used_pct" {
		t.Fatalf("observation = %+v", o)
	}
	if o.Value == nil || *o.Value != 42.5 {
		t.Fatalf("value = %v, want 42.5", o.Value)
	}
	if o.Label != nil {
		t.Errorf("a value observation must not carry a label")
	}
	if o.Err != "" {
		t.Errorf("a value observation must not carry an error")
	}
}

func TestLabelObservation(t *testing.T) {
	o := labelObservation("sshd", "service.state", "active")
	if o.Item != "sshd" || o.Metric != "service.state" {
		t.Fatalf("observation = %+v", o)
	}
	if o.Label == nil || *o.Label != "active" {
		t.Fatalf("label = %v, want active", o.Label)
	}
	if o.Value != nil {
		t.Errorf("a label observation must not carry a value")
	}
	if o.Err != "" {
		t.Errorf("a label observation must not carry an error")
	}
}

func TestErrObservation(t *testing.T) {
	o := errObservation("/data", "disk.used_pct", fmt.Errorf("permission denied"))
	if o.Item != "/data" || o.Metric != "disk.used_pct" {
		t.Fatalf("observation = %+v", o)
	}
	if o.Err != "permission denied" {
		t.Fatalf("err = %q, want %q", o.Err, "permission denied")
	}
	if o.Value != nil || o.Label != nil {
		t.Errorf("an error observation must carry neither a value nor a label: %+v", o)
	}
}

// TestErrObservationCutsToTheManagersBound is backlog 7.110. The manager
// cuts an error text at MaxErrLen anyway; the agent cuts first so a long
// mount-point-quoting fault does not travel the wire, and on a UTF-8
// boundary so what it sends is still valid text. A test keeps the agent's
// copy of the bound equal to the manager's.
func TestErrObservationCutsToTheManagersBound(t *testing.T) {
	if maxErrLen != managerchecks.MaxErrLen {
		t.Fatalf("maxErrLen = %d, the manager's contract says %d", maxErrLen, managerchecks.MaxErrLen)
	}
	long := errObservation("/data", "disk.used_pct", fmt.Errorf("%s", strings.Repeat("e", 1<<20)))
	if len(long.Err) != maxErrLen {
		t.Fatalf("1 MiB error = %d bytes, want %d", len(long.Err), maxErrLen)
	}
	split := errObservation("", "m", fmt.Errorf("%s", strings.Repeat("e", maxErrLen-1)+"€€"))
	if len(split.Err) > maxErrLen || !utf8.ValidString(split.Err) || split.Err != strings.Repeat("e", maxErrLen-1) {
		t.Fatalf("a rune on the boundary: %d bytes, valid UTF-8 = %v", len(split.Err), utf8.ValidString(split.Err))
	}
	if o := errObservation("", "m", fmt.Errorf("permission denied")); o.Err != "permission denied" {
		t.Fatalf("a short error was altered: %q", o.Err)
	}
}

// TestRegistryWiresEveryPlugin pins the four names Task 8's scheduler and
// the manager's plugin validation (internal/checks.supportedPlugins) agree
// on; a typo'd registry key would silently orphan a plugin from every
// check definition that names it.
func TestRegistryWiresEveryPlugin(t *testing.T) {
	reg := Registry()
	want := []string{"cpu", "memory", "disk", "service"}
	if len(reg) != len(want) {
		t.Fatalf("Registry() has %d plugins, want %d: %v", len(reg), len(want), reg)
	}
	for _, name := range want {
		p, ok := reg[name]
		if !ok {
			t.Fatalf("Registry() is missing plugin %q", name)
		}
		if p == nil {
			t.Fatalf("Registry()[%q] is nil", name)
		}
		if p.Name() != name {
			t.Errorf("Registry()[%q].Name() = %q, want %q", name, p.Name(), name)
		}
	}
}
