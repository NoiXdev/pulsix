package checkplugins

// tcpPlugin: connect to a port on the host from the Pulsix server, and
// optionally send something and look for a banner (backlog 3.13). There is
// no default port. send may be empty; the SPA's \r \n \t \\ escapes are
// decoded before it is sent, so the manager sees the bytes themselves.
func tcpPlugin() Descriptor {
	return Descriptor{
		Name: "tcp",
		Runs: Server,
		Params: []ParamSpec{
			syntheticPort(true, nil),
			syntheticText("send", false),
			syntheticText("expect", true),
			syntheticTimeout(30, 5),
		},
		Metrics: []MetricSpec{
			{Name: "tcp.up", Kind: Numeric},
			// The failure reason of an attempt (probe.Failure*): "none" while
			// up, else why it failed.
			{Name: "tcp.failure", Kind: Discrete},
			{Name: "tcp.connect_ms", Kind: Numeric},
			{Name: "tcp.banner_match", Kind: Numeric},
		},
	}
}
