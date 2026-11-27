package apache

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
	errExtended = "not reported by the server (ExtendedStatus Off)"
	errFormatTx = "unexpected response format"
)

// eventPage is an event MPM ?auto page (ExtendedStatus On).
func eventPage(accesses, kbytes, busy, idle int) string {
	return fmt.Sprintf(`localhost
ServerVersion: Apache/2.4.62 (Unix)
ServerMPM: event
Server Built: Jul 22 2024 16:12:55
CurrentTime: Friday, 13-Nov-2026 10:00:00 UTC
RestartTime: Friday, 13-Nov-2026 09:00:00 UTC
ParentServerConfigGeneration: 1
ParentServerMPMGeneration: 0
ServerUptimeSeconds: 3600
ServerUptime: 1 hour
Load1: 0.10
Total Accesses: %d
Total kBytes: %d
Total Duration: 12
CPUUser: 1.5
CPULoad: .0123
Uptime: 3600
ReqPerSec: 1.5
BytesPerSec: 2000
BytesPerReq: 1300
DurationPerReq: .5
BusyWorkers: %d
IdleWorkers: %d
Processes: 2
Stopping: 0
BusyWorkers: 999
ConnsTotal: 4
ConnsAsyncWriting: 0
ConnsAsyncKeepAlive: 1
ConnsAsyncClosing: 0
Scoreboard: ____W_____K_..........
`, accesses, kbytes, busy, idle)
}

// preforkPage is a prefork page without ExtendedStatus.
func preforkPage(busy, idle int) string {
	return fmt.Sprintf(`ServerVersion: Apache/2.4.62 (Unix)
ServerMPM: prefork
ParentServerConfigGeneration: 1
ServerUptimeSeconds: 100
BusyWorkers: %d
IdleWorkers: %d
Scoreboard: W_____
`, busy, idle)
}

func newTestPlugin() (*plugin, *webchecktest.Clock) {
	c := webchecktest.NewClock()
	return &plugin{now: c.Now}, c
}

func params(s *webchecktest.Server) map[string]any {
	return map[string]any{"url": s.URL + "/server-status?auto"}
}

func mustErr(t *testing.T, obs []checks.Observation, metric, reason string) {
	t.Helper()
	webchecktest.MustErr(t, obs, "apache", metric, reason)
}

// mustDown asserts the whole down set: up 0 and the reason on the seven
// other metrics.
func mustDown(t *testing.T, obs []checks.Observation, reason string) {
	t.Helper()
	if len(hostMetrics) != 7 {
		t.Fatalf("%d host metrics, want 7", len(hostMetrics))
	}
	webchecktest.MustDown(t, obs, "apache", "apache.up", hostMetrics, reason)
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
	srv := newStatusServer(t, eventPage(1000, 500, 3, 72))
	p, clk := newTestPlugin()

	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "apache.up", 1)
	mustValue(t, obs, "apache.workers_busy", 3)
	mustValue(t, obs, "apache.workers_idle", 72)
	mustValue(t, obs, "apache.workers_busy_pct", 4)
	mustValue(t, obs, "apache.workers_max", 22)
	mustValue(t, obs, "apache.workers_busy_max_pct", float64(3)/22*100)
	mustAbsent(t, obs, "apache.requests_per_s")
	mustAbsent(t, obs, "apache.bytes_per_s")
	if len(obs) != 6 {
		t.Fatalf("first round: %d observations, want 6: %+v", len(obs), obs)
	}

	clk.Advance(10 * time.Second)
	srv.Set(200, eventPage(1100, 520, 25, 25))
	obs, err = p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "apache.workers_busy_pct", 50)
	mustValue(t, obs, "apache.requests_per_s", 10)
	mustValue(t, obs, "apache.bytes_per_s", 2*1024) // 20 kB over 10 s
}

func TestPreforkWithoutExtendedStatus(t *testing.T) {
	srv := newStatusServer(t, preforkPage(1, 4))
	p, clk := newTestPlugin()
	for range 2 {
		obs, err := p.Collect(t.Context(), params(srv))
		if err != nil {
			t.Fatal(err)
		}
		mustValue(t, obs, "apache.up", 1)
		mustValue(t, obs, "apache.workers_busy", 1)
		mustValue(t, obs, "apache.workers_idle", 4)
		mustValue(t, obs, "apache.workers_busy_pct", 20)
		mustErr(t, obs, "apache.requests_per_s", errExtended)
		mustErr(t, obs, "apache.bytes_per_s", errExtended)
		mustValue(t, obs, "apache.workers_max", 6)
		if len(obs) != 8 {
			t.Fatalf("%d observations, want 8: %+v", len(obs), obs)
		}
		clk.Advance(10 * time.Second)
	}
}

