package phpfpm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

const (
	errFormatTx = "unexpected response format"
	plantUser   = webchecktest.PlantUser
	plantPass   = webchecktest.PlantPass
)

// page is a pool's ?json page.
func page(start, accepted, maxChildren, slow, idle, active int) string {
	return fmt.Sprintf(`{"pool":"www","process manager":"dynamic","start time":%d,"start since":10,"accepted conn":%d,`+
		`"listen queue":0,"max listen queue":0,"listen queue len":511,"idle processes":%d,"active processes":%d,`+
		`"total processes":%d,"max active processes":2,"max children reached":%d,"slow requests":%d}`,
		start, accepted, idle, active, idle+active, maxChildren, slow)
}

func newTestPlugin() (*plugin, *webchecktest.Clock) {
	c := webchecktest.NewClock()
	return &plugin{now: c.Now}, c
}

// entry is a pool entry on srv as an operator writes it.
func entry(s *webchecktest.Server) string { return s.URL + "/status" }

// urls is the urls parameter as it comes off the wire.
func urls(entries ...string) map[string]any {
	l := make([]any, len(entries))
	for i, e := range entries {
		l[i] = e
	}
	return map[string]any{"urls": l}
}

func findAt(obs []checks.Observation, item, metric string) (checks.Observation, bool) {
	for _, o := range obs {
		if o.Item == item && o.Metric == metric {
			return o, true
		}
	}
	return checks.Observation{}, false
}

func mustValueAt(t *testing.T, obs []checks.Observation, item, metric string, want float64) {
	t.Helper()
	o, ok := findAt(obs, item, metric)
	if !ok || o.Value == nil || *o.Value != want || o.Err != "" || o.Label != nil {
		t.Fatalf("%q on %q = %+v (found %v), want value %v; all: %+v", metric, item, o, ok, want, obs)
	}
}

func mustErrAt(t *testing.T, obs []checks.Observation, item, metric, reason string) {
	t.Helper()
	o, ok := findAt(obs, item, metric)
	if !ok || o.Value != nil || o.Label != nil || o.Err != "phpfpm: "+reason {
		t.Fatalf("%q on %q = %+v, want error %q", metric, item, o, "phpfpm: "+reason)
	}
}

func mustAbsentAt(t *testing.T, obs []checks.Observation, item, metric string) {
	t.Helper()
	if o, ok := findAt(obs, item, metric); ok {
		t.Fatalf("%q on %q reported: %+v", metric, item, o)
	}
}

// itemObs is the observations of one item.
func itemObs(obs []checks.Observation, item string) []checks.Observation {
	var out []checks.Observation
	for _, o := range obs {
		if o.Item == item {
			out = append(out, o)
		}
	}
	return out
}

// mustPoolDown asserts a pool's whole down set: up 0 and the reason on its
// eight other metrics, nothing else on that item.
func mustPoolDown(t *testing.T, obs []checks.Observation, item, reason string) {
	t.Helper()
	if len(poolMetrics) != 8 {
		t.Fatalf("%d pool metrics, want 8", len(poolMetrics))
	}
	mustValueAt(t, obs, item, "phpfpm.up", 0)
	if got := itemObs(obs, item); len(got) != 1+len(poolMetrics) {
		t.Fatalf("%d observations on %q, want %d: %+v", len(got), item, 1+len(poolMetrics), got)
	}
	for _, m := range poolMetrics {
		mustErrAt(t, obs, item, m, reason)
	}
}

