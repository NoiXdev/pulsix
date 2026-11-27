package checkplugins

// caddyPlugin: Caddy health from its Prometheus metrics (backlog 3.11). The
// metrics page's URL is a parameter (the admin endpoint by default; a
// metrics handler on a site works as well). An optional secret holds HTTP
// basic-authentication credentials in the host's agent.yaml. The plugin
// reports the server as a whole, so it has no item parameter.
func caddyPlugin() Descriptor {
	return Descriptor{
		Name: "caddy",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "url", Type: TypeString, MaxBytes: maxParamStringLen, Format: FormatStatusURL, Default: "http://127.0.0.1:2019/metrics"},
			SecretParam(),
			{Name: "verify_tls", Type: TypeBool, Default: true},
			{Name: "upstreams_url", Type: TypeString, MaxBytes: maxParamStringLen, Format: FormatStatusURL, Example: "http://127.0.0.1:2019/reverse_proxy/upstreams"},
		},
		MetricAsked: apiMetricsAsked("upstreams_url", caddyUpstreamMetrics),
		Metrics: []MetricSpec{
			{Name: "caddy.up", Kind: Numeric},
			{Name: "caddy.requests_in_flight", Kind: Numeric},
			{Name: "caddy.requests_per_s", Kind: Numeric},
			{Name: "caddy.errors_5xx_per_s", Kind: Numeric},
			{Name: "caddy.admin_up", Kind: Numeric},
			{Name: "caddy.upstreams_unhealthy", Kind: Numeric},
			{Name: "caddy.upstreams_unreported", Kind: Numeric},
			{Name: "caddy.upstream_healthy", Kind: Numeric},
			{Name: "caddy.upstream_requests", Kind: Numeric},
			{Name: "caddy.upstream_fails", Kind: Numeric},
		},
	}
}

// caddyUpstreamMetrics are the metrics upstreams_url switches on.
var caddyUpstreamMetrics = []string{
	"caddy.admin_up", "caddy.upstreams_unhealthy", "caddy.upstreams_unreported",
	"caddy.upstream_healthy", "caddy.upstream_requests", "caddy.upstream_fails",
}
