package statuspage

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

type fakeSource struct {
	mu   sync.Mutex
	snap Snapshot
}

func (f *fakeSource) Snapshot() Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func tp(t time.Time) *time.Time { return &t }
func fp(v float64) *float64     { return &v }
func sp(s string) *string       { return &s }

var t0 = time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

// fullSnapshot fills every section, with markers a test can look for.
func fullSnapshot() Snapshot {
	return Snapshot{
		GeneratedAt: t0,
		Agent:       Agent{Version: "v1.2.3", OS: "linux", Arch: "amd64", StartedAt: t0.Add(-time.Hour)},
		Connection:  Connection{Manager: "manager.example:8444", Connected: true, ConnectedSince: tp(t0.Add(-time.Minute)), LastContact: tp(t0), LastHeartbeat: tp(t0)},
		Identity: Identity{AgentName: "web-1", Hostname: "web-1.local", OrgID: "org-id-1", OrgName: "Acme Corp", HostID: "host-id-1",
			HostName: "Web One", CertNotAfter: tp(t0.Add(60 * 24 * time.Hour)), RenewalDue: tp(t0.Add(30 * 24 * time.Hour))},
		Checks: []Check{
			{Key: "cpu", Plugin: "cpu", IntervalSeconds: 30, LastRun: tp(t0), Results: []Value{{Metric: "cpu.used_pct", Value: fp(12.5)}}},
			{Key: "local-db", Plugin: "postgres", Local: true, IntervalSeconds: 60, LastRun: tp(t0),
				Results: []Value{{Item: "main", Metric: "postgres.up", Error: "connection refused marker-err"}, {Metric: "postgres.role", Label: sp("primary-label")}}},
		},
		Facts:     Facts{SentAt: tp(t0), Values: map[string]string{"os": "linux", "kernel": "marker-kernel"}},
		Heartbeat: Heartbeat{SentAt: tp(t0), Version: "v1.2.3", ConfigVersion: "cfg-v-marker", SecretNames: []string{"pg_main"}, SecretsState: "ok", LocalChecks: []LocalCheck{{Key: "local-db", Plugin: "postgres", ParamsJSON: `{"secret":"pg_main"}`, IntervalSeconds: 60}}, LocalCheckProblems: []string{"line 9: marker-problem"}, ProbesSupported: true, ProbeConfigVersion: "probe-v-marker", StatusPage: "loopback"},
		Probes: Probes{Enabled: true, DenyCIDRs: []string{"10.0.0.0/8"}, Assigned: []Probe{{TargetHostID: "target-1", CheckKey: "web", Plugin: "http",
			Address: "203.0.113.7", IntervalSeconds: 60, LastRun: tp(t0), Results: []Value{{Metric: "http.status", Value: fp(200)}}}}},
		Abilities: Abilities{ConfigFile: "/etc/pulsix/agent.yaml", ConfigState: "ok", ScriptCommands: []string{"check_raid_marker"}, SecretNames: []string{"pg_main"}, LocalChecks: []string{"local-db"}},
		Buffer:    Buffer{CheckResults: 7, ProbeResults: 3, Oldest: tp(t0.Add(-5 * time.Minute))},
		Log:       []string{"level=INFO msg=marker-log-line"},
	}
}

func newTestHandler(snap Snapshot, token string, clock *fakeClock) *handler {
	return newHandler(&fakeSource{snap: snap}, token, nil, clock.Now, slog.New(slog.DiscardHandler))
}