// mustItems asserts which items a round reported, in order of first
// appearance: "" (pools_down) first, then the pools.
func mustItems(t *testing.T, obs []checks.Observation, want ...string) {
	t.Helper()
	var got []string
	for _, o := range obs {
		if !slices.Contains(got, o.Item) {
			got = append(got, o.Item)
		}
		if !checks.ReportableItem(o.Item) {
			t.Fatalf("item %q is not reportable", o.Item)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("items = %q, want %q", got, want)
	}
	if len(itemObs(obs, "")) != 1 {
		t.Fatalf("item \"\" carries %+v, want pools_down only", itemObs(obs, ""))
	}
}

// noLeak asserts the planted login appears nowhere and the server's
// host:port nowhere but in the items (an item is the entry as written).
func noLeak(t *testing.T, srv *webchecktest.Server, obs []checks.Observation, err error) {
	t.Helper()
	var all, items []string
	for _, o := range obs {
		all = append(all, o.Metric, o.Err)
		items = append(items, o.Item)
		if o.Label != nil {
			all = append(all, *o.Label)
		}
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
	for _, s := range []string{plantUser, plantPass} {
		if strings.Contains(strings.Join(items, "\n"), s) {
			t.Fatalf("%q leaked into the items %q", s, items)
		}
	}
}

func collect(t *testing.T, p *plugin, params map[string]any) []checks.Observation {
	t.Helper()
	obs, err := p.Collect(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	return obs
}

func TestFixturesReportEveryMetric(t *testing.T) {
	tests := []struct {
		file                                      string
		active, idle, total, busyPct, listenQueue float64
	}{
		{"php-7.4.json", 1, 3, 4, 25, 1},
		{"php-8.3.json", 1, 1, 2, 50, 0},
		{"php-8.3-full.json", 1, 1, 2, 50, 0},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			srv := webchecktest.NewServer(t, fixture(t, tt.file))
			p, _ := newTestPlugin()
			e := entry(srv)
			obs := collect(t, p, urls(e))
			mustItems(t, obs, "", e)
			mustValueAt(t, obs, "", "phpfpm.pools_down", 0)
			mustValueAt(t, obs, e, "phpfpm.up", 1)
			mustValueAt(t, obs, e, "phpfpm.processes_active", tt.active)
			mustValueAt(t, obs, e, "phpfpm.processes_idle", tt.idle)
			mustValueAt(t, obs, e, "phpfpm.processes_total", tt.total)
			mustValueAt(t, obs, e, "phpfpm.processes_busy_pct", tt.busyPct)
			mustValueAt(t, obs, e, "phpfpm.listen_queue", tt.listenQueue)
			for _, m := range []string{"phpfpm.requests_per_s", "phpfpm.max_children_reached_per_s", "phpfpm.slow_requests_per_s"} {
				mustAbsentAt(t, obs, e, m)
			}
			if len(obs) != 7 {
				t.Fatalf("first round: %d observations, want 7: %+v", len(obs), obs)
			}
		})
	}
}

func TestRatesAcrossRounds(t *testing.T) {
	srv := webchecktest.NewServer(t, page(100, 1000, 5, 10, 3, 1))
	p, clk := newTestPlugin()
	e := entry(srv)
	collect(t, p, urls(e))
	clk.Advance(10 * time.Second)
	srv.Set(200, page(100, 1500, 25, 40, 3, 1))
	obs := collect(t, p, urls(e))
	mustValueAt(t, obs, e, "phpfpm.requests_per_s", 50)
	mustValueAt(t, obs, e, "phpfpm.max_children_reached_per_s", 2)
	mustValueAt(t, obs, e, "phpfpm.slow_requests_per_s", 3)
	if len(obs) != 10 {
		t.Fatalf("%d observations, want 10: %+v", len(obs), obs)
	}
}

// The json query key is added when missing; the item stays the entry as
// written.
func TestJSONKeyAddedOnlyWhenMissing(t *testing.T) {
	srv := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
	p, _ := newTestPlugin()
	entries := []string{srv.URL + "/status", srv.URL + "/a?full", srv.URL + "/b?json&full", srv.URL + "/c?"}
	obs := collect(t, p, urls(entries...))
	mustItems(t, obs, append([]string{""}, entries...)...)
	got := srv.URIs()
	slices.Sort(got)
	want := []string{"/a?full&json", "/b?json&full", "/c?json", "/status?json"}
	if !slices.Equal(got, want) {
		t.Fatalf("requested %q, want %q", got, want)
	}
}

// Two pools are fetched concurrently: one that never answers costs only
// its own item, within the round's bound.
func TestOneHungPoolTimesOutAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 5 s request deadline")
	}
	hung := webchecktest.NewHangingServer(t)
	srv := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
	p, _ := newTestPlugin()
	hungEntry := hung.URL + "/status"
	start := time.Now()
	obs := collect(t, p, urls(hungEntry, entry(srv)))
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("round took %v", d)
	}
	mustItems(t, obs, "", hungEntry, entry(srv))
	mustPoolDown(t, obs, hungEntry, "timed out")
	mustValueAt(t, obs, entry(srv), "phpfpm.up", 1)
	mustValueAt(t, obs, "", "phpfpm.pools_down", 1)
}

