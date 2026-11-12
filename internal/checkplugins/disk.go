package checkplugins

// diskPlugin: filesystem usage per mount. mounts optionally limits the
// check to the listed mount points; without it every filesystem the host
// finds is reported.
func diskPlugin() Descriptor {
	return Descriptor{
		Name: "disk",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "mounts", Type: TypeList, Format: FormatNotBlank},
		},
		ItemParam: "mounts",
		Metrics: []MetricSpec{
			{Name: "disk.used_pct", Kind: Numeric},
			{Name: "disk.free_bytes", Kind: Numeric},
			{Name: "disk.inodes_used_pct", Kind: Numeric},
		},
	}
}
