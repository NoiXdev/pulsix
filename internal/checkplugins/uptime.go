package checkplugins

// uptimePlugin: seconds since the host booted (backlog 3.7). No parameters.
func uptimePlugin() Descriptor {
	return Descriptor{
		Name: "uptime",
		Runs: Agent,
		Metrics: []MetricSpec{
			{Name: "uptime.seconds", Kind: Numeric},
		},
	}
}
