package nginx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

const (
	plantUser = "plantuser-5e2"
	plantPass = "plantpass-8b4"
)

// page renders a stub_status page.
func page(active, accepts, handled, requests, reading, writing, waiting int) string {
	return fmt.Sprintf("Active connections: %d \nserver accepts handled requests\n %d %d %d \nReading: %d Writing: %d Waiting: %d \n",
		active, accepts, handled, requests, reading, writing, waiting)
}

// statusServer serves the page returned by body() with the given status.
type statusServer struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	body   string
	auth   []string
}

func newStatusServer(t *testing.T, body string) *statusServer {
	t.Helper()
	s := &statusServer{status: http.StatusOK, body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *statusServer) set(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body = status, body
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestPlugin() (*plugin, *clock) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	return &plugin{now: c.now}, c
}

func params(s *statusServer) map[string]any { return map[string]any{"url": s.URL + "/nginx_status"} }

func find(obs []checks.Observation, metric string) (checks.Observation, bool) {
	for _, o := range obs {
		if o.Metric == metric {
			if o.Item != "" {
				panic("item set: " + o.Item)
			}
			return o, true
		}
	}
	return checks.Observation{}, false
}

func mustValue(t *testing.T, obs []checks.Observation, metric string, want float64) {
	t.Helper()
	o, ok := find(obs, metric)
	if !ok {
		t.Fatalf("no observation %q in %+v", metric, obs)
	}
	if o.Value == nil || *o.Value != want || o.Err != "" {
		t.Fatalf("%q = %+v, want value %v", metric, o, want)
	}
}

func mustAbsent(t *testing.T, obs []checks.Observation, metric string) {
	t.Helper()
	if o, ok := find(obs, metric); ok {
		t.Fatalf("%q reported: %+v", metric, o)
	}
}

// mustDown asserts the whole down set: up 0 and the reason on the seven
// other metrics.
func mustDown(t *testing.T, obs []checks.Observation, reason string) {
	t.Helper()
	mustValue(t, obs, "nginx.up", 0)
	if len(obs) != 1+len(hostMetrics) || len(hostMetrics) != 7 {
		t.Fatalf("got %d observations, want 8: %+v", len(obs), obs)
	}
	for _, m := range hostMetrics {
		o, ok := find(obs, m)
		if !ok || o.Value != nil || o.Label != nil || o.Err != "nginx: "+reason {
			t.Fatalf("%q = %+v, want error %q", m, o, "nginx: "+reason)
		}
	}
}

func noSecretIn(t *testing.T, srv *statusServer, obs []checks.Observation, err error) {
	t.Helper()
	var all []string
	for _, o := range obs {
		all = append(all, o.Item, o.Metric, o.Err)
	}
	if err != nil {
		all = append(all, err.Error())
	}
	text := strings.Join(all, "\n")
	for _, s := range []string{plantUser, plantPass, strings.TrimPrefix(srv.URL, "http://")} {
		if strings.Contains(text, s) {
			t.Fatalf("%q leaked into %q", s, text)
		}
	}
}

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

	clk.t = clk.t.Add(10 * time.Second)
	srv.set(200, page(300, 16630948+100, 16630948+90, 31070465+500, 1, 2, 3))
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
	clk.t = clk.t.Add(10 * time.Second)
	srv.set(200, page(1, 10, 10, 20, 0, 1, 0)) // nginx restarted
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "nginx.up", 1)
	for _, m := range []string{"nginx.requests_per_s", "nginx.connections_accepted_per_s", "nginx.connections_dropped_per_s"} {
		mustAbsent(t, obs, m)
	}
	// The new baseline works on the next round.
	clk.t = clk.t.Add(10 * time.Second)
	srv.set(200, page(1, 30, 30, 120, 0, 1, 0))
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
	clk.t = clk.t.Add(10 * time.Second)
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
		{"handled above accepts", page(1, 5, 6, 10, 0, 1, 0)},
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

func TestToleratesExtraWhitespace(t *testing.T) {
	body := "\n  Active connections:   3\r\n server accepts handled requests \r\n\t4  4   9\r\n Reading: 0   Writing: 1  Waiting: 2\r\n\r\n"
	srv := newStatusServer(t, body)
	p, _ := newTestPlugin()
	obs, _ := p.Collect(t.Context(), params(srv))
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
			srv.set(tt.status, "body "+plantPass)
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
	obs, _ := p.Collect(t.Context(), params(srv))
	mustDown(t, obs, "response too large")
}

func TestBasicAuthSentExactlyWithASecret(t *testing.T) {
	srv := newStatusServer(t, page(1, 1, 1, 1, 0, 1, 0))
	p, _ := newTestPlugin()
	if _, err := p.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	if srv.auth[0] != "" {
		t.Fatalf("Authorization sent without a secret: %q", srv.auth[0])
	}
	obs, err := p.CollectWithSecret(t.Context(), params(srv), map[string]string{"basic_auth_user": plantUser, "basic_auth_password": plantPass})
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "nginx.up", 1)
	if !strings.HasPrefix(srv.auth[1], "Basic ") {
		t.Fatalf("Authorization = %q, want Basic", srv.auth[1])
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
	if len(srv.auth) != 0 {
		t.Fatalf("a request was sent with bad secret fields")
	}
}

func TestCallerContextEnding(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

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

func TestDefaultURLAndParams(t *testing.T) {
	if defaultURL != "http://127.0.0.1/nginx_status" {
		t.Fatalf("defaultURL = %q", defaultURL)
	}
	// An unreachable default must still read as one fixed failure text.
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), nil)
	if err != nil || len(obs) != 8 {
		t.Fatalf("Collect(nil) = %+v, %v", obs, err)
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