func TestDuplicateEntriesReportedOnce(t *testing.T) {
	a := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
	b := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
	p, _ := newTestPlugin()
	obs := collect(t, p, urls(entry(b), entry(a), entry(b), entry(a)))
	mustItems(t, obs, "", entry(b), entry(a))
	if len(a.URIs()) != 1 || len(b.URIs()) != 1 {
		t.Fatalf("requests: a %v, b %v", a.URIs(), b.URIs())
	}
	if len(obs) != 1+2*6 {
		t.Fatalf("%d observations: %+v", len(obs), obs)
	}
}

// The default is asserted without a request: nothing here may dial
// 127.0.0.1:80.
func TestDefaultPoolURL(t *testing.T) {
	want := []string{"http://127.0.0.1/status"}
	for _, params := range []map[string]any{nil, {}, {"urls": nil}, {"urls": []any{}}, {"urls": []string{}}} {
		if got, err := poolURLs(params); err != nil || !slices.Equal(got, want) {
			t.Fatalf("poolURLs(%v) = %q, %v", params, got, err)
		}
	}
	got, err := poolURLs(map[string]any{"urls": []string{"http://a/s", "http://b/s", "http://a/s"}})
	if err != nil || !slices.Equal(got, []string{"http://a/s", "http://b/s"}) {
		t.Fatalf("poolURLs(list) = %q, %v", got, err)
	}
}

// An entry the item contract would refuse (it cannot pass the manager's
// validation) is left out rather than cost the batch; more entries than
// the descriptor allows are cut at its bound.
func TestPoolURLsKeepTheItemContract(t *testing.T) {
	got, err := poolURLs(map[string]any{"urls": []any{"", "http://a/\n", "http://a/" + strings.Repeat("x", checks.MaxItemLen), "http://a/ok"}})
	if err != nil || !slices.Equal(got, []string{"http://a/ok"}) {
		t.Fatalf("poolURLs = %q, %v", got, err)
	}
	many := make([]any, 40)
	for i := range many {
		many[i] = fmt.Sprintf("http://h/%d", i)
	}
	got, _ = poolURLs(map[string]any{"urls": many})
	if len(got) != maxPools || got[0] != "http://h/0" {
		t.Fatalf("poolURLs(40 entries) = %d entries", len(got))
	}
}

func TestMalformedURLsParam(t *testing.T) {
	p, _ := newTestPlugin()
	obs, err := p.Collect(t.Context(), map[string]any{"urls": "http://127.0.0.1/status"})
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 {
		t.Fatalf("observations = %+v", obs)
	}
	mustErrAt(t, obs, "", "phpfpm.pools_down", `param "urls": must be a list of strings`)
}