func TestBusyPctFullAndEmpty(t *testing.T) {
	tests := []struct {
		name       string
		busy, idle int
		pct        float64
		err        bool
	}{
		{"no idle worker", 10, 0, 100, false},
		{"all idle", 0, 10, 0, false},
		{"no workers", 0, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newStatusServer(t, preforkPage(tt.busy, tt.idle))
			p, _ := newTestPlugin()
			obs, err := p.Collect(t.Context(), params(srv))
			if err != nil {
				t.Fatal(err)
			}
			mustValue(t, obs, "apache.up", 1)
			mustValue(t, obs, "apache.workers_busy", float64(tt.busy))
			mustValue(t, obs, "apache.workers_idle", float64(tt.idle))
			if tt.err {
				mustErr(t, obs, "apache.workers_busy_pct", errFormatTx)
			} else {
				mustValue(t, obs, "apache.workers_busy_pct", tt.pct)
			}
		})
	}
}

// Either counter falling restarts both baselines (a restart; a graceful one
// may keep or reset the counters).
func TestRatesResetWhenCountersDrop(t *testing.T) {
	for name, page := range map[string]string{
		"both":     eventPage(10, 20, 1, 5),
		"accesses": eventPage(10, 9500, 1, 5),
		"kbytes":   eventPage(5500, 20, 1, 5),
	} {
		t.Run(name, func(t *testing.T) {
			srv := newStatusServer(t, eventPage(5000, 9000, 1, 5))
			p, clk := newTestPlugin()
			if _, err := p.Collect(t.Context(), params(srv)); err != nil {
				t.Fatal(err)
			}
			clk.Advance(10 * time.Second)
			srv.Set(200, page)
			obs, err := p.Collect(t.Context(), params(srv))
			if err != nil {
				t.Fatal(err)
			}
			mustValue(t, obs, "apache.up", 1)
			mustAbsent(t, obs, "apache.requests_per_s")
			mustAbsent(t, obs, "apache.bytes_per_s")
		})
	}
}

func TestNewBaselineWorksAfterAReset(t *testing.T) {
	srv := newStatusServer(t, eventPage(5000, 9000, 1, 5))
	p, clk := newTestPlugin()
	for _, page := range []string{eventPage(5000, 9000, 1, 5), eventPage(10, 20, 1, 5), eventPage(110, 120, 1, 5)} {
		srv.Set(200, page)
		_, _ = p.Collect(t.Context(), params(srv))
		clk.Advance(10 * time.Second)
	}
	srv.Set(200, eventPage(210, 220, 1, 5))
	obs, _ := p.Collect(t.Context(), params(srv))
	mustValue(t, obs, "apache.requests_per_s", 10)
	mustValue(t, obs, "apache.bytes_per_s", 10*1024)
}

// A page without the counters (ExtendedStatus switched Off) restarts the
// baselines: the first On round after it has no rate.
func TestPageWithoutCountersResetsTheBaselines(t *testing.T) {
	srv := newStatusServer(t, eventPage(1000, 1000, 1, 5))
	p, clk := newTestPlugin()
	step := func(page string) []checks.Observation {
		srv.Set(200, page)
		obs, err := p.Collect(t.Context(), params(srv))
		if err != nil {
			t.Fatal(err)
		}
		clk.Advance(10 * time.Second)
		return obs
	}
	step(eventPage(1000, 1000, 1, 5))
	obs := step(eventPage(1100, 1100, 1, 5))
	mustValue(t, obs, "apache.requests_per_s", 10)
	obs = step(preforkPage(1, 4))
	mustErr(t, obs, "apache.requests_per_s", errExtended)
	obs = step(eventPage(1200, 1200, 1, 5))
	mustAbsent(t, obs, "apache.requests_per_s")
	mustAbsent(t, obs, "apache.bytes_per_s")
	obs = step(eventPage(1300, 1300, 1, 5))
	mustValue(t, obs, "apache.requests_per_s", 10)
}

func TestChangedURLRestartsTheBaselines(t *testing.T) {
	a := newStatusServer(t, eventPage(100, 100, 1, 5))
	b := newStatusServer(t, eventPage(900, 900, 1, 5))
	p, clk := newTestPlugin()
	_, _ = p.Collect(t.Context(), params(a))
	clk.Advance(10 * time.Second)
	obs, _ := p.Collect(t.Context(), params(b))
	mustAbsent(t, obs, "apache.requests_per_s")
}

func TestBaselineSurvivesAFailedRound(t *testing.T) {
	srv := newStatusServer(t, eventPage(1000, 1000, 1, 5))
	p, clk := newTestPlugin()
	if _, err := p.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	srv.Set(500, "")
	obs, _ := p.Collect(t.Context(), params(srv))
	mustDown(t, obs, "HTTP status 500")
	clk.Advance(10 * time.Second)
	srv.Set(200, eventPage(1400, 1200, 1, 5))
	obs, _ = p.Collect(t.Context(), params(srv))
	mustValue(t, obs, "apache.requests_per_s", 20)
	mustValue(t, obs, "apache.bytes_per_s", 10*1024)
}

