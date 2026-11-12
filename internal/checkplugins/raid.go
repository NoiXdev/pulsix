package checkplugins

// raidPlugin: software RAID arrays (backlog 3.9). arrays optionally limits
// the check to the listed arrays; the whole-host item "" (the array count,
// host-level errors) is reported whatever the list says.
func raidPlugin() Descriptor {
	return Descriptor{
		Name:                "raid",
		Runs:                Agent,
		Params:              []ParamSpec{hardwareList("arrays", FormatNoControl)},
		ItemParam:           "arrays",
		HostItemAlwaysAsked: true,
		Metrics: []MetricSpec{
			{Name: "raid.state", Kind: Discrete},
			{Name: "raid.devices_total", Kind: Numeric},
			{Name: "raid.devices_active", Kind: Numeric},
			{Name: "raid.devices_failed", Kind: Numeric},
			{Name: "raid.sync_pct", Kind: Numeric},
			{Name: "raid.arrays", Kind: Numeric},
		},
	}
}