func TestPoolsDownEveryRound(t *testing.T) {
	a := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
	b := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
	p, _ := newTestPlugin()
	params := urls(entry(a), entry(b))

	mustValueAt(t, collect(t, p, params), "", "phpfpm.pools_down", 0)
	b.Set(500, "")
	obs := collect(t, p, params)
	mustValueAt(t, obs, "", "phpfpm.pools_down", 1)
	mustPoolDown(t, obs, entry(b), "HTTP status 500")
	mustValueAt(t, obs, entry(a), "phpfpm.up", 1)
	a.Set(200, "{}")
	obs = collect(t, p, params)
	mustValueAt(t, obs, "", "phpfpm.pools_down", 2)
	mustPoolDown(t, obs, entry(a), errFormatTx)
}

func TestBusyPctWithoutProcesses(t *testing.T) {
	srv := webchecktest.NewServer(t, page(1, 1, 0, 0, 0, 0))
	p, _ := newTestPlugin()
	obs := collect(t, p, urls(entry(srv)))
	mustValueAt(t, obs, entry(srv), "phpfpm.up", 1)
	mustValueAt(t, obs, entry(srv), "phpfpm.processes_total", 0)
	mustErrAt(t, obs, entry(srv), "phpfpm.processes_busy_pct", errFormatTx)
	mustValueAt(t, obs, "", "phpfpm.pools_down", 0)
}

func TestRatesReset(t *testing.T) {
	srv := webchecktest.NewServer(t, "")
	p, clk := newTestPlugin()
	e := entry(srv)
	step := func(body string) []checks.Observation {
		srv.Set(200, body)
		obs := collect(t, p, urls(e))
		clk.Advance(10 * time.Second)
		return obs
	}
	rates := []string{"phpfpm.requests_per_s", "phpfpm.max_children_reached_per_s", "phpfpm.slow_requests_per_s"}
	absent := func(obs []checks.Observation) {
		t.Helper()
		mustValueAt(t, obs, e, "phpfpm.up", 1)
		for _, m := range rates {
			mustAbsentAt(t, obs, e, m)
		}
	}
	step(page(100, 1000, 10, 10, 1, 1))
	// A restart: every counter falls.
	absent(step(page(200, 5, 0, 0, 1, 1)))
	obs := step(page(200, 105, 10, 20, 1, 1))
	mustValueAt(t, obs, e, "phpfpm.requests_per_s", 10)
	// A reload with a new start time whose counters happen to be higher:
	// no rate across it.
	absent(step(page(300, 5000, 50, 50, 1, 1)))
	mustValueAt(t, step(page(300, 5100, 50, 50, 1, 1)), e, "phpfpm.requests_per_s", 10)
	// One counter falls without a new start time: the pool's baselines
	// restart, all three.
	absent(step(page(300, 5200, 40, 50, 1, 1)))
	obs = step(page(300, 5300, 40, 50, 1, 1))
	mustValueAt(t, obs, e, "phpfpm.max_children_reached_per_s", 0)
	mustValueAt(t, obs, e, "phpfpm.slow_requests_per_s", 0)
}

func TestBaselineSurvivesAFailedRound(t *testing.T) {
	srv := webchecktest.NewServer(t, page(1, 1000, 0, 0, 1, 1))
	p, clk := newTestPlugin()
	e := entry(srv)
	collect(t, p, urls(e))
	clk.Advance(10 * time.Second)
	srv.Set(500, "")
	mustPoolDown(t, collect(t, p, urls(e)), e, "HTTP status 500")
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 1400, 0, 0, 1, 1))
	mustValueAt(t, collect(t, p, urls(e)), e, "phpfpm.requests_per_s", 20)
}

