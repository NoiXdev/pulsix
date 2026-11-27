package checkplugins

// nginxPlugin: nginx health from its stub_status page (backlog 3.11). The
// page's URL is a parameter (a localhost default); an optional secret holds
// HTTP basic-authentication credentials in the host's agent.yaml. The plugin
// reports the server as a whole on item "", so it has no item parameter.
// With the optional api_url (the nginx Plus API base, its version included)
// it also reports each server zone ("zone:<name>") and upstream
// ("upstream:<name>") the API lists, at most 64 of each (backlog 3.11b);
// those metrics and nginx.api_* are asked only while api_url is set.
func nginxPlugin() Descriptor {
	return Descriptor{
		Name: "nginx",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "url", Type: TypeString, MaxBytes: maxParamStringLen, Format: FormatStatusURL, Default: "http://127.0.0.1/nginx_status"},
			SecretParam(),
			{Name: "verify_tls", Type: TypeBool, Default: true},
			{Name: "api_url", Type: TypeString, MaxBytes: maxParamStringLen, Format: FormatStatusURL, Example: "http://127.0.0.1/api/9"},
		},
		MetricAsked: apiMetricsAsked("api_url", nginxAPIMetrics),
		Metrics: []MetricSpec{
			{Name: "nginx.up", Kind: Numeric},
			{Name: "nginx.connections_active", Kind: Numeric},
			{Name: "nginx.connections_reading", Kind: Numeric},
			{Name: "nginx.connections_writing", Kind: Numeric},
			{Name: "nginx.connections_waiting", Kind: Numeric},
			{Name: "nginx.requests_per_s", Kind: Numeric},
			{Name: "nginx.connections_accepted_per_s", Kind: Numeric},
			{Name: "nginx.connections_dropped_per_s", Kind: Numeric},
			{Name: "nginx.api_up", Kind: Numeric},
			{Name: "nginx.api_items_unreported", Kind: Numeric},
			{Name: "nginx.zone_processing", Kind: Numeric},
			{Name: "nginx.zone_requests_per_s", Kind: Numeric},
			{Name: "nginx.zone_responses_4xx_per_s", Kind: Numeric},
			{Name: "nginx.zone_responses_5xx_per_s", Kind: Numeric},
			{Name: "nginx.zone_received_bytes_per_s", Kind: Numeric},
			{Name: "nginx.zone_sent_bytes_per_s", Kind: Numeric},
			{Name: "nginx.upstream_peers", Kind: Numeric},
			{Name: "nginx.upstream_peers_up", Kind: Numeric},
			{Name: "nginx.upstream_peers_unavailable", Kind: Numeric},
			{Name: "nginx.upstream_active", Kind: Numeric},
			{Name: "nginx.upstream_requests_per_s", Kind: Numeric},
			{Name: "nginx.upstream_responses_5xx_per_s", Kind: Numeric},
			{Name: "nginx.upstream_fails_per_s", Kind: Numeric},
		},
	}
}

// nginxAPIMetrics are the metrics api_url switches on.
var nginxAPIMetrics = []string{
	"nginx.api_up", "nginx.api_items_unreported",
	"nginx.zone_processing", "nginx.zone_requests_per_s", "nginx.zone_responses_4xx_per_s",
	"nginx.zone_responses_5xx_per_s", "nginx.zone_received_bytes_per_s", "nginx.zone_sent_bytes_per_s",
	"nginx.upstream_peers", "nginx.upstream_peers_up", "nginx.upstream_peers_unavailable", "nginx.upstream_active",
	"nginx.upstream_requests_per_s", "nginx.upstream_responses_5xx_per_s", "nginx.upstream_fails_per_s",
}
