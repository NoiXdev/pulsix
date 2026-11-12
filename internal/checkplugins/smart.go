package checkplugins

// smartPlugin: disk health through smartctl (backlog 3.9). devices
// optionally limits the check to the listed device paths, which smartctl is
// handed as they are — hence FormatDevicePath. The whole-host item "" is
// reported whatever the list says, and a disk behind a RAID controller,
// reported as "<path> [<type>]", is asked when its path is listed.
func smartPlugin() Descriptor {
	return Descriptor{
		Name:                "smart",
		Runs:                Agent,
		Params:              []ParamSpec{hardwareList("devices", FormatDevicePath)},
		ItemParam:           "devices",
		HostItemAlwaysAsked: true,
		ItemEntry:           smartItemPath,
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
		},
	}
}