func do(h http.Handler, method, target string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, body)
	r.Host = "127.0.0.1:8742" // a loopback page answers loopback Hosts only
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSecurityHeaders(t *testing.T) {
	h := newTestHandler(fullSnapshot(), "", &fakeClock{now: t0})
	for _, path := range []string{"/", "/status.json", "/nope"} {
		w := do(h, http.MethodGet, path, nil, nil)
		csp := w.Header().Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "frame-ancestors 'none'", "style-src 'nonce-", "base-uri 'none'", "form-action 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", path, csp, want)
			}
		}
		for k, want := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store", "X-Frame-Options": "DENY"} {
			if got := w.Header().Get(k); got != want {
				t.Errorf("%s: %s = %q, want %q", path, k, got, want)
			}
		}
	}
	// The nonce in the CSP is the one on the style element, and fresh per response.
	w1, w2 := do(h, http.MethodGet, "/", nil, nil), do(h, http.MethodGet, "/", nil, nil)
	n1 := strings.TrimSuffix(strings.Split(strings.Split(w1.Header().Get("Content-Security-Policy"), "'nonce-")[1], "'")[0], "'")
	if !strings.Contains(w1.Body.String(), `<style nonce="`+n1+`">`) || strings.Contains(w2.Header().Get("Content-Security-Policy"), n1) {
		t.Fatalf("nonce %q not used once", n1)
	}
	if strings.Contains(w1.Body.String(), "<script") {
		t.Fatal("the page carries a script")
	}
}

func TestMethodsOtherThanGetAre405(t *testing.T) {
	open := newTestHandler(fullSnapshot(), "", &fakeClock{now: t0})
	locked := newTestHandler(fullSnapshot(), testToken, &fakeClock{now: t0})
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		for _, path := range []string{"/", "/status.json", "/other"} {
			if w := do(open, m, path, nil, nil); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s = %d (Allow %q)", m, path, w.Code, w.Header().Get("Allow"))
			}
		}
		if m != http.MethodPost {
			for _, h := range []http.Handler{locked, open} {
				if w := do(h, m, "/login", nil, nil); w.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s /login = %d", m, w.Code)
				}
			}
		}
	}
	if w := do(open, http.MethodHead, "/", nil, nil); w.Code != http.StatusOK {
		t.Errorf("HEAD / = %d", w.Code)
	}
}

