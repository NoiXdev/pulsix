package checkplugins

import "slices"

// zfsPlugin: ZFS pool health (backlog 3.9a). pools optionally limits the
// check to the listed pools; the whole-host item "" (the pool count,
// host-level errors) is reported whatever the list says. optional_pools
// (backlog 3.74) names pools that may be absent: one zpool list does not
// print reports only zfs.state EXPORTED, while a pool only in pools that
// is absent reports only zfs.state MISSING.
func zfsPlugin() Descriptor {
	return Descriptor{
		Name: "zfs",
		Runs: Agent,
		Params: []ParamSpec{
			hardwareList("pools", FormatNoControl),
			hardwareList("optional_pools", FormatNoControl),
		},
		ItemParam:           "pools",
		HostItemAlwaysAsked: true,
		ItemAsked:           zfsItemAsked,
		MetricAskedForItem:  zfsMetricAskedForItem,
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

// zfsState is the per-pool metric that carries the pool's state.
const zfsState = "zfs.state"

// The labels the agent's zfs plugin reports as zfs.state for a named pool
// zpool list does not print. The agent's plugin
// (internal/agent/checks/plugins/zfs) reports these very constants, so the
// hook below cannot drift from what it reads.
const (
	// ZFSStateExported: absent, and named in optional_pools.
	ZFSStateExported = "EXPORTED"
	// ZFSStateMissing: absent, and named only in pools.
	ZFSStateMissing = "MISSING"
)

// zfsItemAsked: the agent reports the imported pools named in either list,
// or, while pools is empty, every imported pool besides the optional ones —
// optional_pools never narrows. entries is pools.
func zfsItemAsked(params map[string]any, entries []string, item string) bool {
	if len(entries) == 0 {
		return true
	}
	optional, _ := stringList(params["optional_pools"])
	return slices.Contains(entries, item) || slices.Contains(optional, item)
}

// zfsMetricAskedForItem: an EXPORTED or MISSING pool reports zfs.state and
// nothing else, so while its current state reads either, no other metric
// of it is asked; zfs.state always is, which is how the pool leaves that
// state again.
func zfsMetricAskedForItem(_ map[string]any, labels map[string]string, metric string) bool {
	if metric == zfsState {
		return true
	}
	switch labels[zfsState] {
	case ZFSStateExported, ZFSStateMissing:
		return false
	}
	return true
}
