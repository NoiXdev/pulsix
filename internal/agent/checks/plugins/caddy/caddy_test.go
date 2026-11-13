package caddy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

const (
	errNotEnabled = "HTTP metrics are not enabled in Caddy"
	errFormatTx   = "unexpected response format"
)

// page is a metrics page of one server with one handler.
func page(inflight, requests, errs5xx int) string {
	return fmt.Sprintf(`# TYPE go_goroutines gauge
go_goroutines 12
caddy_http_requests_in_flight{handler="subroute",server="srv0"} %d
caddy_http_requests_total{handler="subroute",server="srv0"} %d
caddy_http_request_duration_seconds_count{code="200",handler="subroute",method="GET",server="srv0"} 1000
caddy_http_request_duration_seconds_count{code="500",handler="subroute",method="GET",server="srv0"} %d
`, inflight, requests, errs5xx)
}

// goOnly is a page of a Caddy without HTTP metrics: Go runtime and admin
// series only.
const goOnly = `# TYPE go_goroutines gauge
go_goroutines 12
caddy_admin_http_requests_total{code="200",handler="admin",method="GET",path="/metrics"} 3
promhttp_metric_handler_requests_total{code="200"} 4
`

func newTestPlugin() (*plugin, *webchecktest.Clock) {
	c := webchecktest.NewClock()
	return &plugin{now: c.Now}, c
}

func params(s *webchecktest.Server) map[string]any {
	return map[string]any{"url": s.URL + "/metrics"}
}

func mustErr(t *testing.T, obs []checks.Observation, metric, reason string) {
	t.Helper()
	webchecktest.MustErr(t, obs, "caddy", metric, reason)
}

// mustDown asserts the whole down set: up 0 and the reason on the three
// other metrics.
func mustDown(t *testing.T, obs []checks.Observation, reason string) {
	t.Helper()
	if len(hostMetrics) != 3 {
		t.Fatalf("%d host metrics, want 3", len(hostMetrics))
	}
	webchecktest.MustDown(t, obs, "caddy", "caddy.up", hostMetrics, reason)
}

var (
	newServer  = webchecktest.NewServer
	mustValue  = webchecktest.MustValue
	mustAbsent = webchecktest.MustAbsent
	noSecretIn = webchecktest.NoSecretIn
)

const (
	plantUser = webchecktest.PlantUser
	plantPass = webchecktest.PlantPass
)

func TestCollectReportsEveryMetric(t *testing.T) {
	srv := newServer(t, page(2, 1000, 10))
	p, clk := newTestPlugin()

	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "caddy.up", 1)
	mustValue(t, obs, "caddy.requests_in_flight", 2)
	mustAbsent(t, obs, "caddy.requests_per_s")
	mustAbsent(t, obs, "caddy.errors_5xx_per_s")
	if len(obs) != 2 {
		t.Fatalf("first round: %d observations, want 2: %+v", len(obs), obs)
	}

	clk.Advance(10 * time.Second)
	srv.Set(200, page(5, 1100, 30))
	obs, err = p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "caddy.requests_in_flight", 5)
	mustValue(t, obs, "caddy.requests_per_s", 10)
	mustValue(t, obs, "caddy.errors_5xx_per_s", 2)
}

func TestTwoServersFixtureAggregates(t *testing.T) {
	srv := newServer(t, fixture(t, "two-servers.txt"))
	p, clk := newTestPlugin()
	if _, err := p.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	srv.Set(200, strings.NewReplacer(
		`caddy_http_requests_total{handler="subroute",server="srv0"} 1000`, `caddy_http_requests_total{handler="subroute",server="srv0"} 1100`,
		`caddy_http_requests_total{handler="encode",server="srv1"} 200`, `caddy_http_requests_total{handler="encode",server="srv1"} 250`,
		`code="503",handler="file_server",method="GET",server="srv1"} 7`, `code="503",handler="file_server",method="GET",server="srv1"} 27`,
	).Replace(fixture(t, "two-servers.txt")))
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "caddy.requests_in_flight", 6)
	mustValue(t, obs, "caddy.requests_per_s", 15) // largest handler per server: 1100+250 against 1000+200
	mustValue(t, obs, "caddy.errors_5xx_per_s", 2)
}

