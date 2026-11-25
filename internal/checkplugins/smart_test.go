package checkplugins_test

import (
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
