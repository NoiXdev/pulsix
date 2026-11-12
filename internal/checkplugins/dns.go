package checkplugins

// dnsPlugin: resolve a name from the Pulsix server (backlog 3.13), through
// server when one is set and the server's own resolver otherwise. The
// record type is asked verbatim, so its case matters.
func dnsPlugin() Descriptor {
	return Descriptor{
		Name: "dns",
		Runs: Server,
		Params: []ParamSpec{
			{Name: "name", Type: TypeString, MaxBytes: maxParamStringLen, Format: FormatDNSName, Default: "{address}"},
			{Name: "type", Type: TypeEnum, Options: []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS"}, Default: "A"},
			{Name: "server", Type: TypeString, MaxBytes: maxParamStringLen, Format: FormatServer},
			syntheticText("expect", true),
			syntheticTimeout(30, 5),
		},
		Metrics: []MetricSpec{
			{Name: "dns.up", Kind: Numeric},
			{Name: "dns.resolve_ms", Kind: Numeric},
			{Name: "dns.answer_match", Kind: Numeric},
		},
	}
}
