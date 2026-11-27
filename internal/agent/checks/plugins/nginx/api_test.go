package nginx

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

// zonesPage is a /api/9/http/server_zones answer as the nginx Plus API
// documents it (ssl and codes trimmed to what matters, plus a field the
// agent does not read).
func zonesPage(requests, r4xx, r5xx, received, sent int) string {
	return fmt.Sprintf(`{
  "site1": {
    "processing": 2,
    "requests": %d,
    "responses": {"1xx": 0, "2xx": 727290, "3xx": 4614, "4xx": %d, "5xx": %d, "codes": {"200": 727290}, "total": 734373},
    "discarded": 2020,
    "received": %d,
    "sent": %d,
    "ssl": {"handshakes": 104303, "handshakes_failed": 1421, "session_reuses": 54645}
  },
  "site2": {"processing": 0, "requests": 5, "responses": {"4xx": 0, "5xx": 0}, "received": 10, "sent": 20}
}`, requests, r4xx, r5xx, received, sent)
}

// upstreamsPage is a /api/9/http/upstreams answer: upstream "backend"
// with three peers (up, unavail, a backup that is unhealthy) and "empty"
// with none.
func upstreamsPage(requests, r5xx, fails int) string {
	return fmt.Sprintf(`{
  "backend": {
    "peers": [
      {"id": 0, "server": "10.0.0.1:8088", "name": "10.0.0.1:8088", "backup": false, "weight": 5, "state": "up",
       "active": 3, "requests": %d, "header_time": 20, "response_time": 36,
       "responses": {"1xx": 0, "2xx": 666310, "3xx": 0, "4xx": 915, "5xx": %d, "codes": {"200": 666310}, "total": 667231},
       "sent": 251946292, "received": 19222475454, "fails": %d, "unavail": 0,
       "health_checks": {"checks": 26214, "fails": 0, "unhealthy": 0, "last_passed": true},
       "downtime": 0, "downstart": "2022-06-28T11:09:21.602Z", "selected": "2022-06-28T15:01:25Z"},
      {"id": 1, "server": "10.0.0.2:8088", "state": "unavail", "active": 0, "requests": 10, "responses": {"5xx": 1}, "fails": 4},
      {"id": 2, "server": "10.0.0.3:8088", "backup": true, "state": "unhealthy", "active": 1, "requests": 0, "responses": {"5xx": 0}, "fails": 0}
    ],
    "keepalive": 0,
    "zombies": 0,
    "zone": "backend"
  },
  "empty": {"peers": [], "keepalive": 0, "zombies": 0, "zone": "empty"}
}`, requests, r5xx, fails)
}

// apiServer serves stub_status at /nginx_status and the two API pages
// under /api/9/, each settable, and records every request URI.
type apiServer struct {
	*httptest.Server
	mu                     sync.Mutex
	stub, zones, upstreams string
	zonesCode, upsCode     int
	uris                   []string
}