func TestInstancesKeepSeparateBaselines(t *testing.T) {
	srv := newStatusServer(t, eventPage(1000, 1000, 1, 5))
	clk := webchecktest.NewClock()
	proto := &plugin{now: clk.Now}
	a, b := proto.NewInstance(), proto.NewInstance()
	if _, err := a.Collect(t.Context(), params(srv)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	srv.Set(200, eventPage(2000, 2000, 1, 5))
	obs, _ := b.Collect(t.Context(), params(srv))
	mustAbsent(t, obs, "apache.requests_per_s") // b's first round
	obs, _ = a.Collect(t.Context(), params(srv))
	mustValue(t, obs, "apache.requests_per_s", 100)
}

func TestUnexpectedFormat(t *testing.T) {
	good := eventPage(10, 20, 3, 72)
	tests := []struct{ name, body string }{
		{"empty", ""},
		{"html", "<html>hi</html>"},
		{"no busy", strings.ReplaceAll(good, "BusyWorkers", "Busy")},
		{"no idle", strings.ReplaceAll(good, "IdleWorkers", "Idle")},
		{"non-number busy", strings.Replace(good, "BusyWorkers: 3", "BusyWorkers: many", 1)},
		{"negative idle", strings.Replace(good, "IdleWorkers: 72", "IdleWorkers: -72", 1)},
		{"float busy", strings.Replace(good, "BusyWorkers: 3", "BusyWorkers: 3.5", 1)},
		{"nan idle", strings.Replace(good, "IdleWorkers: 72", "IdleWorkers: NaN", 1)},
		{"overflow", strings.Replace(good, "BusyWorkers: 3", "BusyWorkers: 99999999999999999999999", 1)},
		{"empty busy", strings.Replace(good, "BusyWorkers: 3", "BusyWorkers:", 1)},
		{"malformed accesses", strings.Replace(good, "Total Accesses: 10", "Total Accesses: x", 1)},
		{"negative accesses", strings.Replace(good, "Total Accesses: 10", "Total Accesses: -1", 1)},
		{"overflow kbytes", strings.Replace(good, "Total kBytes: 20", "Total kBytes: 99999999999999999999999", 1)},
		{"malformed kbytes", strings.Replace(good, "Total kBytes: 20", "Total kBytes: 2.5.1", 1)},
		{"huge page", good + strings.Repeat("Key: value\n", 100<<10)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newStatusServer(t, tt.body)
			p, _ := newTestPlugin()
			obs, err := p.Collect(t.Context(), params(srv))
			if err != nil {
				t.Fatal(err)
			}
			mustDown(t, obs, errFormatTx)
		})
	}
}

// A real page can carry a Scoreboard of tens of KB and CRLF line ends; both
// are fine.
func TestLargeScoreboardAndCRLF(t *testing.T) {
	body := strings.ReplaceAll(preforkPage(2, 8), "Scoreboard: W_____", "Scoreboard: "+strings.Repeat("_", 64<<10))
	body = strings.ReplaceAll(body, "\n", "\r\n")
	srv := newStatusServer(t, body)
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "apache.up", 1)
	mustValue(t, obs, "apache.workers_busy_pct", 20)
}

// A Scoreboard can be up to ServerLimit x ThreadLimit characters; a big
// configuration exceeds 256 KiB and must still read, as long as the page is
// within bounds.
func TestHugeScoreboardWithinThePageBound(t *testing.T) {
	body := eventPage(10, 20, 3, 72) + "Scoreboard: " + strings.Repeat("_", 600<<10) + "\n"
	srv := newStatusServer(t, body)
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), params(srv))
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "apache.up", 1)
	mustValue(t, obs, "apache.workers_busy", 3)
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
	srv := newStatusServer(t, "")
	url := srv.URL
	srv.Close()
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), map[string]any{"url": url + "/server-status"})
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
	srv := newStatusServer(t, preforkPage(1, 4))
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
	mustValue(t, obs, "apache.up", 1)
	r := &http.Request{Header: http.Header{"Authorization": {srv.Auths()[1]}}}
	if u, pw, ok := r.BasicAuth(); !ok || u != plantUser || pw != plantPass {
		t.Fatalf("Authorization = %q, want basic auth of the planted login", srv.Auths()[1])
	}
}

