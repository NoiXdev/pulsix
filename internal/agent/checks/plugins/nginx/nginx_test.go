package nginx

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

// page renders a stub_status page.
func page(active, accepts, handled, requests, reading, writing, waiting int) string {
	return fmt.Sprintf("Active connections: %d \nserver accepts handled requests\n %d %d %d \nReading: %d Writing: %d Waiting: %d \n",
		active, accepts, handled, requests, reading, writing, waiting)
}

func newTestPlugin() (*plugin, *webchecktest.Clock) {
	c := webchecktest.NewClock()
	return &plugin{now: c.Now}, c
}

func params(s *webchecktest.Server) map[string]any {
	return map[string]any{"url": s.URL + "/nginx_status"}
}

// mustDown asserts the whole down set: up 0 and the reason on the seven
// other metrics.
func mustDown(t *testing.T, obs []checks.Observation, reason string) {
	t.Helper()
	if len(hostMetrics) != 7 {
		t.Fatalf("%d host metrics, want 7", len(hostMetrics))
	}
	webchecktest.MustDown(t, obs, "nginx", "nginx.up", hostMetrics, reason)
}

var (
	newStatusServer = webchecktest.NewServer
	find            = webchecktest.Find
	mustValue       = webchecktest.MustValue
	mustAbsent      = webchecktest.MustAbsent
	noSecretIn      = webchecktest.NoSecretIn
)

const (
	plantUser = webchecktest.PlantUser
	plantPass = webchecktest.PlantPass
)

func TestCollectReportsEveryMetric(t *testing.T) {
	srv := newStatusServer(t, page(291, 16630948, 16630948, 31070465, 6, 179, 106))
	p, clk := newTestPlugin()

	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "nginx.up", 1)
	mustValue(t, obs, "nginx.connections_active", 291)
	mustValue(t, obs, "nginx.connections_reading", 6)
	mustValue(t, obs, "nginx.connections_writing", 179)
	mustValue(t, obs, "nginx.connections_waiting", 106)
	for _, m := range []string{"nginx.requests_per_s", "nginx.connections_accepted_per_s", "nginx.connections_dropped_per_s"} {
		mustAbsent(t, obs, m)
	}
	if len(obs) != 5 {
		t.Fatalf("first round: %d observations, want 5: %+v", len(obs), obs)
	}

	clk.Advance(10 * time.Second)
	srv.Set(200, page(300, 16630948+100, 16630948+90, 31070465+500, 1, 2, 3))
	obs, err = p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "nginx.up", 1)
	mustValue(t, obs, "nginx.connections_active", 300)
	mustValue(t, obs, "nginx.requests_per_s", 50)
	mustValue(t, obs, "nginx.connections_accepted_per_s", 10)
	mustValue(t, obs, "nginx.connections_dropped_per_s", 1)
}

func TestRatesResetWhenCountersDrop(t *testing.T) {
	srv := newStatusServer(t, page(1, 1000, 1000, 5000, 0, 1, 0))
	p, clk := newTestPlugin()
	if _, err := p.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 10, 10, 20, 0, 1, 0)) // nginx restarted
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "nginx.up", 1)
	for _, m := range []string{"nginx.requests_per_s", "nginx.connections_accepted_per_s", "nginx.connections_dropped_per_s"} {
		mustAbsent(t, obs, m)
	}
	// The new baseline works on the next round.
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 30, 30, 120, 0, 1, 0))
	obs, _ = p.Collect(t.Context(), params(srv))
	mustValue(t, obs, "nginx.requests_per_s", 10)
	mustValue(t, obs, "nginx.connections_accepted_per_s", 2)
	mustValue(t, obs, "nginx.connections_dropped_per_s", 0)
}

func TestChangedURLRestartsTheBaselines(t *testing.T) {
	a := newStatusServer(t, page(1, 100, 100, 100, 0, 1, 0))
	b := newStatusServer(t, page(1, 900, 900, 900, 0, 1, 0))
	p, clk := newTestPlugin()
	_, _ = p.Collect(t.Context(), params(a))
	clk.Advance(10 * time.Second)
	obs, _ := p.Collect(t.Context(), params(b))
	mustAbsent(t, obs, "nginx.requests_per_s")
}