func newAPIServer(t *testing.T) *apiServer {
	t.Helper()
	s := &apiServer{
		stub:      page(10, 100, 100, 200, 1, 2, 7),
		zones:     zonesPage(1000, 10, 2, 5000, 90000),
		upstreams: upstreamsPage(2000, 6, 1),
		zonesCode: http.StatusOK, upsCode: http.StatusOK,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.uris = append(s.uris, r.URL.RequestURI())
		switch r.URL.Path {
		case "/nginx_status":
			_, _ = w.Write([]byte(s.stub))
		case "/api/9/http/server_zones":
			w.WriteHeader(s.zonesCode)
			_, _ = w.Write([]byte(s.zones))
		case "/api/9/http/upstreams":
			w.WriteHeader(s.upsCode)
			_, _ = w.Write([]byte(s.upstreams))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *apiServer) set(f func(s *apiServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *apiServer) requestURIs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.uris)
}

func (s *apiServer) params() map[string]any {
	return map[string]any{"url": s.URL + "/nginx_status", "api_url": s.URL + "/api/9"}
}

var (
	mustItemValue  = webchecktest.MustItemValue
	mustItemAbsent = webchecktest.MustItemAbsent
)

func mustItemErr(t *testing.T, obs []checks.Observation, item, metric, reason string) {
	t.Helper()
	webchecktest.MustItemErr(t, obs, "nginx", item, metric, reason)
}

func collect(t *testing.T, p *plugin, params map[string]any) []checks.Observation {
	t.Helper()
	obs, err := p.Collect(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	return obs
}

// No api_url, no change: one request, the old metrics and nothing else.
func TestNginxWithoutAPIURLReadsOnlyStubStatus(t *testing.T) {
	srv := newAPIServer(t)
	p, _ := newTestPlugin()
	obs := collect(t, p, map[string]any{"url": srv.URL + "/nginx_status"})
	if got := srv.requestURIs(); !slices.Equal(got, []string{"/nginx_status"}) {
		t.Fatalf("requests %v, want only the stub_status page", got)
	}
	if len(obs) != 5 {
		t.Fatalf("%d observations, want the 5 of a first stub_status round: %+v", len(obs), obs)
	}
	for _, o := range obs {
		if o.Item != "" || strings.HasPrefix(o.Metric, "nginx.api_") {
			t.Fatalf("API observation without api_url: %+v", o)
		}
	}
}

func TestNginxAPIZonesAndUpstreams(t *testing.T) {
	srv := newAPIServer(t)
	p, clk := newTestPlugin()

	obs := collect(t, p, srv.params())
	mustValue(t, obs, "nginx.up", 1)
	mustValue(t, obs, "nginx.connections_active", 10)
	mustValue(t, obs, "nginx.api_up", 1)
	mustValue(t, obs, "nginx.api_items_unreported", 0)
	mustItemValue(t, obs, "zone:site1", "nginx.zone_processing", 2)
	mustItemValue(t, obs, "zone:site2", "nginx.zone_processing", 0)
	mustItemValue(t, obs, "upstream:backend", "nginx.upstream_peers", 3)
	mustItemValue(t, obs, "upstream:backend", "nginx.upstream_peers_up", 1)
	mustItemValue(t, obs, "upstream:backend", "nginx.upstream_peers_unavailable", 2)
	mustItemValue(t, obs, "upstream:backend", "nginx.upstream_active", 4)
	mustItemValue(t, obs, "upstream:empty", "nginx.upstream_peers", 0)
	mustItemValue(t, obs, "upstream:empty", "nginx.upstream_peers_up", 0)
	for _, m := range zoneRates {
		mustItemAbsent(t, obs, "zone:site1", m)
	}
	for _, m := range upstreamRates {
		mustItemAbsent(t, obs, "upstream:backend", m)
	}
	if items := webchecktest.Items(obs); len(items) != 5 {
		t.Fatalf("items %v, want \"\", two zones and two upstreams", items)
	}

	clk.Advance(10 * time.Second)
	srv.set(func(s *apiServer) {
		s.zones = zonesPage(1500, 30, 12, 15000, 190000)
		s.upstreams = upstreamsPage(2100, 16, 21)
	})
	obs = collect(t, p, srv.params())
	mustItemValue(t, obs, "zone:site1", "nginx.zone_requests_per_s", 50)
	mustItemValue(t, obs, "zone:site1", "nginx.zone_responses_4xx_per_s", 2)
	mustItemValue(t, obs, "zone:site1", "nginx.zone_responses_5xx_per_s", 1)
	mustItemValue(t, obs, "zone:site1", "nginx.zone_received_bytes_per_s", 1000)
	mustItemValue(t, obs, "zone:site1", "nginx.zone_sent_bytes_per_s", 10000)
	mustItemValue(t, obs, "zone:site2", "nginx.zone_requests_per_s", 0)
	mustItemValue(t, obs, "upstream:backend", "nginx.upstream_requests_per_s", 10)
	mustItemValue(t, obs, "upstream:backend", "nginx.upstream_responses_5xx_per_s", 1)
	mustItemValue(t, obs, "upstream:backend", "nginx.upstream_fails_per_s", 2)
	mustItemValue(t, obs, "upstream:empty", "nginx.upstream_requests_per_s", 0)
}

// A peer added, removed or replaced makes the upstream's sums jump, so its
// rates start over; a zone's counters falling (a restart) restart them too.
func TestNginxAPIPeerSetChangeRestartsRates(t *testing.T) {
	srv := newAPIServer(t)
	p, clk := newTestPlugin()
	collect(t, p, srv.params())

	clk.Advance(10 * time.Second)
	srv.set(func(s *apiServer) {
		s.upstreams = strings.Replace(upstreamsPage(2100, 16, 21), `"10.0.0.3:8088"`, `"10.0.0.4:8088"`, 1)
		s.zones = zonesPage(10, 0, 0, 0, 0)
	})
	obs := collect(t, p, srv.params())
	for _, m := range upstreamRates {
		mustItemAbsent(t, obs, "upstream:backend", m)
	}
	for _, m := range zoneRates {
		mustItemAbsent(t, obs, "zone:site1", m)
	}
	mustItemValue(t, obs, "upstream:empty", "nginx.upstream_requests_per_s", 0)

	clk.Advance(10 * time.Second)
	srv.set(func(s *apiServer) {
		s.upstreams = strings.Replace(upstreamsPage(2200, 16, 21), `"10.0.0.3:8088"`, `"10.0.0.4:8088"`, 1)
	})
	obs = collect(t, p, srv.params())
	mustItemValue(t, obs, "upstream:backend", "nginx.upstream_requests_per_s", 10)
}

func TestNginxAPICapsItems(t *testing.T) {
	var zones, ups []string
	for i := range 70 {
		zones = append(zones, fmt.Sprintf(`"z%02d": {"requests": 1}`, i))
	}
	zones = append(zones, `"a\u0001b": {"requests": 1}`)
	for i := range 66 {
		ups = append(ups, fmt.Sprintf(`"u%02d": {"peers": [{"id": 0, "server": "s", "state": "up"}]}`, i))
	}
	srv := newAPIServer(t)
	srv.set(func(s *apiServer) {
		s.zones = "{" + strings.Join(zones, ",") + "}"
		s.upstreams = "{" + strings.Join(ups, ",") + "}"
	})
	p, _ := newTestPlugin()
	obs := collect(t, p, srv.params())
	// 6 zones over the cap, one refused as an item, 2 upstreams over it.
	mustValue(t, obs, "nginx.api_items_unreported", 9)
	mustItemValue(t, obs, "zone:z63", "nginx.zone_processing", 0)
	mustItemAbsent(t, obs, "zone:z64", "nginx.zone_processing")
	mustItemValue(t, obs, "upstream:u63", "nginx.upstream_peers", 1)
	mustItemAbsent(t, obs, "upstream:u64", "nginx.upstream_peers")
	if items := webchecktest.Items(obs); len(items) != 1+maxAPIZones+maxAPIUpstreams {
		t.Fatalf("%d items, want %d", len(items), 1+maxAPIZones+maxAPIUpstreams)
	}
	for _, o := range obs {
		if !checks.ReportableItem(o.Item) {
			t.Fatalf("unreportable item %q", o.Item)
		}
	}
}

func TestNginxAPIRejectsMalformedPages(t *testing.T) {
	const format = "unexpected response format"
	tests := []struct {
		name              string
		zones, upstreams  string
		zonesCode, upCode int
		reason            string
	}{
		{"html", "<html>nginx</html>", upstreamsPage(1, 1, 1), 200, 200, format},
		{"null", "null", upstreamsPage(1, 1, 1), 200, 200, format},
		{"array", `["nginx","processes"]`, upstreamsPage(1, 1, 1), 200, 200, format},
		{"negative", `{"a": {"requests": -1}}`, upstreamsPage(1, 1, 1), 200, 200, format},
		{"fraction", `{"a": {"requests": 1.5}}`, upstreamsPage(1, 1, 1), 200, 200, format},
		{"overflow", `{"a": {"requests": 18446744073709551616}}`, upstreamsPage(1, 1, 1), 200, 200, format},
		{"zone not an object", `{"a": "x"}`, upstreamsPage(1, 1, 1), 200, 200, format},
		{"trailing data", `{"a": {}} {}`, upstreamsPage(1, 1, 1), 200, 200, format},
		{"peers not a list", zonesPage(1, 1, 1, 1, 1), `{"u": {"peers": {}}}`, 200, 200, format},
		{"peer count not a number", zonesPage(1, 1, 1, 1, 1), `{"u": {"peers": [{"requests": "1"}]}}`, 200, 200, format},
		{"zones 404", `{"error":{"status":404}}`, upstreamsPage(1, 1, 1), 404, 200, "HTTP status 404"},
		{"upstreams 500", zonesPage(1, 1, 1, 1, 1), `{"error":{}}`, 200, 500, "HTTP status 500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newAPIServer(t)
			srv.set(func(s *apiServer) {
				s.zones, s.upstreams, s.zonesCode, s.upsCode = tt.zones, tt.upstreams, tt.zonesCode, tt.upCode
			})
			p, _ := newTestPlugin()
			obs := collect(t, p, srv.params())
			mustValue(t, obs, "nginx.up", 1)
			mustValue(t, obs, "nginx.api_up", 0)
			webchecktest.MustErr(t, obs, "nginx", "nginx.api_items_unreported", tt.reason)
			if items := webchecktest.Items(obs); len(items) != 1 {
				t.Fatalf("items %v on a failed API round with nothing known", items)
			}
		})
	}
}

// A failing API after a good round puts the reason on every item it knew,
// keeps their baselines, and drops nothing until the API answers again.
func TestNginxAPIDownMarksKnownItems(t *testing.T) {
	srv := newAPIServer(t)
	p, clk := newTestPlugin()
	collect(t, p, srv.params())

	clk.Advance(10 * time.Second)
	srv.set(func(s *apiServer) { s.upsCode = http.StatusInternalServerError })
	obs := collect(t, p, srv.params())
	mustValue(t, obs, "nginx.up", 1)
	mustValue(t, obs, "nginx.api_up", 0)
	for _, m := range zoneMetrics {
		mustItemErr(t, obs, "zone:site1", m, "HTTP status 500")
	}
	for _, m := range upstreamMetrics {
		mustItemErr(t, obs, "upstream:empty", m, "HTTP status 500")
	}

	clk.Advance(10 * time.Second)
	srv.set(func(s *apiServer) {
		s.upsCode = http.StatusOK
		s.zones = zonesPage(1200, 10, 2, 5000, 90000)
	})
	obs = collect(t, p, srv.params())
	mustValue(t, obs, "nginx.api_up", 1)
	mustItemValue(t, obs, "zone:site1", "nginx.zone_requests_per_s", 10) // 200 over 20 s
}

// stub_status and the API fail on their own: a stub_status page that is
// down leaves the API items alone, and the other way round.
func TestNginxStubStatusAndAPIFailApart(t *testing.T) {
	srv := newAPIServer(t)
	srv.set(func(s *apiServer) { s.stub = "<html>" })
	p, _ := newTestPlugin()
	obs := collect(t, p, srv.params())
	mustValue(t, obs, "nginx.up", 0)
	webchecktest.MustErr(t, obs, "nginx", "nginx.connections_active", "unexpected response format")
	mustValue(t, obs, "nginx.api_up", 1)
	mustItemValue(t, obs, "upstream:backend", "nginx.upstream_peers", 3)
}

// The API base may end in a slash and carry a query; both are kept as
// written for the two pages.
func TestNginxAPIURLJoin(t *testing.T) {
	srv := newAPIServer(t)
	p, _ := newTestPlugin()
	params := map[string]any{"url": srv.URL + "/nginx_status", "api_url": srv.URL + "/api/9/?x=1"}
	collect(t, p, params)
	got := srv.requestURIs()
	slices.Sort(got)
	want := []string{"/api/9/http/server_zones?x=1", "/api/9/http/upstreams?x=1", "/nginx_status"}
	if !slices.Equal(got, want) {
		t.Fatalf("requests %v, want %v", got, want)
	}
}

// The secret is sent to all three pages, and neither it nor the URL ever
// reaches a text, whatever the API pages answer.
func TestNginxAPINeverLeaks(t *testing.T) {
	srv := webchecktest.NewServer(t, "")
	body := fmt.Sprintf(`{"error":{"status":401,"text":"%s %s %s"}}`, plantUser, plantPass, srv.URL)
	for _, code := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusInternalServerError} {
		srv.Set(code, body)
		p, _ := newTestPlugin()
		params := map[string]any{"url": srv.URL + "/nginx_status", "api_url": srv.URL + "/api/9", "secret": "s"}
		obs, err := p.CollectWithSecret(t.Context(), params, map[string]string{"basic_auth_user": plantUser, "basic_auth_password": plantPass})
		noSecretIn(t, srv, obs, err)
		mustValue(t, obs, "nginx.api_up", 0)
	}
	for _, a := range srv.Auths() {
		if a == "" {
			t.Fatalf("a request went without the login: %q", srv.Auths())
		}
	}
	if n := len(srv.Auths()); n != 9 {
		t.Fatalf("%d requests, want 3 per round", n)
	}
}

func TestNginxAPICallerContextEnding(t *testing.T) {
	srv := newAPIServer(t)
	p, _ := newTestPlugin()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	obs, err := p.Collect(ctx, srv.params())
	if err == nil || obs != nil {
		t.Fatalf("Collect = %+v, %v; want nothing and the context's error", obs, err)
	}
	if len(p.zones) != 0 || len(p.upstreams) != 0 {
		t.Fatal("a cancelled round moved the API state")
	}
}
