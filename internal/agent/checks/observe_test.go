// package checks: the observation helpers' (observe.go) own unit tests,
// including the pins that keep their bounds equal to the manager's. The
// check that the registry wires every plugin under the names the manager
// expects lives with the registry, in plugins/registry_test.go.
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
			values, present, err := ParamStrings(tc.params, tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParamStrings: %v", err)
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
// ParamStrings copies rather than aliasing.
func TestParamStringsCopiesNativeSlice(t *testing.T) {
	src := []string{"/"}
	params := map[string]any{"mounts": src}
	values, _, err := ParamStrings(params, "mounts")
	if err != nil {
		t.Fatalf("ParamStrings: %v", err)
	}
	src[0] = "/mutated"
	if values[0] != "/" {
		t.Errorf("returned slice aliases the caller's backing array: %v", values)
	}
}

func TestValueObservation(t *testing.T) {
	o := ValueObservation("/", "disk.used_pct", 42.5)
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
	o := LabelObservation("sshd", "service.state", "active")
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
	o := ErrObservation("/data", "disk.used_pct", fmt.Errorf("permission denied"))
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
	long := ErrObservation("/data", "disk.used_pct", fmt.Errorf("%s", strings.Repeat("e", 1<<20)))
	if len(long.Err) != maxErrLen {
		t.Fatalf("1 MiB error = %d bytes, want %d", len(long.Err), maxErrLen)
	}
	split := ErrObservation("", "m", fmt.Errorf("%s", strings.Repeat("e", maxErrLen-1)+"€€"))
	if len(split.Err) > maxErrLen || !utf8.ValidString(split.Err) || split.Err != strings.Repeat("e", maxErrLen-1) {
		t.Fatalf("a rune on the boundary: %d bytes, valid UTF-8 = %v", len(split.Err), utf8.ValidString(split.Err))
	}
	if o := ErrObservation("", "m", fmt.Errorf("permission denied")); o.Err != "permission denied" {
		t.Fatalf("a short error was altered: %q", o.Err)
	}
}

// TestReportableItemMatchesTheManagersContract keeps the agent's copy of
// the item bound on the manager's.
func TestReportableItemMatchesTheManagersContract(t *testing.T) {
	if MaxItemLen != managerchecks.MaxItemLen {
		t.Fatalf("MaxItemLen = %d, the manager's contract says %d", MaxItemLen, managerchecks.MaxItemLen)
	}
	for item, want := range map[string]bool{
		"/":                      true,
		"/srv/data":              true,
		strings.Repeat("a", 512): true,
		strings.Repeat("a", 513): false,
		"/a\tb":                  false,
		"/a\nb":                  false,
		"/a b":                   false,
		"/a\x7fb":                false,
	} {
		if got := ReportableItem(item); got != want {
			t.Errorf("ReportableItem(%q) = %v, want %v", item, got, want)
		}
	}
}

// TestReportableLabelMatchesTheManagersContract keeps the agent's copy of
// the label bound on the manager's (internal/withinContract).
func TestReportableLabelMatchesTheManagersContract(t *testing.T) {
	if MaxLabelLen != managerchecks.MaxLabelLen {
		t.Fatalf("MaxLabelLen = %d, the manager's contract says %d", MaxLabelLen, managerchecks.MaxLabelLen)
	}
	for label, want := range map[string]bool{
		"running":                true,
		"":                       true,
		"données":                true,
		strings.Repeat("a", 512): true,
		strings.Repeat("a", 513): false,
		"a\x00b":                 false,
		"a\xffb":                 false,
	} {
		if got := ReportableLabel(label); got != want {
			t.Errorf("ReportableLabel(%q) = %v, want %v", label, got, want)
		}
	}
}