func TestUnexpectedFormat(t *testing.T) {
	good := page(291, 10, 10, 20, 6, 179, 106)
	tests := []struct{ name, body string }{
		{"empty", ""},
		{"html", "<html>hi</html>"},
		{"missing reading line", "Active connections: 1 \nserver accepts handled requests\n 1 1 1 \n"},
		{"missing counters line", "Active connections: 1 \nserver accepts handled requests\nReading: 0 Writing: 1 Waiting: 0 \n"},
		{"missing active line", "server accepts handled requests\n 1 1 1 \nReading: 0 Writing: 1 Waiting: 0 \n"},
		{"non-number", strings.Replace(good, "291", "many", 1)},
		{"non-number counter", strings.Replace(good, " 10 10 20", " 10 x 20", 1)},
		{"negative", strings.Replace(good, "Reading: 6", "Reading: -6", 1)},
		{"overflow", strings.Replace(good, " 10 10 20", " 99999999999999999999999 10 20", 1)},
		{"two counters", strings.Replace(good, " 10 10 20", " 10 10", 1)},
		{"four counters", strings.Replace(good, " 10 10 20", " 10 10 20 30", 1)},
		{"wrong header line", strings.Replace(good, "server accepts handled requests", "server handled accepts requests", 1)},
		{"misspelled label", strings.Replace(good, "Waiting", "Waitin", 1)},
		{"trailing garbage", good + "extra\n"},
		{"too long", good + strings.Repeat(" ", 8<<10)},
		{"fifth line", good + "x\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newStatusServer(t, tt.body)
			p, _ := newTestPlugin()
			obs, err := p.Collect(t.Context(), params(srv))
			if err != nil {
				t.Fatal(err)
			}
			mustDown(t, obs, "unexpected response format")
		})
	}
}

// nginx reads its counters without a lock while workers keep counting, so a
// busy server can print handled above accepts: the page is valid and nothing
// was dropped.
func TestHandledAboveAcceptsIsNotAnError(t *testing.T) {
	srv := newStatusServer(t, page(1, 5, 6, 10, 0, 1, 0))
	p, clk := newTestPlugin()
	if _, err := p.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 25, 27, 50, 0, 1, 0))
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "nginx.up", 1)
	mustValue(t, obs, "nginx.connections_dropped_per_s", 0)
	mustValue(t, obs, "nginx.connections_accepted_per_s", 2)
}

// A failed round leaves the baselines alone: the next good round computes its
// rate against the last good one, over the whole elapsed time.
func TestBaselineSurvivesAFailedRound(t *testing.T) {
	srv := newStatusServer(t, page(1, 100, 100, 1000, 0, 1, 0))
	p, clk := newTestPlugin()
	if _, err := p.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	srv.Set(500, "")
	obs, _ := p.Collect(t.Context(), params(srv))
	mustDown(t, obs, "HTTP status 500")
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 140, 140, 1400, 0, 1, 0))
	obs, _ = p.Collect(t.Context(), params(srv))
	mustValue(t, obs, "nginx.requests_per_s", 20)
	mustValue(t, obs, "nginx.connections_accepted_per_s", 2)
}

func TestInstancesKeepSeparateBaselines(t *testing.T) {
	srv := newStatusServer(t, page(1, 100, 100, 1000, 0, 1, 0))
	clk := webchecktest.NewClock()
	proto := &plugin{now: clk.Now}
	a, b := proto.NewInstance(), proto.NewInstance()
	if _, err := a.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 200, 200, 2000, 0, 1, 0))
	obs, _ := b.Collect(t.Context(), params(srv))
	mustAbsent(t, obs, "nginx.requests_per_s") // b's first round
	obs, _ = a.Collect(t.Context(), params(srv))
	mustValue(t, obs, "nginx.requests_per_s", 100)
}