func TestPageShowsEverySection(t *testing.T) {
	h := newTestHandler(fullSnapshot(), "", &fakeClock{now: t0})
	body := do(h, http.MethodGet, "/", nil, nil).Body.String()
	for _, want := range []string{
		"v1.2.3", "linux/amd64", "manager.example:8444", "Acme Corp", "Web One", "web-1.local", "org-id-1",
		"cpu.used_pct", "12.5", "marker-err", "primary-label", "from agent.yaml", "from the manager",
		"marker-kernel", "cfg-v-marker", "probe-v-marker", "marker-problem", "{&#34;secret&#34;:&#34;pg_main&#34;}",
		"203.0.113.7", "10.0.0.0/8", "http.status", "check_raid_marker", "/etc/pulsix/agent.yaml", "pg_main",
		"never runs playbooks or remote commands", "marker-log-line", `<meta http-equiv="refresh" content="10">`,
		"Waiting for the manager", "<dd>7</dd>", "<dd>3</dd>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Without names from the manager, the IDs stand in and the page says so.
	snap := fullSnapshot()
	snap.Identity.OrgName, snap.Identity.HostName = "", ""
	body = do(newTestHandler(snap, "", &fakeClock{now: t0}), http.MethodGet, "/", nil, nil).Body.String()
	if !strings.Contains(body, "host-id-1") || !strings.Contains(body, "has not named the organization") {
		t.Error("IDs without names not shown")
	}
}

func TestPageEscapesUntrustedText(t *testing.T) {
	snap := fullSnapshot()
	evil := `<script>alert(1)</script><img src=x onerror=alert(2)>`
	snap.Checks[0].Results[0].Item = evil
	snap.Log = []string{evil}
	snap.Facts.Values["x"] = evil
	body := do(newTestHandler(snap, "", &fakeClock{now: t0}), http.MethodGet, "/", nil, nil).Body.String()
	if strings.Contains(body, "<script>") || strings.Contains(body, "<img") {
		t.Fatal("untrusted text reached the page unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("escaped text missing")
	}
}

func TestJSONMatchesSnapshot(t *testing.T) {
	snap := fullSnapshot()
	w := do(newTestHandler(snap, "", &fakeClock{now: t0}), http.MethodGet, "/status.json", nil, nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status.json = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var got Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, snap) {
		t.Fatalf("JSON round trip differs:\n got %+v\nwant %+v", got, snap)
	}
}

// TestHandlerRedactsOnlyTheLocalCheckParams: the page shows what was
// sent — results, facts and log lines arrive redacted already — and
// redacts once more only the local checks' params, which the heartbeat
// sends as written (controller ruling, fix round 1). Keys and metric
// names are shown as they are.
func TestHandlerRedactsOnlyTheLocalCheckParams(t *testing.T) {
	const secret = "hunter22-secret"
	snap := fullSnapshot()
	snap.Heartbeat.LocalChecks[0].ParamsJSON = `{"password":"` + secret + `"}`
	snap.Checks[0].Key = "key-" + secret
	snap.Checks[0].Results[0].Metric = "metric-" + secret
	h := newHandler(&fakeSource{snap: snap}, "", func(s string) string { return strings.ReplaceAll(s, secret, "[secret]") },
		time.Now, slog.New(slog.DiscardHandler))
	for _, path := range []string{"/", "/status.json"} {
		body := do(h, http.MethodGet, path, nil, nil).Body.String()
		shownAsSent := strings.Count(body, "key-"+secret) + strings.Count(body, "metric-"+secret)
		if strings.Count(body, secret) != shownAsSent || !strings.Contains(body, "[secret]") {
			t.Fatalf("%s: params not redacted", path)
		}
		if !strings.Contains(body, "key-"+secret) || !strings.Contains(body, "metric-"+secret) {
			t.Fatalf("%s redacted a key or a metric name", path)
		}
	}
}

// TestOpenPageRefusesForeignHosts: a page without a token answers only to
// a loopback Host, so a site that rebinds its name to 127.0.0.1 cannot read
// it from the visitor's browser (review 3.132, finding 2).
func TestOpenPageRefusesForeignHosts(t *testing.T) {
	open := newTestHandler(fullSnapshot(), "", &fakeClock{now: t0})
	for _, host := range []string{"localhost", "localhost:8742", "LOCALHOST:8742", "127.0.0.1", "127.0.0.1:8742", "127.8.9.1:1", "[::1]", "[::1]:8742"} {
		r := httptest.NewRequest(http.MethodGet, "/status.json", nil)
		r.Host = host
		w := httptest.NewRecorder()
		open.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("Host %q = %d", host, w.Code)
		}
	}
	for _, host := range []string{"evil.example", "evil.example:8742", "localhost.evil.example", "10.0.0.1:8742", "[::2]:8742", ""} {
		for _, path := range []string{"/", "/status.json", "/login"} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Host = host
			w := httptest.NewRecorder()
			open.ServeHTTP(w, r)
			if w.Code != http.StatusMisdirectedRequest || strings.Contains(w.Body.String(), "v1.2.3") {
				t.Errorf("Host %q %s = %d", host, path, w.Code)
			}
		}
	}
	// A page with a token answers any Host: the token is the guard.
	locked := newTestHandler(fullSnapshot(), testToken, &fakeClock{now: t0})
	r := httptest.NewRequest(http.MethodGet, "/status.json", nil)
	r.Host = "status.example:8742"
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	locked.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("token page, foreign Host = %d", w.Code)
	}
}

