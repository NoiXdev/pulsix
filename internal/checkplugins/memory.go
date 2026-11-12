package checkplugins

// memoryPlugin: memory and swap usage of the whole host. No parameters.
func memoryPlugin() Descriptor {
	return Descriptor{
		Name: "memory",
		Runs: Agent,
		Metrics: []MetricSpec{
			{Name: "mem.used_pct", Kind: Numeric},
			{Name: "mem.available_bytes", Kind: Numeric},
			{Name: "swap.used_pct", Kind: Numeric},
		},
	}
}