func TestToleratesExtraWhitespace(t *testing.T) {
	body := "\n  Active connections:   3\r\n server accepts handled requests \r\n\t4  4   9\r\n Reading: 0   Writing: 1  Waiting: 2\r\n\r\n"
	srv := newStatusServer(t, body)
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "nginx.up", 1)
	mustValue(t, obs, "nginx.connections_active", 3)
	mustValue(t, obs, "nginx.connections_waiting", 2)
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
			srv := newStatusServer(t, "body "+plantPass)
			srv.Set(tt.status, "body "+plantPass)
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
	srv := newStatusServer(t, "")
	url := srv.URL
	srv.Close()
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), map[string]any{"url": url + "/nginx_status"})
	if err != nil {
		t.Fatal(err)
	}
	mustDown(t, obs, "connection refused")
}

func TestOversizedBody(t *testing.T) {
	srv := newStatusServer(t, strings.Repeat("x", 4<<20+10))
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustDown(t, obs, "response too large")
}

func TestBasicAuthSentExactlyWithASecret(t *testing.T) {
	srv := newStatusServer(t, page(1, 1, 1, 1, 0, 1, 0))
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
	mustValue(t, obs, "nginx.up", 1)
	r := &http.Request{Header: http.Header{"Authorization": {srv.Auths()[1]}}}
	if u, pw, ok := r.BasicAuth(); !ok || u != plantUser || pw != plantPass {
		t.Fatalf("Authorization = %q, want basic auth of the planted login", srv.Auths()[1])
	}
}

func TestBadSecretFields(t *testing.T) {
	srv := newStatusServer(t, page(1, 1, 1, 1, 0, 1, 0))
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

func TestCallerContextEnding(t *testing.T) {
	srv := webchecktest.NewHangingServer(t)

	p, _ := newTestPlugin()
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
}

// The default is asserted without a request: nothing here may dial
// 127.0.0.1:80.
func TestDefaultURL(t *testing.T) {
	if got := statusURL(nil); got != "http://127.0.0.1/nginx_status" {
		t.Fatalf("statusURL(nil) = %q", got)
	}
	if got := statusURL(map[string]any{"url": ""}); got != defaultURL {
		t.Fatalf("statusURL(empty) = %q", got)
	}
	if got := statusURL(map[string]any{"url": "http://h/s"}); got != "http://h/s" {
		t.Fatalf("statusURL(given) = %q", got)
	}
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
	if proto.Name() != "nginx" {
		t.Fatalf("Name = %q", proto.Name())
	}
	sp, ok := proto.(checks.StatefulPlugin)
	if !ok {
		t.Fatal("not a StatefulPlugin")
	}
	if proto.(checks.SecretPlugin).SecretErrorMetric() != "nginx.up" {
		t.Fatal("SecretErrorMetric is not nginx.up")
	}
	a, b := sp.NewInstance(), sp.NewInstance()
	if a == b {
		t.Fatal("NewInstance returned a shared instance")
	}
	if a.(*plugin).now == nil {
		t.Fatal("instance has no clock")
	}
}

// TestDroppedRateIgnoresARacingRead: stub_status reads accepts before
// handled, so accepts - handled can fall by a few while nothing was undone.
// The fall must not look like a reset and give the next round a phantom rate.
func TestDroppedRateIgnoresARacingRead(t *testing.T) {
	srv := newStatusServer(t, page(1, 100, 95, 1000, 0, 1, 0)) // dropped 5
	p, clk := newTestPlugin()
	if _, err := p.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	for i, tt := range []struct {
		accepts, handled int
		want             float64
	}{
		{101, 98, 0},   // dropped 3: a racing read
		{102, 97, 0},   // dropped 5
		{103, 97, 0.1}, // dropped 6
	} {
		clk.Advance(10 * time.Second)
		srv.Set(200, page(1, tt.accepts, tt.handled, 1000+10*(i+1), 0, 1, 0))
		obs, err := p.Collect(t.Context(), params(srv))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "nginx.connections_dropped_per_s", tt.want)
	}
}
