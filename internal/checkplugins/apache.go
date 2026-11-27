package checkplugins

// apachePlugin: Apache httpd health from its mod_status page (backlog
// 3.11). The page's URL is a parameter (a localhost default); the agent adds
// the query key auto when it is missing. An optional secret holds HTTP
// basic-authentication credentials in the host's agent.yaml. The plugin
// reports the server as a whole, so it has no item parameter.
// apache.workers_max is the page's Scoreboard length, the server's
// effective MaxRequestWorkers, and apache.workers_busy_max_pct the busy
// workers against it (backlog 3.11b); apache.workers_busy_pct stays
// against the started workers.
func apachePlugin() Descriptor {
	return Descriptor{
		Name: "apache",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "url", Type: TypeString, MaxBytes: maxParamStringLen, Format: FormatStatusURL, Default: "http://127.0.0.1/server-status?auto"},
			SecretParam(),
			{Name: "verify_tls", Type: TypeBool, Default: true},
		},
		Metrics: []MetricSpec{
			{Name: "apache.up", Kind: Numeric},
			{Name: "apache.workers_busy", Kind: Numeric},
			{Name: "apache.workers_idle", Kind: Numeric},
			{Name: "apache.workers_busy_pct", Kind: Numeric},
			{Name: "apache.requests_per_s", Kind: Numeric},
			{Name: "apache.bytes_per_s", Kind: Numeric},
			{Name: "apache.workers_max", Kind: Numeric},
			{Name: "apache.workers_busy_max_pct", Kind: Numeric},
		},
	}
}