// TestCrossSiteLoginIsRefused: a login POST another site makes the
// visitor's browser send is refused before it counts as a failure, so it
// cannot lock the visitor out (review 3.132, finding 5).
func TestCrossSiteLoginIsRefused(t *testing.T) {
	h := newTestHandler(fullSnapshot(), testToken, &fakeClock{now: t0})
	post := func(hdr map[string]string) int {
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("token=wrong"))
		r.Host = "status.example:8742"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, hdr := range []map[string]string{
		{"Sec-Fetch-Site": "cross-site"},
		{"Origin": "https://evil.example"},
		{"Origin": "null"},
		{"Origin": "https://status.example:9999"},
	} {
		for range maxFailures + 1 {
			if code := post(hdr); code != http.StatusForbidden {
				t.Fatalf("%v = %d", hdr, code)
			}
		}
	}
	// Nothing was counted: a same-origin wrong token is a plain failure.
	if code := post(map[string]string{"Origin": "https://status.example:8742", "Sec-Fetch-Site": "same-origin"}); code != http.StatusUnauthorized {
		t.Fatalf("same-origin = %d", code)
	}
	if code := post(nil); code != http.StatusUnauthorized {
		t.Fatalf("no headers = %d", code)
	}
}

// TestFailuresCountPerIPv6Slash64: the addresses of one /64 share their
// failures, so rotating through it buys no tries (review 3.132, finding 6).
func TestFailuresCountPerIPv6Slash64(t *testing.T) {
	l := newFailureLimiter(func() time.Time { return t0 })
	for i := range maxFailures {
		l.fail(failureKey("2001:db8:1:2::" + strconv.Itoa(i+1)))
	}
	if !l.blocked(failureKey("2001:db8:1:2:ffff::9")) {
		t.Fatal("another address of the /64 is not blocked")
	}
	if l.blocked(failureKey("2001:db8:1:3::1")) || l.blocked(failureKey("192.0.2.1")) {
		t.Fatal("another /64 or an IPv4 address is blocked")
	}
	if failureKey("192.0.2.1") != "192.0.2.1" || failureKey("::ffff:192.0.2.1") != "192.0.2.1" {
		t.Fatal("IPv4 keys changed")
	}
}

