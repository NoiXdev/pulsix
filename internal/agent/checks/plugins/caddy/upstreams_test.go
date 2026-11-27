package caddy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

// healthyPage is a metrics page with the reverse proxy's health gauge as
// Caddy 2.8 and 2.11 print it, next to the HTTP metrics.
const healthyPage = `# HELP caddy_reverse_proxy_upstreams_healthy Health status of reverse proxy upstreams.
# TYPE caddy_reverse_proxy_upstreams_healthy gauge
caddy_reverse_proxy_upstreams_healthy{upstream="10.0.0.1:80"} 1
caddy_reverse_proxy_upstreams_healthy{upstream="10.0.0.2:80"} 0
caddy_reverse_proxy_upstreams_healthy{upstream="unix//run/app.sock"} 1
caddy_http_requests_in_flight{handler="reverse_proxy",server="srv0"} 1
caddy_http_requests_total{handler="reverse_proxy",server="srv0"} 7
`

// upstreamsList is the admin API's /reverse_proxy/upstreams answer: the
// three upstreams of the gauge and one without a series (a dynamic one).
const upstreamsList = `[
  {"address": "10.0.0.1:80", "num_requests": 3, "fails": 0},
  {"address": "10.0.0.2:80", "num_requests": 0, "fails": 2},
  {"address": "unix//run/app.sock", "num_requests": 1, "fails": 0},
  {"address": "10.0.0.9:80", "num_requests": 0, "fails": 0}
]`

// adminServer serves the metrics page at /metrics and the upstreams list
// at /reverse_proxy/upstreams, each settable.
type adminServer struct {
	*httptest.Server
	mu                  sync.Mutex
	metrics, ups        string
	metricsCode, upCode int
	uris                []string
}

