package checkplugins

import "slices"

// apiMetricsAsked returns a MetricAsked hook for a web server plugin whose
// optional URL parameter switches on a set of metrics (backlog 3.11b:
// nginx's api_url, caddy's upstreams_url): those are asked only while the
// parameter is a non-empty string, the agent's own reading of it; every
// other metric always is.
func apiMetricsAsked(param string, gated []string) func(map[string]any, string) bool {
	return func(params map[string]any, metric string) bool {
		if !slices.Contains(gated, metric) {
			return true
		}
		s, _ := params[param].(string)
		return s != ""
	}
}
