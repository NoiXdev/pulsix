package checkplugins

// networkPlugin: traffic, error and drop rates per interface (backlog 3.7).
// interfaces optionally limits the check to the listed interfaces; without
// it every interface the agent does not filter out is reported.
func networkPlugin() Descriptor {
	return Descriptor{
		Name: "network",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "interfaces", Type: TypeList, Format: FormatNotBlank},
		},
		ItemParam: "interfaces",
		Metrics: []MetricSpec{
			{Name: "net.rx_bytes_per_s", Kind: Numeric},
			{Name: "net.tx_bytes_per_s", Kind: Numeric},
			{Name: "net.rx_errors_per_s", Kind: Numeric},
			{Name: "net.tx_errors_per_s", Kind: Numeric},
			{Name: "net.rx_drops_per_s", Kind: Numeric},
			{Name: "net.tx_drops_per_s", Kind: Numeric},
		},
	}
}
