package checkplugins

// zfsPlugin: ZFS pool health (backlog 3.9a). pools optionally limits the
// check to the listed pools; the whole-host item "" (the pool count,
// host-level errors) is reported whatever the list says.
func zfsPlugin() Descriptor {
	return Descriptor{
		Name:                "zfs",
		Runs:                Agent,
		Params:              []ParamSpec{hardwareList("pools", FormatNoControl)},
		ItemParam:           "pools",
		HostItemAlwaysAsked: true,
		Metrics: []MetricSpec{
			{Name: "zfs.state", Kind: Discrete},
			{Name: "zfs.capacity_pct", Kind: Numeric},
			{Name: "zfs.size_bytes", Kind: Numeric},
			{Name: "zfs.free_bytes", Kind: Numeric},
			{Name: "zfs.fragmentation_pct", Kind: Numeric},
			{Name: "zfs.errors_read", Kind: Numeric},
			{Name: "zfs.errors_write", Kind: Numeric},
			{Name: "zfs.errors_checksum", Kind: Numeric},
			{Name: "zfs.data_errors", Kind: Numeric},
			{Name: "zfs.devices_unhealthy", Kind: Numeric},
			{Name: "zfs.scan", Kind: Discrete},
			{Name: "zfs.scan_pct", Kind: Numeric},
			{Name: "zfs.scrub_age_days", Kind: Numeric},
			{Name: "zfs.pools", Kind: Numeric},
		},
	}
}
