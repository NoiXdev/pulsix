package checkplugins

// smartPlugin: disk health through smartctl (backlog 3.9). devices
// optionally limits the check to the listed device paths, which smartctl is
// handed as they are — hence FormatDevicePath. The whole-host item "" is
// reported whatever the list says, and a disk behind a RAID controller,
// reported as "<path> [<type>]", is asked when its path is listed.
// smart.power_mode (backlog 3.9b) says whether a disk sleeps; while it
// does, the agent leaves it asleep and its other metrics are not asked.
func smartPlugin() Descriptor {
	return Descriptor{
		Name:                "smart",
		Runs:                Agent,
		Params:              []ParamSpec{hardwareList("devices", FormatDevicePath)},
		ItemParam:           "devices",
		HostItemAlwaysAsked: true,
		ItemEntry:           smartItemPath,
		MetricAskedForItem:  smartMetricAskedForItem,
		Metrics: []MetricSpec{
			{Name: "smart.devices", Kind: Numeric},
			{Name: "smart.passed", Kind: Numeric},
			{Name: "smart.temperature_celsius", Kind: Numeric},
			{Name: "smart.power_on_hours", Kind: Numeric},
			{Name: "smart.reallocated_sectors", Kind: Numeric},
			{Name: "smart.pending_sectors", Kind: Numeric},
			{Name: "smart.offline_uncorrectable", Kind: Numeric},
			{Name: "smart.nvme_percentage_used", Kind: Numeric},
			{Name: "smart.nvme_available_spare_pct", Kind: Numeric},
			{Name: "smart.nvme_media_errors", Kind: Numeric},
			{Name: smartPowerMode, Kind: Discrete},
		},
	}
}

// smartPowerMode is the per-device metric that says whether the disk
// sleeps.
const smartPowerMode = "smart.power_mode"

// The labels the agent's smart plugin reports as smart.power_mode for a
// device it queried. The agent's plugin
// (internal/agent/checks/plugins/smart) reports these very constants, so
// the hook below cannot drift from what it reads.
const (
	// SmartPowerModeStandby: smartctl left the disk asleep and read
	// nothing from it.
	SmartPowerModeStandby = "standby"
	// SmartPowerModeActive: the disk answered with its readings.
	SmartPowerModeActive = "active"
)

// smartMetricAskedForItem: a sleeping disk reports smart.power_mode and
// nothing else, so while its current power mode reads standby no other
// metric of it is asked; smart.power_mode always is, which is how the disk
// leaves standby again. Item "" carries no power mode, so smart.devices is
// always asked.
func smartMetricAskedForItem(_ map[string]any, labels map[string]string, metric string) bool {
	return metric == smartPowerMode || labels[smartPowerMode] != SmartPowerModeStandby
}
