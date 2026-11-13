package checkplugins

// maxPHPFPMPools bounds phpfpm's urls list; the agent's phpfpm plugin
// fetches at most as many pools at once.
const maxPHPFPMPools = 16

// phpfpmPlugin: PHP-FPM pool health from each pool's status page (backlog
// 3.11), served by the web server in front of FPM. Each urls entry is one
// pool and the item its metrics are reported on, so urls is the ItemParam;
// item "" carries phpfpm.pools_down whatever the list says. Absent or
// empty, urls means the one local default pool, shown as the example: a
// list parameter carries no Default (the API types a default as a string,
// a number or a boolean). An optional secret holds HTTP
// basic-authentication credentials in the host's agent.yaml.
func phpfpmPlugin() Descriptor {
	return Descriptor{
		Name: "phpfpm",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "urls", Type: TypeList, MaxEntries: maxPHPFPMPools, MaxBytes: maxParamStringLen, Format: FormatStatusURL, Example: "http://127.0.0.1/status"},
			SecretParam(),
			{Name: "verify_tls", Type: TypeBool, Default: true},
		},
		ItemParam:           "urls",
		HostItemAlwaysAsked: true,
		Metrics: []MetricSpec{
			{Name: "phpfpm.pools_down", Kind: Numeric},
			{Name: "phpfpm.up", Kind: Numeric},
			{Name: "phpfpm.processes_active", Kind: Numeric},
			{Name: "phpfpm.processes_idle", Kind: Numeric},
			{Name: "phpfpm.processes_total", Kind: Numeric},
			{Name: "phpfpm.processes_busy_pct", Kind: Numeric},
			{Name: "phpfpm.listen_queue", Kind: Numeric},
			{Name: "phpfpm.requests_per_s", Kind: Numeric},
			{Name: "phpfpm.max_children_reached_per_s", Kind: Numeric},
			{Name: "phpfpm.slow_requests_per_s", Kind: Numeric},
		},
	}
}