// Rate state is per URL string: a reorder keeps it, a removed entry loses
// it, and an entry added again starts over.
func TestRateStateFollowsTheList(t *testing.T) {
	a := webchecktest.NewServer(t, page(1, 1000, 0, 0, 1, 1))
	b := webchecktest.NewServer(t, page(1, 5000, 0, 0, 1, 1))
	p, clk := newTestPlugin()
	collect(t, p, urls(entry(a), entry(b)))
	clk.Advance(10 * time.Second)
	a.Set(200, page(1, 1100, 0, 0, 1, 1))
	b.Set(200, page(1, 5500, 0, 0, 1, 1))
	obs := collect(t, p, urls(entry(b), entry(a)))
	mustItems(t, obs, "", entry(b), entry(a))
	mustValueAt(t, obs, entry(a), "phpfpm.requests_per_s", 10)
	mustValueAt(t, obs, entry(b), "phpfpm.requests_per_s", 50)

	clk.Advance(10 * time.Second)
	collect(t, p, urls(entry(a)))
	if _, ok := p.pools[entry(b)]; ok || len(p.pools) != 1 {
		t.Fatalf("pools kept after b was removed: %v", p.pools)
	}
	clk.Advance(10 * time.Second)
	b.Set(200, page(1, 6000, 0, 0, 1, 1))
	obs = collect(t, p, urls(entry(a), entry(b)))
	mustAbsentAt(t, obs, entry(b), "phpfpm.requests_per_s")
}

func TestInstancesKeepSeparateBaselines(t *testing.T) {
	srv := webchecktest.NewServer(t, page(1, 1000, 0, 0, 1, 1))
	clk := webchecktest.NewClock()
	proto := &plugin{now: clk.Now}
	a, b := proto.NewInstance(), proto.NewInstance()
	if _, err := a.Collect(t.Context(), urls(entry(srv))); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 2000, 0, 0, 1, 1))
	obs, _ := b.Collect(t.Context(), urls(entry(srv)))
	mustAbsentAt(t, obs, entry(srv), "phpfpm.requests_per_s")
	obs, _ = a.Collect(t.Context(), urls(entry(srv)))
	mustValueAt(t, obs, entry(srv), "phpfpm.requests_per_s", 100)
}

func TestWrongJSONIsUnexpectedFormat(t *testing.T) {
	good := page(1, 1, 0, 0, 1, 1)
	tests := []struct{ name, body string }{
		{"string number", strings.Replace(good, `"accepted conn":1`, `"accepted conn":"1"`, 1)},
		{"negative", strings.Replace(good, `"listen queue":0`, `"listen queue":-3`, 1)},
		{"missing key", strings.Replace(good, `"slow requests"`, `"slow"`, 1)},
		{"text page", "pool: www\naccepted conn: 1\n"},
		{"over the page bound", `{"pool":"` + strings.Repeat("x", maxPage) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := webchecktest.NewServer(t, tt.body)
			p, _ := newTestPlugin()
			obs := collect(t, p, urls(entry(srv)))
			want := errFormatTx
			if len(tt.body) > maxPage {
				want = "response too large"
			}
			mustPoolDown(t, obs, entry(srv), want)
			mustValueAt(t, obs, "", "phpfpm.pools_down", 1)
		})
	}
}

func TestFetchFailuresReportThePoolDown(t *testing.T) {
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
			srv := webchecktest.NewServer(t, "")
			srv.Set(tt.status, "body "+plantUser+" "+plantPass)
			p, _ := newTestPlugin()
			obs, err := p.CollectWithSecret(t.Context(), urls(entry(srv)), map[string]string{"basic_auth_user": plantUser, "basic_auth_password": plantPass})
			if err != nil {
				t.Fatal(err)
			}
			mustPoolDown(t, obs, entry(srv), tt.want)
			noLeak(t, srv, obs, err)
		})
	}
}

func TestConnectionRefused(t *testing.T) {
	srv := webchecktest.NewServer(t, "")
	e := entry(srv)
	srv.Close()
	p, _ := newTestPlugin()
	obs := collect(t, p, urls(e))
	mustPoolDown(t, obs, e, "connection refused")
	noLeak(t, srv, obs, nil)
}

