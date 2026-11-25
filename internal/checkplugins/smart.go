package checkplugins

// smartPlugin: disk health through smartctl (backlog 3.9). devices
// optionally limits the check to the listed device paths, which smartctl is
// handed as they are — hence FormatDevicePath. The whole-host item "" is
// reported whatever the list says, and a disk behind a RAID controller,
// reported as "<path> [<type>]", is asked when its path is listed.
// smart.power_mode (backlog 3.9b) says whether a disk sleeps; while it
// does, the agent leaves it asleep and reports nothing else, so its other
// metrics are held (HeldWhile): their last states and alerts wait for it
// to wake.
func smartPlugin() Descriptor {
	return Descriptor{
		Name:                "smart",
		Runs:                Agent,
		Params:              []ParamSpec{hardwareList("devices", FormatDevicePath)},
		ItemParam:           "devices",
		HostItemAlwaysAsked: true,
		ItemEntry:           smartItemPath,
		HeldWhile:           &HeldState{Metric: smartPowerMode, Labels: []string{SmartPowerModeStandby}},
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
// HeldWhile cannot drift from what it reports.
const (
	// SmartPowerModeStandby: smartctl left the disk asleep and read
	// nothing from it.
	SmartPowerModeStandby = "standby"
	// SmartPowerModeActive: the disk answered with its readings.
	SmartPowerModeActive = "active"
)