func TestRealFixture(t *testing.T) {
	srv := newServer(t, fixture(t, "caddy-2.8.txt"))
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "caddy.up", 1)
	mustValue(t, obs, "caddy.requests_in_flight", 1)
}

// Without caddy_http_ series: up 1, the three others an error.
func TestMetricsNotEnabled(t *testing.T) {
	srv := newServer(t, goOnly)
	p, _ := newTestPlugin()
	for range 2 {
		obs, err := p.Collect(t.Context(), params(srv))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "caddy.up", 1)
		for _, m := range hostMetrics {
			mustErr(t, obs, m, errNotEnabled)
		}
		if len(obs) != 4 {
			t.Fatalf("%d observations, want 4: %+v", len(obs), obs)
		}
	}
}

// A missing family is the same error for its metric only.
func TestMissingFamily(t *testing.T) {
	srv := newServer(t, "caddy_http_requests_total{handler=\"h\",server=\"s\"} 5\n")
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "caddy.up", 1)
	mustErr(t, obs, "caddy.requests_in_flight", errNotEnabled)
	mustErr(t, obs, "caddy.errors_5xx_per_s", errNotEnabled)
	mustAbsent(t, obs, "caddy.requests_per_s") // first round
}

// With the duration family but no 5xx code the error rate is a real 0.
func TestNo5xxIsZero(t *testing.T) {
	srv := newServer(t, page(0, 10, 0))
	p, clk := newTestPlugin()
	_, _ = p.Collect(t.Context(), params(srv))
	clk.Advance(10 * time.Second)
	obs, _ := p.Collect(t.Context(), params(srv))
	mustValue(t, obs, "caddy.errors_5xx_per_s", 0)
	mustValue(t, obs, "caddy.requests_per_s", 0)
}

func TestRatesResetWhenACounterDrops(t *testing.T) {
	srv := newServer(t, page(1, 5000, 100))
	p, clk := newTestPlugin()
	step := func(body string) []checks.Observation {
		srv.Set(200, body)
		obs, err := p.Collect(t.Context(), params(srv))
		if err != nil {
			t.Fatal(err)
		}
		clk.Advance(10 * time.Second)
		return obs
	}
	step(page(1, 5000, 100))
	// A restart: both counters fall.
	obs := step(page(1, 10, 1))
	mustValue(t, obs, "caddy.up", 1)
	mustAbsent(t, obs, "caddy.requests_per_s")
	mustAbsent(t, obs, "caddy.errors_5xx_per_s")
	obs = step(page(1, 110, 11))
	mustValue(t, obs, "caddy.requests_per_s", 10)
	mustValue(t, obs, "caddy.errors_5xx_per_s", 1)
	// Only the 5xx counter falls: only its rate restarts.
	obs = step(page(1, 210, 3))
	mustValue(t, obs, "caddy.requests_per_s", 10)
	mustAbsent(t, obs, "caddy.errors_5xx_per_s")
}

// A server disappearing (a reload) makes the aggregate fall: no negative
// or huge rate.
func TestAggregateFallResets(t *testing.T) {
	two := `caddy_http_requests_total{handler="a",server="s0"} 100
caddy_http_requests_total{handler="a",server="s1"} 100
caddy_http_requests_in_flight{handler="a",server="s0"} 0
caddy_http_request_duration_seconds_count{code="200",handler="a",server="s0"} 1
`
	one := strings.Replace(two, "caddy_http_requests_total{handler=\"a\",server=\"s1\"} 100\n", "", 1)
	srv := newServer(t, two)
	p, clk := newTestPlugin()
	_, _ = p.Collect(t.Context(), params(srv))
	clk.Advance(10 * time.Second)
	srv.Set(200, one)
	obs, _ := p.Collect(t.Context(), params(srv))
	mustAbsent(t, obs, "caddy.requests_per_s")
}

