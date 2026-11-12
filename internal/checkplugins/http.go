package checkplugins

// httpPlugin: fetch a URL from the Pulsix server (backlog 3.13). There is
// no default URL; {address} in it stands for the host's address, which
// FormatURL accounts for.
func httpPlugin() Descriptor {
	return Descriptor{
		Name: "http",
		Runs: Server,
		Params: []ParamSpec{
			{Name: "url", Type: TypeString, Required: true, MaxBytes: maxParamStringLen, Format: FormatURL, Example: "https://{address}/"},
			{Name: "method", Type: TypeEnum, Options: []string{"GET", "HEAD"}, Default: "GET"},
			syntheticText("body_contains", true),
			syntheticTimeout(60, 10),
			{Name: "follow_redirects", Type: TypeBool, Default: true},
			{Name: "verify_tls", Type: TypeBool, Default: true},
		},
		Metrics: []MetricSpec{
			{Name: "http.up", Kind: Numeric},
			{Name: "http.status_code", Kind: Numeric},
			{Name: "http.response_ms", Kind: Numeric},
			{Name: "http.body_match", Kind: Numeric},
		},
	}
}
