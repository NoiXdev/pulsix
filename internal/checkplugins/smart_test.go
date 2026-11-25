package checkplugins_test

import (
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestSmartAsksForMetricOfASleepingDisk (backlog 3.9b): while a disk's
// current smart.power_mode reads standby the agent leaves it asleep and
// reports nothing else for it, so no other metric of it is asked;
// smart.power_mode itself always is, which is how the disk wakes up
// again. active, or no power mode known (an older agent, a device without
// SMART data), asks for everything, and the whole-host count never
// depends on a disk's labels.
func TestSmartAsksForMetricOfASleepingDisk(t *testing.T) {
	mode := func(label string) map[string]string { return map[string]string{"smart.power_mode": label} }
	tests := []struct {
		name   string
		labels map[string]string
		metric string
		want   bool
	}{
		{"sleeping disk's temperature", mode(checkplugins.SmartPowerModeStandby), "smart.temperature_celsius", false},
		{"sleeping disk's health", mode(checkplugins.SmartPowerModeStandby), "smart.passed", false},
		{"sleeping disk's reallocated sectors", mode(checkplugins.SmartPowerModeStandby), "smart.reallocated_sectors", false},
		{"sleeping disk's NVMe wear", mode(checkplugins.SmartPowerModeStandby), "smart.nvme_percentage_used", false},
		{"sleeping disk's power mode", mode(checkplugins.SmartPowerModeStandby), "smart.power_mode", true},
		{"active disk's temperature", mode(checkplugins.SmartPowerModeActive), "smart.temperature_celsius", true},
		{"active disk's power mode", mode(checkplugins.SmartPowerModeActive), "smart.power_mode", true},
		{"no power mode known", nil, "smart.temperature_celsius", true},
		{"an upper-case label is no mode the agent reports", mode("STANDBY"), "smart.temperature_celsius", true},
		{"the whole-host count", nil, "smart.devices", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkplugins.AsksForMetric("smart", nil, tc.labels, tc.metric); got != tc.want {
				t.Fatalf("AsksForMetric(smart, %v, %q) = %v, want %v", tc.labels, tc.metric, got, tc.want)
			}
		})
	}
	if checkplugins.SmartPowerModeStandby != "standby" || checkplugins.SmartPowerModeActive != "active" {
		t.Fatalf("power mode labels = %q, %q; want standby, active", checkplugins.SmartPowerModeStandby, checkplugins.SmartPowerModeActive)
	}
}

// TestHeldByItemState: a metric the item's current labels keep from being
// asked — a sleeping disk's temperature, an exported pool's capacity —
// is held, not unasked: its last state and alert wait for the item to
// leave that state. A metric the parameters switch off is not held,
// whatever the labels say, and neither is the metric that carries the
// state, or any metric of an item in another state.
func TestHeldByItemState(t *testing.T) {
	standby := map[string]string{"smart.power_mode": checkplugins.SmartPowerModeStandby}
	tests := []struct {
		name   string
		plugin string
		params map[string]any
		labels map[string]string
		metric string
		want   bool
	}{
		{"sleeping disk's temperature", "smart", nil, standby, "smart.temperature_celsius", true},
		{"sleeping disk's power mode", "smart", nil, standby, "smart.power_mode", false},
		{"active disk's temperature", "smart", nil, map[string]string{"smart.power_mode": checkplugins.SmartPowerModeActive}, "smart.temperature_celsius", false},
		{"nothing known", "smart", nil, nil, "smart.temperature_celsius", false},
		{"exported pool's capacity", "zfs", nil, map[string]string{"zfs.state": checkplugins.ZFSStateExported}, "zfs.capacity_pct", true},
		{"missing pool's errors", "zfs", nil, map[string]string{"zfs.state": checkplugins.ZFSStateMissing}, "zfs.errors_read", true},
		{"online pool's capacity", "zfs", nil, map[string]string{"zfs.state": "ONLINE"}, "zfs.capacity_pct", false},
		{"a plugin without a held state", "disk", nil, standby, "disk.used_pct", false},
		{"a metric the parameters switch off", "http", nil, nil, "http.body_match", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkplugins.HeldByItemState(tc.plugin, tc.params, tc.labels, tc.metric); got != tc.want {
				t.Fatalf("HeldByItemState(%s, %v, %q) = %v, want %v", tc.plugin, tc.labels, tc.metric, got, tc.want)
			}
		})
	}
}

// TestHeldStates: the list the metric expiry reads, one entry per plugin
// whose items can wait on a state — the metric that carries the state,
// one of the plugin's own, and the labels in which the item waits.
func TestHeldStates(t *testing.T) {
	got := map[string][]string{}
	for _, h := range checkplugins.HeldStates() {
		got[h.Metric] = h.Labels
	}
	want := map[string][]string{
		"smart.power_mode": {checkplugins.SmartPowerModeStandby},
		"zfs.state":        {checkplugins.ZFSStateExported, checkplugins.ZFSStateMissing},
	}
	if len(got) != len(want) {
		t.Fatalf("HeldStates() = %v, want %v", got, want)
	}
	for metric, labels := range want {
		if !slices.Equal(got[metric], labels) {
			t.Fatalf("HeldStates()[%s] = %v, want %v", metric, got[metric], labels)
		}
	}
	owners := checkplugins.MetricOwners()
	for _, d := range checkplugins.All() {
		if d.HeldWhile != nil && owners[d.HeldWhile.Metric] != d.Name {
			t.Errorf("%s holds on %s, which is not one of its metrics", d.Name, d.HeldWhile.Metric)
		}
	}
}