func TestChangedURLRestartsTheBaselines(t *testing.T) {
	a := newServer(t, page(1, 100, 1))
	b := newServer(t, page(1, 900, 9))
	p, clk := newTestPlugin()
	_, _ = p.Collect(t.Context(), params(a))
	clk.Advance(10 * time.Second)
	obs, _ := p.Collect(t.Context(), params(b))
	mustAbsent(t, obs, "caddy.requests_per_s")
	mustAbsent(t, obs, "caddy.errors_5xx_per_s")
}

func TestBaselineSurvivesAFailedRound(t *testing.T) {
	srv := newServer(t, page(1, 1000, 10))
	p, clk := newTestPlugin()
	if _, err := p.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	srv.Set(500, "")
	obs, _ := p.Collect(t.Context(), params(srv))
	mustDown(t, obs, "HTTP status 500")
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 1400, 50))
	obs, _ = p.Collect(t.Context(), params(srv))
	mustValue(t, obs, "caddy.requests_per_s", 20)
	mustValue(t, obs, "caddy.errors_5xx_per_s", 2)
}

func TestInstancesKeepSeparateBaselines(t *testing.T) {
	srv := newServer(t, page(1, 1000, 10))
	clk := webchecktest.NewClock()
	proto := &plugin{now: clk.Now}
	a, b := proto.NewInstance(), proto.NewInstance()
	if _, err := a.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 2000, 10))
	obs, _ := b.Collect(t.Context(), params(srv))
	mustAbsent(t, obs, "caddy.requests_per_s")
	obs, _ = a.Collect(t.Context(), params(srv))
	mustValue(t, obs, "caddy.requests_per_s", 100)
}

func TestUnexpectedFormat(t *testing.T) {
	tests := []struct{ name, body string }{
		{"empty", ""},
		{"html", "<html>hi</html>\n"},
		{"json", `{"requests": 5}`},
		{"bad value", "caddy_http_requests_total{server=\"a\"} many\n"},
		{"plain text", "Welcome to Caddy\n"},
		{"long relevant line", "go_goroutines 1\ncaddy_http_requests_total{server=\"" + strings.Repeat("x", 70<<10) + "\"} 1\n"},
		{"huge page of one line", strings.Repeat("a", 1<<20)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(t, tt.body)
			p, _ := newTestPlugin()
			obs, err := p.Collect(t.Context(), params(srv))
			if err != nil {
				t.Fatal(err)
			}
			mustDown(t, obs, errFormatTx)
		})
	}
}

func TestFetchFailuresReportDown(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{401, "authentication failed (HTTP 401)"},
		{403, "permission denied (HTTP 403)"},
		{404, "HTTP status 404"},
		{500, "HTTP status 500"},
		{301, "HTTP status 301"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			srv := newServer(t, "")
			srv.Set(tt.status, "body "+plantUser+" "+plantPass)
			p, _ := newTestPlugin()
			obs, err := p.CollectWithSecret(t.Context(), params(srv), map[string]string{"basic_auth_user": plantUser, "basic_auth_password": plantPass})
			if err != nil {
				t.Fatal(err)
			}
			mustDown(t, obs, tt.want)
			noSecretIn(t, srv, obs, err)
		})
	}
}

func TestConnectionRefused(t *testing.T) {
	srv := newServer(t, "")
	url := srv.URL
	srv.Close()
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), map[string]any{"url": url + "/metrics"})
	if err != nil {
		t.Fatal(err)
	}
	mustDown(t, obs, "connection refused")
}

func TestOversizedBody(t *testing.T) {
	srv := newServer(t, strings.Repeat("x", 4<<20+10))
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustDown(t, obs, "response too large")
}

