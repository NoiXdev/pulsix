package checkplugins_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestValidateZFSParams (backlog 3.74): optional_pools is a list like
// pools, with the same bounds and format.
func TestValidateZFSParams(t *testing.T) {
	runParamCases(t, []paramCase{
		{"zfs empty", "zfs", map[string]any{}, true},
		{"zfs pools", "zfs", map[string]any{"pools": []any{"tank"}}, true},
		{"zfs optional pools", "zfs", map[string]any{"optional_pools": []any{"backup"}}, true},
		{"zfs both lists", "zfs", map[string]any{"pools": []any{"tank"}, "optional_pools": []any{"backup"}}, true},
		{"zfs optional pools 65 entries", "zfs", map[string]any{"optional_pools": anyList(65, "p")}, false},
		{"zfs optional pools empty entry", "zfs", map[string]any{"optional_pools": []any{""}}, false},
		{"zfs optional pools control char", "zfs", map[string]any{"optional_pools": []any{"a\nb"}}, false},
		{"zfs optional pools not a list", "zfs", map[string]any{"optional_pools": "backup"}, false},
	})
}

// TestZFSAsksForItem (backlog 3.74): the pools the agent reports are the
// imported ones named in either list, or every imported pool plus the
// optional ones when pools is empty — so an item named in optional_pools
// is asked, and optional_pools alone narrows nothing.
func TestZFSAsksForItem(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		item   string
		want   bool
	}{
		{"no lists ask for every pool", nil, "tank", true},
		{"a pool on pools", map[string]any{"pools": []any{"tank"}}, "tank", true},
		{"a pool on neither list", map[string]any{"pools": []any{"tank"}}, "rpool", false},
		{"a pool on optional_pools", map[string]any{"pools": []any{"tank"}, "optional_pools": []any{"backup"}}, "backup", true},
		{"optional_pools alone does not narrow", map[string]any{"optional_pools": []any{"backup"}}, "rpool", true},
		{"optional_pools alone asks for its own", map[string]any{"optional_pools": []any{"backup"}}, "backup", true},
		{"an empty pools list with optional_pools", map[string]any{"pools": []any{}, "optional_pools": []any{"backup"}}, "rpool", true},
		{"the whole-host item whatever the lists", map[string]any{"pools": []any{"tank"}, "optional_pools": []any{"backup"}}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkplugins.AsksForItem("zfs", tc.params, tc.item); got != tc.want {
				t.Fatalf("AsksForItem(zfs, %v, %q) = %v, want %v", tc.params, tc.item, got, tc.want)
			}
		})
	}
}

// TestZFSAsksForMetricOfAnAbsentPool (backlog 3.74): while an item's
// current zfs.state reads EXPORTED or MISSING the agent reports nothing
// else for that pool, so no other metric of it is asked; zfs.state itself
// always is, and any other state, or none known, asks for everything.
func TestZFSAsksForMetricOfAnAbsentPool(t *testing.T) {
	state := func(label string) map[string]string { return map[string]string{"zfs.state": label} }
	tests := []struct {
		name   string
		labels map[string]string
		metric string
		want   bool
	}{
		{"exported pool's capacity", state("EXPORTED"), "zfs.capacity_pct", false},
		{"exported pool's scan", state("EXPORTED"), "zfs.scan", false},
		{"exported pool's state", state("EXPORTED"), "zfs.state", true},
		{"missing pool's errors", state("MISSING"), "zfs.errors_read", false},
		{"missing pool's state", state("MISSING"), "zfs.state", true},
		{"online pool's capacity", state("ONLINE"), "zfs.capacity_pct", true},
		{"faulted pool's capacity", state("FAULTED"), "zfs.capacity_pct", true},
		{"no state known", nil, "zfs.capacity_pct", true},
		{"a lower-case label is no state the agent reports", state("exported"), "zfs.capacity_pct", true},
		{"the whole-host count", nil, "zfs.pools", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkplugins.AsksForMetric("zfs", nil, tc.labels, tc.metric); got != tc.want {
				t.Fatalf("AsksForMetric(zfs, %v, %q) = %v, want %v", tc.labels, tc.metric, got, tc.want)
			}
		})
	}
}

// TestAsksForMetricWithoutAnItemHook: a plugin without a
// MetricAskedForItem hook ignores the item's labels — a disk's labels say
// nothing about its metrics, and http's metric rule is its parameters'.
func TestAsksForMetricWithoutAnItemHook(t *testing.T) {
	labels := map[string]string{"zfs.state": "EXPORTED"}
	if !checkplugins.AsksForMetric("disk", nil, labels, "disk.used_pct") {
		t.Fatal("disk stopped asking for disk.used_pct over an item label")
	}
	if checkplugins.AsksForMetric("http", nil, labels, "http.body_match") {
		t.Fatal("http asks for http.body_match without body_contains")
	}
}
