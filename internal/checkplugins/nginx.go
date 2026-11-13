package checkplugins

// nginxPlugin: nginx health from its stub_status page (backlog 3.11). The
// page's URL is a parameter (a localhost default); an optional secret holds
// HTTP basic-authentication credentials in the host's agent.yaml. The plugin
// reports the server as a whole, so it has no item parameter.
func nginxPlugin() Descriptor {
	return Descriptor{
		Name: "nginx",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "url", Type: TypeString, MaxBytes: maxParamStringLen, Format: FormatStatusURL, Default: "http://127.0.0.1/nginx_status"},
			SecretParam(),
			{Name: "verify_tls", Type: TypeBool, Default: true},
		},
		Metrics: []MetricSpec{
			{Name: "nginx.up", Kind: Numeric},
			{Name: "nginx.connections_active", Kind: Numeric},
			{Name: "nginx.connections_reading", Kind: Numeric},
			{Name: "nginx.connections_writing", Kind: Numeric},
			{Name: "nginx.connections_waiting", Kind: Numeric},
			{Name: "nginx.requests_per_s", Kind: Numeric},
			{Name: "nginx.connections_accepted_per_s", Kind: Numeric},
			{Name: "nginx.connections_dropped_per_s", Kind: Numeric},
		},
	}
}