func newAdminServer(t *testing.T) *adminServer {
	t.Helper()
	s := &adminServer{metrics: healthyPage, ups: upstreamsList, metricsCode: http.StatusOK, upCode: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.uris = append(s.uris, r.URL.RequestURI())
		switch r.URL.Path {
		case "/metrics":
			w.WriteHeader(s.metricsCode)
			_, _ = w.Write([]byte(s.metrics))
		case "/reverse_proxy/upstreams":
			w.WriteHeader(s.upCode)
			_, _ = w.Write([]byte(s.ups))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *adminServer) set(f func(s *adminServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *adminServer) params() map[string]any {
	return map[string]any{"url": s.URL + "/metrics", "upstreams_url": s.URL + "/reverse_proxy/upstreams"}
}

var (
	mustItemValue  = webchecktest.MustItemValue
	mustItemAbsent = webchecktest.MustItemAbsent
)

func mustItemErr(t *testing.T, obs []checks.Observation, item, metric, reason string) {
	t.Helper()
	webchecktest.MustItemErr(t, obs, "caddy", item, metric, reason)
}

func collect(t *testing.T, p *plugin, params map[string]any) []checks.Observation {
	t.Helper()
	obs, err := p.Collect(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	return obs
}

// No upstreams_url, no change: one request, the old metrics only, even
// with the health gauge on the page.
func TestCaddyWithoutUpstreamsURLReadsOnlyMetrics(t *testing.T) {
	srv := newAdminServer(t)
	p, _ := newTestPlugin()
	obs := collect(t, p, map[string]any{"url": srv.URL + "/metrics"})
	srv.mu.Lock()
	uris := slices.Clone(srv.uris)
	srv.mu.Unlock()
	if !slices.Equal(uris, []string{"/metrics"}) {
		t.Fatalf("requests %v, want only the metrics page", uris)
	}
	// up, in flight and the 5xx rate not enabled; the request rate waits
	// for its second round.
	if len(obs) != 3 {
		t.Fatalf("%d observations, want 3: %+v", len(obs), obs)
	}
	for _, o := range obs {
		if o.Item != "" {
			t.Fatalf("item without upstreams_url: %+v", o)
		}
	}
}

func TestCaddyUpstreamHealthJoin(t *testing.T) {
	srv := newAdminServer(t)
	p, _ := newTestPlugin()
	obs := collect(t, p, srv.params())
	mustValue(t, obs, "caddy.up", 1)
	mustValue(t, obs, "caddy.requests_in_flight", 1)
	mustValue(t, obs, "caddy.admin_up", 1)
	mustValue(t, obs, "caddy.upstreams_unreported", 0)
	mustValue(t, obs, "caddy.upstreams_unhealthy", 1)
	mustItemValue(t, obs, "upstream:10.0.0.1:80", "caddy.upstream_healthy", 1)
	mustItemValue(t, obs, "upstream:10.0.0.1:80", "caddy.upstream_requests", 3)
	mustItemValue(t, obs, "upstream:10.0.0.1:80", "caddy.upstream_fails", 0)
	mustItemValue(t, obs, "upstream:10.0.0.2:80", "caddy.upstream_healthy", 0)
	mustItemValue(t, obs, "upstream:10.0.0.2:80", "caddy.upstream_fails", 2)
	mustItemValue(t, obs, "upstream:unix//run/app.sock", "caddy.upstream_requests", 1)
	mustItemErr(t, obs, "upstream:10.0.0.9:80", "caddy.upstream_healthy", reasonNotReported)
	mustItemValue(t, obs, "upstream:10.0.0.9:80", "caddy.upstream_requests", 0)
	if items := webchecktest.Items(obs); len(items) != 5 {
		t.Fatalf("items %v, want \"\" and four upstreams", items)
	}
}

// The metrics page failing leaves the upstream list readable: the counts
// stay, the health carries the page's reason.
func TestCaddyMetricsDownKeepsTheUpstreamCounts(t *testing.T) {
	srv := newAdminServer(t)
	srv.set(func(s *adminServer) { s.metricsCode = http.StatusForbidden })
	p, _ := newTestPlugin()
	obs := collect(t, p, srv.params())
	mustValue(t, obs, "caddy.up", 0)
	mustValue(t, obs, "caddy.admin_up", 1)
	webchecktest.MustErr(t, obs, "caddy", "caddy.upstreams_unhealthy", "permission denied (HTTP 403)")
	mustItemErr(t, obs, "upstream:10.0.0.1:80", "caddy.upstream_healthy", "permission denied (HTTP 403)")
	mustItemValue(t, obs, "upstream:10.0.0.1:80", "caddy.upstream_requests", 3)
}

// A page without any health series (none of the listed upstreams has one)
// says so rather than counting zero unhealthy upstreams.
func TestCaddyNoHealthSeries(t *testing.T) {
	srv := newAdminServer(t)
	srv.set(func(s *adminServer) { s.metrics = page(1, 2, 0) })
	p, _ := newTestPlugin()
	obs := collect(t, p, srv.params())
	webchecktest.MustErr(t, obs, "caddy", "caddy.upstreams_unhealthy", reasonNotReported)
	mustItemErr(t, obs, "upstream:10.0.0.1:80", "caddy.upstream_healthy", reasonNotReported)

	srv.set(func(s *adminServer) { s.ups = "[]" })
	obs = collect(t, p, srv.params())
	mustValue(t, obs, "caddy.upstreams_unhealthy", 0)
	if items := webchecktest.Items(obs); len(items) != 1 {
		t.Fatalf("items %v, want only \"\"", items)
	}
}

func TestParseHealthyGauge(t *testing.T) {
	body := `caddy_reverse_proxy_upstreams_healthy{upstream="a:1"} 1
caddy_reverse_proxy_upstreams_healthy{upstream="b:1"} 0
caddy_reverse_proxy_upstreams_healthy{upstream="c:1"} 0.5
caddy_reverse_proxy_upstreams_healthy{upstream="d:1"} NaN
caddy_reverse_proxy_upstreams_healthy{upstream="e\"q:1",other="x"} 1 1700000000000
caddy_reverse_proxy_upstreams_healthy{upstream="a:1"} 0
caddy_reverse_proxy_upstreams_healthy{upstream=broken} 1
caddy_reverse_proxy_upstreams_healthy 1
caddy_reverse_proxy_upstreams_healthy_total{upstream="f:1"} 1
# caddy_reverse_proxy_upstreams_healthy{upstream="g:1"} 1
`
	got := parseHealthy([]byte(body))
	want := map[string]bool{"a:1": true, "b:1": false, `e"q:1`: true}
	if len(got) != len(want) {
		t.Fatalf("parseHealthy = %v, want %v", got, want)
	}
	for k, v := range want {
		if h, ok := got[k]; !ok || h != v {
			t.Fatalf("parseHealthy = %v, want %v", got, want)
		}
	}
}

func TestCaddyUpstreamsCapsItems(t *testing.T) {
	var list []string
	for i := range 70 {
		list = append(list, fmt.Sprintf(`{"address": "10.0.%d.1:80", "num_requests": 0, "fails": 0}`, i))
	}
	list = append(list, `{"address": "a\u0001b:80"}`, `{"address": "10.0.0.1:80", "num_requests": 9}`)
	srv := newAdminServer(t)
	srv.set(func(s *adminServer) { s.ups = "[" + strings.Join(list, ",") + "]" })
	p, _ := newTestPlugin()
	obs := collect(t, p, srv.params())
	// 70 distinct reportable addresses, 64 reported; one refused as an
	// item; the duplicate is the same upstream, reported once (first wins).
	mustValue(t, obs, "caddy.upstreams_unreported", 7)
	mustItemValue(t, obs, "upstream:10.0.0.1:80", "caddy.upstream_requests", 0)
	if items := webchecktest.Items(obs); len(items) != 1+maxUpstreams {
		t.Fatalf("%d items, want %d", len(items), 1+maxUpstreams)
	}
	for _, o := range obs {
		if !checks.ReportableItem(o.Item) {
			t.Fatalf("unreportable item %q", o.Item)
		}
	}
}

func TestCaddyUpstreamsRejectsMalformedPages(t *testing.T) {
	const format = "unexpected response format"
	tests := []struct {
		name, body string
		code       int
		reason     string
	}{
		{"html", "<html>caddy</html>", 200, format},
		{"null", "null", 200, format},
		{"object", `{"address": "a:1"}`, 200, format},
		{"no address", `[{"num_requests": 1}]`, 200, format},
		{"empty address", `[{"address": ""}]`, 200, format},
		{"count not a number", `[{"address": "a:1", "fails": "2"}]`, 200, format},
		{"fraction", `[{"address": "a:1", "fails": 1.5}]`, 200, format},
		{"trailing data", `[] []`, 200, format},
		{"not found", `{"error":"x"}`, 404, "HTTP status 404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newAdminServer(t)
			srv.set(func(s *adminServer) { s.ups, s.upCode = tt.body, tt.code })
			p, _ := newTestPlugin()
			obs := collect(t, p, srv.params())
			mustValue(t, obs, "caddy.up", 1)
			mustValue(t, obs, "caddy.admin_up", 0)
			webchecktest.MustErr(t, obs, "caddy", "caddy.upstreams_unhealthy", tt.reason)
			webchecktest.MustErr(t, obs, "caddy", "caddy.upstreams_unreported", tt.reason)
			if items := webchecktest.Items(obs); len(items) != 1 {
				t.Fatalf("items %v on a failed round with nothing known", items)
			}
		})
	}
}

// A negative count (a server bug) reads 0, never a negative gauge.
func TestCaddyNegativeCountsReadZero(t *testing.T) {
	srv := newAdminServer(t)
	srv.set(func(s *adminServer) { s.ups = `[{"address": "10.0.0.1:80", "num_requests": -1, "fails": -3}]` })
	p, _ := newTestPlugin()
	obs := collect(t, p, srv.params())
	mustItemValue(t, obs, "upstream:10.0.0.1:80", "caddy.upstream_requests", 0)
	mustItemValue(t, obs, "upstream:10.0.0.1:80", "caddy.upstream_fails", 0)
}

func TestCaddyAdminDownMarksKnownItems(t *testing.T) {
	srv := newAdminServer(t)
	p, _ := newTestPlugin()
	collect(t, p, srv.params())

	srv.set(func(s *adminServer) { s.upCode = http.StatusInternalServerError })
	obs := collect(t, p, srv.params())
	mustValue(t, obs, "caddy.admin_up", 0)
	for _, m := range upstreamMetrics {
		mustItemErr(t, obs, "upstream:10.0.0.2:80", m, "HTTP status 500")
	}

	// A good round forgets an upstream that is gone.
	srv.set(func(s *adminServer) { s.upCode, s.ups = http.StatusOK, `[{"address": "10.0.0.1:80"}]` })
	collect(t, p, srv.params())
	srv.set(func(s *adminServer) { s.upCode = http.StatusInternalServerError })
	obs = collect(t, p, srv.params())
	mustItemErr(t, obs, "upstream:10.0.0.1:80", "caddy.upstream_healthy", "HTTP status 500")
	mustItemAbsent(t, obs, "upstream:10.0.0.2:80", "caddy.upstream_healthy")
}

// The secret goes to both pages; neither it nor the URL reaches a text.
func TestCaddyAdminNeverLeaks(t *testing.T) {
	srv := webchecktest.NewServer(t, "")
	body := fmt.Sprintf(`{"error":"%s %s %s"}`, plantUser, plantPass, srv.URL)
	for _, code := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusBadRequest} {
		srv.Set(code, body)
		p, _ := newTestPlugin()
		params := map[string]any{"url": srv.URL + "/metrics", "upstreams_url": srv.URL + "/reverse_proxy/upstreams", "secret": "s"}
		obs, err := p.CollectWithSecret(t.Context(), params, map[string]string{"basic_auth_user": plantUser, "basic_auth_password": plantPass})
		noSecretIn(t, srv, obs, err)
		mustValue(t, obs, "caddy.admin_up", 0)
	}
	auths := srv.Auths()
	if len(auths) != 6 || slices.Contains(auths, "") {
		t.Fatalf("auth headers %q, want the login on 2 requests per round", auths)
	}
}

func TestCaddyUpstreamsCallerContextEnding(t *testing.T) {
	srv := newAdminServer(t)
	p, _ := newTestPlugin()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	obs, err := p.Collect(ctx, srv.params())
	if err == nil || obs != nil {
		t.Fatalf("Collect = %+v, %v; want nothing and the context's error", obs, err)
	}
	if len(p.upstreams) != 0 {
		t.Fatal("a cancelled round moved the upstream state")
	}
}

// The join is exact on the address as each source writes it, after its own
// escapes are undone: Prometheus label escapes on the metrics page, JSON
// escapes in the admin list (backlog 3.11b review L3). IPv6 brackets, a
// placeholder dial, a backslash, a quote and a non-ASCII host all match.
func TestCaddyUpstreamAddressForms(t *testing.T) {
	srv := newAdminServer(t)
	srv.set(func(s *adminServer) {
		s.metrics = `caddy_reverse_proxy_upstreams_healthy{upstream="[::1]:8080"} 1
caddy_reverse_proxy_upstreams_healthy{upstream="[fe80::1%eth0]:80"} 0
caddy_reverse_proxy_upstreams_healthy{upstream="{http.request.host}:8080"} 1
caddy_reverse_proxy_upstreams_healthy{upstream="odd\\host\"q:1"} 0
caddy_reverse_proxy_upstreams_healthy{upstream="bücher.example:443"} 1
caddy_reverse_proxy_upstreams_healthy{upstream="::1:8080"} 0
`
		s.ups = `[
  {"address": "[::1]:8080", "num_requests": 1},
  {"address": "[fe80::1%eth0]:80"},
  {"address": "{http.request.host}:8080"},
  {"address": "odd\\host\"q:1"},
  {"address": "bücher.example:443"}
]`
	})
	p, _ := newTestPlugin()
	obs := collect(t, p, srv.params())
	mustItemValue(t, obs, "upstream:[::1]:8080", "caddy.upstream_healthy", 1)
	mustItemValue(t, obs, "upstream:[::1]:8080", "caddy.upstream_requests", 1)
	mustItemValue(t, obs, "upstream:[fe80::1%eth0]:80", "caddy.upstream_healthy", 0)
	mustItemValue(t, obs, "upstream:{http.request.host}:8080", "caddy.upstream_healthy", 1)
	mustItemValue(t, obs, `upstream:odd\host"q:1`, "caddy.upstream_healthy", 0)
	mustItemValue(t, obs, "upstream:bücher.example:443", "caddy.upstream_healthy", 1)
	// The unbracketed series names no listed upstream and is not joined.
	mustItemAbsent(t, obs, "upstream:::1:8080", "caddy.upstream_healthy")
	mustValue(t, obs, "caddy.upstreams_unhealthy", 2)
	if items := webchecktest.Items(obs); len(items) != 6 {
		t.Fatalf("items %v, want \"\" and five upstreams", items)
	}
}
