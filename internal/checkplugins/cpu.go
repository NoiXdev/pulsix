package checkplugins

// cpuPlugin: CPU utilisation and load averages of the whole host. No
// parameters.
func cpuPlugin() Descriptor {
	return Descriptor{
		Name: "cpu",
		Runs: Agent,
		Metrics: []MetricSpec{
			{Name: "cpu.used_pct", Kind: Numeric},
			{Name: "cpu.load1", Kind: Numeric},
			{Name: "cpu.load5", Kind: Numeric},
			{Name: "cpu.load15", Kind: Numeric},
		},
	}
}