func TestBasicAuthSentExactlyWithASecret(t *testing.T) {
	a := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
	b := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
	p, _ := newTestPlugin()
	collect(t, p, urls(entry(a), entry(b)))
	if a.Auths()[0] != "" || b.Auths()[0] != "" {
		t.Fatalf("Authorization sent without a secret: %q %q", a.Auths(), b.Auths())
	}
	obs, err := p.CollectWithSecret(t.Context(), urls(entry(a), entry(b)), map[string]string{"basic_auth_user": plantUser, "basic_auth_password": plantPass})
	if err != nil {
		t.Fatal(err)
	}
	mustValueAt(t, obs, "", "phpfpm.pools_down", 0)
	for _, s := range []*webchecktest.Server{a, b} {
		r := &http.Request{Header: http.Header{"Authorization": {s.Auths()[1]}}}
		if u, pw, ok := r.BasicAuth(); !ok || u != plantUser || pw != plantPass {
			t.Fatalf("Authorization = %q, want basic auth of the planted login", s.Auths()[1])
		}
	}
}

// Secret fields that are refused cost every pool, with the field's text,
// and no request is sent.
func TestBadSecretFields(t *testing.T) {
	a := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
	b := webchecktest.NewServer(t, page(1, 1, 0, 0, 1, 1))
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
			obs, err := p.CollectWithSecret(t.Context(), urls(entry(a), entry(b)), tt.fields)
			if err != nil {
				t.Fatal(err)
			}
			mustItems(t, obs, "", entry(a), entry(b))
			mustValueAt(t, obs, "", "phpfpm.pools_down", 2)
			mustPoolDown(t, obs, entry(a), tt.want)
			mustPoolDown(t, obs, entry(b), tt.want)
			noLeak(t, a, obs, err)
		})
	}
	if len(a.URIs()) != 0 || len(b.URIs()) != 0 {
		t.Fatalf("a request was sent with bad secret fields")
	}
}

func TestCallerContextEnding(t *testing.T) {
	hung := webchecktest.NewHangingServer(t)
	srv := webchecktest.NewServer(t, page(1, 1000, 0, 0, 1, 1))
	p, clk := newTestPlugin()
	params := urls(entry(srv))
	collect(t, p, params)

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	// The answering pool's page moved on; the round is cancelled while the
	// other pool hangs, so nothing of it may be reported or kept.
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 9000, 0, 0, 1, 1))
	obs, err := p.Collect(ctx, urls(entry(srv), hung.URL+"/status"))
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("Collect = %+v, %v; want nil, context.Canceled", obs, err)
	}
	obs, err = p.Collect(ctx, params)
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("Collect with a dead context = %+v, %v", obs, err)
	}
	obs, err = p.Collect(ctx, map[string]any{"urls": "not a list"})
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("Collect with a dead context and a bad param = %+v, %v", obs, err)
	}
	obs, err = p.CollectWithSecret(ctx, params, map[string]string{})
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("Collect with a dead context and a bad secret = %+v, %v", obs, err)
	}
	// The baseline is still the first round's: 1000 → 1100 over 20 s.
	clk.Advance(10 * time.Second)
	srv.Set(200, page(1, 1100, 0, 0, 1, 1))
	mustValueAt(t, collect(t, p, params), entry(srv), "phpfpm.requests_per_s", 5)
}

func TestPrototypeAndInstances(t *testing.T) {
	proto := New()
	if proto.Name() != "phpfpm" {
		t.Fatalf("Name = %q", proto.Name())
	}
	sp, ok := proto.(checks.StatefulPlugin)
	if !ok {
		t.Fatal("not a StatefulPlugin")
	}
	if proto.(checks.SecretPlugin).SecretErrorMetric() != "phpfpm.pools_down" {
		t.Fatal("SecretErrorMetric is not phpfpm.pools_down")
	}
	a, b := sp.NewInstance(), sp.NewInstance()
	if a == b {
		t.Fatal("NewInstance returned a shared instance")
	}
	if a.(*plugin).now == nil {
		t.Fatal("instance has no clock")
	}
}