func TestBasicAuthSentExactlyWithASecret(t *testing.T) {
	srv := newServer(t, page(1, 1, 0))
	p, _ := newTestPlugin()
	if _, err := p.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	if srv.Auths()[0] != "" {
		t.Fatalf("Authorization sent without a secret: %q", srv.Auths()[0])
	}
	obs, err := p.CollectWithSecret(t.Context(), params(srv), map[string]string{"basic_auth_user": plantUser, "basic_auth_password": plantPass})
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "caddy.up", 1)
	r := &http.Request{Header: http.Header{"Authorization": {srv.Auths()[1]}}}
	if u, pw, ok := r.BasicAuth(); !ok || u != plantUser || pw != plantPass {
		t.Fatalf("Authorization = %q, want basic auth of the planted login", srv.Auths()[1])
	}
}

func TestBadSecretFields(t *testing.T) {
	srv := newServer(t, page(1, 1, 0))
	tests := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"no user", map[string]string{"basic_auth_password": plantPass}, "secret field `basic_auth_user` is required"},
		{"unknown field", map[string]string{"basic_auth_user": plantUser, "password": plantPass}, "secret field `password` is not known"},
		{"empty", map[string]string{}, "secret field `basic_auth_user` is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := newTestPlugin()
			obs, err := p.CollectWithSecret(t.Context(), params(srv), tt.fields)
			if err != nil {
				t.Fatal(err)
			}
			mustDown(t, obs, tt.want)
			noSecretIn(t, srv, obs, err)
		})
	}
	if len(srv.Auths()) != 0 {
		t.Fatalf("a request was sent with bad secret fields")
	}
}

// The default is asserted without a request: nothing here may dial
// 127.0.0.1:2019.
func TestDefaultURL(t *testing.T) {
	const want = "http://127.0.0.1:2019/metrics"
	if got := statusURL(nil); got != want {
		t.Fatalf("statusURL(nil) = %q", got)
	}
	if got := statusURL(map[string]any{"url": ""}); got != want {
		t.Fatalf("statusURL(empty) = %q", got)
	}
	if got := statusURL(map[string]any{"url": "http://h/m"}); got != "http://h/m" {
		t.Fatalf("statusURL(given) = %q", got)
	}
}

func TestCallerContextEnding(t *testing.T) {
	srv := webchecktest.NewHangingServer(t)
	srv2 := newServer(t, page(1, 1000, 10))
	p, clk := newTestPlugin()
	if _, err := p.Collect(t.Context(), params(srv2)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	obs, err := p.Collect(ctx, map[string]any{"url": srv.URL})
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("Collect = %+v, %v; want nil, context.Canceled", obs, err)
	}
	obs, err = p.Collect(ctx, map[string]any{"url": srv.URL})
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("Collect with a dead context = %+v, %v", obs, err)
	}
	clk.Advance(10 * time.Second)
	srv2.Set(200, page(1, 1100, 10))
	obs, _ = p.Collect(t.Context(), params(srv2))
	mustValue(t, obs, "caddy.requests_per_s", 10)
}

func TestHungServerTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 5 s request deadline")
	}
	srv := webchecktest.NewHangingServer(t)
	p, _ := newTestPlugin()
	start := time.Now()
	obs, err := p.Collect(t.Context(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	mustDown(t, obs, "timed out")
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestPrototypeAndInstances(t *testing.T) {
	proto := New()
	if proto.Name() != "caddy" {
		t.Fatalf("Name = %q", proto.Name())
	}
	sp, ok := proto.(checks.StatefulPlugin)
	if !ok {
		t.Fatal("not a StatefulPlugin")
	}
	if proto.(checks.SecretPlugin).SecretErrorMetric() != "caddy.up" {
		t.Fatal("SecretErrorMetric is not caddy.up")
	}
	a, b := sp.NewInstance(), sp.NewInstance()
	if a == b {
		t.Fatal("NewInstance returned a shared instance")
	}
	if a.(*plugin).now == nil {
		t.Fatal("instance has no clock")
	}
}
