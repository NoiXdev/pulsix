package checkplugins_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// Backlog 3.11b: nginx's api_url and caddy's upstreams_url are optional
// status URLs with no default; their example names the page to point at.
func TestWebServerAPIParams(t *testing.T) {
	for _, tc := range []struct{ plugin, param, example string }{
		{"nginx", "api_url", "http://127.0.0.1/api/9"},
	} {
		d, _ := checkplugins.Get(tc.plugin)
		i := slices.IndexFunc(d.Params, func(p checkplugins.ParamSpec) bool { return p.Name == tc.param })
		if i < 0 {
			t.Fatalf("%s has no %s", tc.plugin, tc.param)
		}
		p := d.Params[i]
		if p.Type != checkplugins.TypeString || p.Format != checkplugins.FormatStatusURL || p.MaxBytes != 500 ||
			p.Default != nil || p.Example != tc.example || p.Required {
			t.Fatalf("%s.%s = %+v", tc.plugin, tc.param, p)
		}
	}
}

func TestValidateWebServerAPIParams(t *testing.T) {
	runParamCases(t, []paramCase{
		{"nginx api_url accepted", "nginx", map[string]any{"api_url": "http://127.0.0.1:8080/api/9"}, true},
		{"nginx api_url with a query accepted", "nginx", map[string]any{"api_url": "https://h/api/9/?x=1"}, true},
		{"nginx api_url with user info refused", "nginx", map[string]any{"api_url": "http://u:p@127.0.0.1/api/9"}, false},
		{"nginx api_url not http refused", "nginx", map[string]any{"api_url": "ftp://127.0.0.1/api/9"}, false},
		{"nginx api_url of 501 bytes refused", "nginx", map[string]any{"api_url": "http://h/" + strings.Repeat("x", 492)}, false},
		{"nginx api_url not a string refused", "nginx", map[string]any{"api_url": 9.0}, false},
		{"apache has no api parameter", "apache", map[string]any{"api_url": "http://127.0.0.1/api/9"}, false},
	})
}

// The item metrics are asked only while their URL parameter is a non-empty
// string, the agent's own reading of it; the server's metrics always are.
func TestWebServerAPIMetricsAsked(t *testing.T) {
	for _, tc := range []struct {
		plugin, param string
		gated         []string
		always        []string
	}{
		{"nginx", "api_url",
			[]string{"nginx.api_up", "nginx.api_items_unreported", "nginx.zone_processing", "nginx.zone_requests_per_s",
				"nginx.zone_responses_4xx_per_s", "nginx.zone_responses_5xx_per_s", "nginx.zone_received_bytes_per_s",
				"nginx.zone_sent_bytes_per_s", "nginx.upstream_peers", "nginx.upstream_peers_up",
				"nginx.upstream_peers_unavailable", "nginx.upstream_active", "nginx.upstream_requests_per_s",
				"nginx.upstream_responses_5xx_per_s", "nginx.upstream_fails_per_s"},
			[]string{"nginx.up", "nginx.connections_active", "nginx.requests_per_s"}},
	} {
		d, _ := checkplugins.Get(tc.plugin)
		declared := map[string]checkplugins.MetricKind{}
		for _, m := range d.Metrics {
			declared[m.Name] = m.Kind
		}
		for _, p := range []struct {
			name   string
			params map[string]any
			want   bool
		}{
			{"set", map[string]any{tc.param: "http://127.0.0.1/x"}, true},
			{"empty", map[string]any{tc.param: ""}, false},
			{"absent", map[string]any{"url": "http://127.0.0.1/x"}, false},
			{"no params", nil, false},
			{"not a string", map[string]any{tc.param: true}, false},
		} {
			for _, m := range tc.gated {
				if declared[m] != checkplugins.Numeric {
					t.Fatalf("%s declared as %q, want numeric", m, declared[m])
				}
				if got := checkplugins.AsksForMetric(tc.plugin, p.params, nil, m); got != p.want {
					t.Errorf("%s %s: AsksForMetric(%s) = %v, want %v", tc.plugin, p.name, m, got, p.want)
				}
			}
			for _, m := range tc.always {
				if !checkplugins.AsksForMetric(tc.plugin, p.params, nil, m) {
					t.Errorf("%s %s: %s not asked", tc.plugin, p.name, m)
				}
			}
		}
	}
	for _, m := range []string{"apache.workers_max", "apache.workers_busy_max_pct"} {
		if !checkplugins.AsksForMetric("apache", nil, nil, m) {
			t.Errorf("%s not asked", m)
		}
	}
}