func TestAuthRequiredWithToken(t *testing.T) {
	h := newTestHandler(fullSnapshot(), testToken, &fakeClock{now: t0})
	if w := do(h, http.MethodGet, "/", nil, nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("GET / without token = %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(h, http.MethodGet, "/status.json", nil, nil); w.Code != http.StatusUnauthorized || !strings.Contains(w.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("GET /status.json without token = %d", w.Code)
	}
	if w := do(h, http.MethodGet, "/status.json", nil, map[string]string{"Authorization": "Bearer wrong"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer = %d", w.Code)
	}
	for _, path := range []string{"/", "/status.json"} {
		if w := do(h, http.MethodGet, path, nil, map[string]string{"Authorization": "Bearer " + testToken}); w.Code != http.StatusOK {
			t.Fatalf("GET %s with bearer = %d", path, w.Code)
		}
	}
	if w := do(h, http.MethodGet, "/login", nil, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="token"`) {
		t.Fatalf("GET /login = %d", w.Code)
	}
	// An open page has no login.
	if w := do(newTestHandler(fullSnapshot(), "", &fakeClock{now: t0}), http.MethodGet, "/login", nil, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("open /login = %d", w.Code)
	}
}

func login(h http.Handler, token string, viaTLS bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"token": {token}}.Encode()))
	r.Host = "127.0.0.1:8742"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if viaTLS {
		r.TLS = &tls.ConnectionState{}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLoginCookieAttributes(t *testing.T) {
	clock := &fakeClock{now: t0}
	h := newTestHandler(fullSnapshot(), testToken, clock)
	if w := login(h, "wrong", false); w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 || !strings.Contains(w.Body.String(), "not right") {
		t.Fatalf("wrong login = %d", w.Code)
	}
	w := login(h, testToken, true)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login = %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if c.Name != cookieName || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || strings.Contains(c.Value, testToken) {
		t.Fatalf("cookie = %+v", c)
	}
	if c := login(h, testToken, false).Result().Cookies()[0]; c.Secure {
		t.Fatal("a plain HTTP login set a Secure cookie the browser would drop")
	}
	withCookie := map[string]string{"Cookie": cookieName + "=" + c.Value}
	if w := do(h, http.MethodGet, "/", nil, withCookie); w.Code != http.StatusOK {
		t.Fatalf("GET / with session = %d", w.Code)
	}
	// A forged or tampered session is refused.
	if w := do(h, http.MethodGet, "/", nil, map[string]string{"Cookie": cookieName + "=99999999999.00"}); w.Code != http.StatusSeeOther {
		t.Fatalf("forged session = %d", w.Code)
	}
	// A token change ends every session.
	other := newTestHandler(fullSnapshot(), testToken+"x", clock)
	if w := do(other, http.MethodGet, "/", nil, withCookie); w.Code != http.StatusSeeOther {
		t.Fatalf("session after token change = %d", w.Code)
	}
	// Sessions expire.
	clock.advance(sessionLifetime + time.Second)
	if w := do(h, http.MethodGet, "/", nil, withCookie); w.Code != http.StatusSeeOther {
		t.Fatalf("expired session = %d", w.Code)
	}
}

func TestFailedTokensAreRateLimited(t *testing.T) {
	clock := &fakeClock{now: t0}
	h := newTestHandler(fullSnapshot(), testToken, clock)
	for range maxFailures {
		if w := login(h, "wrong", false); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure = %d", w.Code)
		}
	}
	if w := login(h, testToken, false); w.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures, the right token = %d, want 429", maxFailures, w.Code)
	}
	if w := do(h, http.MethodGet, "/status.json", nil, map[string]string{"Authorization": "Bearer " + testToken}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("bearer while blocked = %d", w.Code)
	}
	clock.advance(failureWindow)
	if w := login(h, testToken, false); w.Code != http.StatusSeeOther {
		t.Fatalf("after the window = %d", w.Code)
	}
	// Bearer failures count too.
	for range maxFailures {
		do(h, http.MethodGet, "/status.json", nil, map[string]string{"Authorization": "Bearer nope"})
	}
	if w := do(h, http.MethodGet, "/status.json", nil, map[string]string{"Authorization": "Bearer " + testToken}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("after bearer failures = %d", w.Code)
	}
}

func TestFailureLimiterStaysBounded(t *testing.T) {
	l := newFailureLimiter(func() time.Time { return t0 })
	for i := range maxTracked + 50 {
		l.fail("ip-" + string(rune(i)))
	}
	if len(l.m) > maxTracked+1 {
		t.Fatalf("tracked %d addresses", len(l.m))
	}
	for range maxFailures {
		l.fail("rotating-new-address")
	}
	if !l.blocked("yet-another-address") {
		t.Fatal("addresses beyond the table share no limit")
	}
}

func TestLanguageFromAcceptLanguage(t *testing.T) {
	for header, want := range map[string]string{
		"":                           "en",
		"de":                         "de",
		"de-DE,de;q=0.9,en;q=0.8":    "de",
		"en-US,en;q=0.9,de;q=0.8":    "en",
		"fr-FR,de;q=0.5":             "de",
		"fr-FR":                      "en",
		"en;q=0.2,de;q=0.7":          "de",
		"de;q=0":                     "en",
		"garbage;;;,,de-AT;q=banana": "de",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Language", header)
		if got := textsFor(r).Lang; got != want {
			t.Errorf("Accept-Language %q = %s, want %s", header, got, want)
		}
	}
	h := newTestHandler(fullSnapshot(), "", &fakeClock{now: t0})
	body := do(h, http.MethodGet, "/", nil, map[string]string{"Accept-Language": "de"}).Body.String()
	if !strings.Contains(body, `lang="de"`) || !strings.Contains(body, "Was er erhebt") {
		t.Fatal("German page not served")
	}
}