func TestBadSecretFields(t *testing.T) {
	srv := newStatusServer(t, preforkPage(1, 4))
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

// The agent asks for the machine-readable page: the query key auto is added
// to a user's URL that lacks it and left alone where it is.
func TestRequestedURLHasAuto(t *testing.T) {
	tests := []struct{ name, path, want string }{
		{"no query", "/server-status", "/server-status?auto"},
		{"other query", "/server-status?x=1", "/server-status?x=1&auto"},
		{"auto given", "/server-status?auto", "/server-status?auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newStatusServer(t, preforkPage(1, 4))
			p, _ := newTestPlugin()
			obs, err := p.Collect(t.Context(), map[string]any{"url": srv.URL + tt.path})
			if err != nil {
				t.Fatal(err)
			}
			mustValue(t, obs, "apache.up", 1)
			if len(srv.URIs()) != 1 || srv.URIs()[0] != tt.want {
				t.Fatalf("requested %v, want %q", srv.URIs(), tt.want)
			}
		})
	}
}

// The default is asserted without a request: nothing here may dial
// 127.0.0.1:80.
func TestDefaultURL(t *testing.T) {
	const want = "http://127.0.0.1/server-status?auto"
	if got := statusURL(nil); got != want {
		t.Fatalf("statusURL(nil) = %q", got)
	}
	if got := statusURL(map[string]any{"url": ""}); got != want {
		t.Fatalf("statusURL(empty) = %q", got)
	}
	if got := statusURL(map[string]any{"url": "http://h/s"}); got != "http://h/s?auto" {
		t.Fatalf("statusURL(no auto) = %q", got)
	}
	if got := statusURL(map[string]any{"url": "http://h/s?auto"}); got != "http://h/s?auto" {
		t.Fatalf("statusURL(auto) = %q", got)
	}
}

func TestCallerContextEnding(t *testing.T) {
	srv := webchecktest.NewHangingServer(t)

	srv2 := newStatusServer(t, eventPage(1000, 1000, 1, 5))
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
	// Baselines untouched.
	clk.Advance(10 * time.Second)
	srv2.Set(200, eventPage(1100, 1100, 1, 5))
	obs, _ = p.Collect(t.Context(), params(srv2))
	mustValue(t, obs, "apache.requests_per_s", 10)
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
	if proto.Name() != "apache" {
		t.Fatalf("Name = %q", proto.Name())
	}
	sp, ok := proto.(checks.StatefulPlugin)
	if !ok {
		t.Fatal("not a StatefulPlugin")
	}
	if proto.(checks.SecretPlugin).SecretErrorMetric() != "apache.up" {
		t.Fatal("SecretErrorMetric is not apache.up")
	}
	a, b := sp.NewInstance(), sp.NewInstance()
	if a == b {
		t.Fatal("NewInstance returned a shared instance")
	}
	if a.(*plugin).now == nil {
		t.Fatal("instance has no clock")
	}
}

// The limit-based percentage is busy over the scoreboard's slots, and the
// existing percentage keeps its meaning next to it (backlog 3.11b, R2).
// After a graceful restart that lowered MaxRequestWorkers from 150 to 50
// the board keeps 50 dead slots before the 50 live ones (measured in
// review): the length, 100, is reported as it is and the percentage reads
// low.
func TestWorkersAgainstTheLimit(t *testing.T) {
	page := func(busy, idle int, board string) string {
		return fmt.Sprintf("BusyWorkers: %d\nIdleWorkers: %d\n%s", busy, idle, board)
	}
	tests := []struct {
		name         string
		body         string
		max, pct     float64
		reason       string
		busyPctStill float64
	}{
		{"quarter", page(100, 100, "Scoreboard: "+strings.Repeat("_", 400)+"\n"), 400, 25, "", 50},
		{"full", page(150, 0, "Scoreboard: "+strings.Repeat("W", 150)+"\n"), 150, 100, "", 100},
		{"after a graceful restart lowered the limit", page(10, 40, "Scoreboard: "+strings.Repeat(".", 50)+strings.Repeat("W", 10)+strings.Repeat("_", 40)+"\n"), 100, 10, "", 20},
		{"no scoreboard", page(1, 4, ""), 0, 0, reasonNoScoreboard, 20},
		{"bad scoreboard", page(1, 4, "Scoreboard: <html>\n"), 0, 0, errFormatTx, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newStatusServer(t, tt.body)
			p, _ := newTestPlugin()
			obs, err := p.Collect(t.Context(), params(srv))
			if err != nil {
				t.Fatal(err)
			}
			mustValue(t, obs, "apache.up", 1)
			mustValue(t, obs, "apache.workers_busy_pct", tt.busyPctStill)
			if tt.reason != "" {
				mustErr(t, obs, "apache.workers_max", tt.reason)
				mustErr(t, obs, "apache.workers_busy_max_pct", tt.reason)
				return
			}
			mustValue(t, obs, "apache.workers_max", tt.max)
			mustValue(t, obs, "apache.workers_busy_max_pct", tt.pct)
		})
	}
}
