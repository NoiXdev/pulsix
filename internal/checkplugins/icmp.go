package checkplugins

// icmpPlugin: ping the host from the Pulsix server (backlog 3.13).
func icmpPlugin() Descriptor {
	return Descriptor{
		Name: "icmp",
		Runs: Server,
		Params: []ParamSpec{
			{Name: "count", Type: TypeInteger, Min: bound(1), Max: bound(10), Default: int64(3)},
			syntheticTimeout(30, 5),
		},
		Metrics: []MetricSpec{
			{Name: "icmp.up", Kind: Numeric},
			// The failure reason of an attempt (probe.Failure*): "none" while
			// up, else why it failed.
			{Name: "icmp.failure", Kind: Discrete},
			{Name: "icmp.loss_pct", Kind: Numeric},
			{Name: "icmp.rtt_ms", Kind: Numeric},
		},
	}
}
