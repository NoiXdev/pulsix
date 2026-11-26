package checkplugins

// tcpPlugin: connect to a port on the host from the Pulsix server, and
// optionally send something and look for a banner (backlog 3.13). There is
// no default port. send may be empty; the SPA's \r \n \t \\ escapes are
// decoded before it is sent, so the manager sees the bytes themselves.
//
// Backlog 3.87 added assertions (withAssertions) on connect_ms,
// response_ms and response, the first line the target sent.
func tcpPlugin() Descriptor {
	return withAssertions(Descriptor{
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
	}, FormatTCPAssertion, tcpAssertionExample)
}

// tcpAssertionExample is a valid tcp assertion.
const tcpAssertionExample = "fast=connect_ms < 500"
