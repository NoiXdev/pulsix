package http_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// received is what a test server received.
type received struct {
	method string
	header http.Header
	body   string
	path   string
}

// recorder serves 200 "ok" and reports every request it receives.
func recorder(t *testing.T) (*httptest.Server, chan received) {
	t.Helper()
	got := make(chan received, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- received{method: r.Method, header: r.Header.Clone(), body: string(b), path: r.URL.Path}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// writer is a loopback guard that allows state-changing requests.
func writer() *probe.Guard { return probetest.LoopbackGuard().WithWrites(true) }

// next is the next request the server received, failing the test when
// none arrives within five seconds.
func next(t *testing.T, got chan received) received {
	t.Helper()
	select {
	case r := <-got:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the server received no request")
	}
	return received{}
}

// TestProbeSendsMethodHeadersAndBody: the configured method, headers,
// body and content type reach the server as written (backlog 3.84).
func TestProbeSendsMethodHeadersAndBody(t *testing.T) {
	srv, got := recorder(t)
	cases := []struct {
		name        string
		params      map[string]any
		method      string
		body        string
		contentType string
	}{
		{"POST with the default type", map[string]any{"method": "POST", "body": `{"ping":1}`}, "POST", `{"ping":1}`, "application/json"},
		{"PUT with a type", map[string]any{"method": "PUT", "body": "a=1&b=2", "content_type": "application/x-www-form-urlencoded"}, "PUT", "a=1&b=2", "application/x-www-form-urlencoded"},
		{"PATCH", map[string]any{"method": "PATCH", "body": "line 1\r\nline 2"}, "PATCH", "line 1\r\nline 2", "application/json"},
		{"POST without a body", map[string]any{"method": "POST"}, "POST", "", ""},
		{"DELETE", map[string]any{"method": "DELETE"}, "DELETE", "", ""},
		{"OPTIONS", map[string]any{"method": "OPTIONS"}, "OPTIONS", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			params := httpParams(srv.URL, c.params)
			params["headers"] = []any{"X-Api-Version: 2", "accept:  application/json ", "X-Empty:"}
			obs := run(context.Background(), writer(), params, "")
			probetest.WantUp(t, obs, "http")
			s := next(t, got)
			if s.method != c.method || s.body != c.body {
				t.Fatalf("server saw %s %q, want %s %q", s.method, s.body, c.method, c.body)
			}
			if ct := s.header.Get("Content-Type"); ct != c.contentType {
				t.Fatalf("Content-Type = %q, want %q", ct, c.contentType)
			}
			if s.header.Get("X-Api-Version") != "2" || s.header.Get("Accept") != "application/json" {
				t.Fatalf("headers = %v", s.header)
			}
			if v, ok := s.header["X-Empty"]; !ok || len(v) != 1 || v[0] != "" {
				t.Fatalf("X-Empty = %v, want one empty value", v)
			}
			if ua := s.header.Get("User-Agent"); ua != "Pulsix-Probe/1" {
				t.Fatalf("User-Agent = %q", ua)
			}
		})
	}
}

// TestSecretHeaderReplacesPlainHeader: the secret's header is applied
// last, so a plain header of the same name never goes out with it.
func TestSecretHeaderReplacesPlainHeader(t *testing.T) {
	srv, got := recorder(t)
	params := httpParams(srv.URL, map[string]any{"secret": "api", "headers": []any{"X-Tenant: plain", "X-Other: 1"}})
	obs := runWith(context.Background(), probetest.LoopbackGuard(), params, "", map[string]string{"header": "X-Tenant", "value": "s3cret"})
	probetest.WantUp(t, obs, "http")
	s := next(t, got)
	if v := s.header.Values("X-Tenant"); len(v) != 1 || v[0] != "s3cret" {
		t.Fatalf("X-Tenant = %v, want only the secret's value", v)
	}
	if s.header.Get("X-Other") != "1" {
		t.Fatalf("headers = %v", s.header)
	}
}

// TestRedirectBodyFollowsBrowserRules: a 307 or 308 resends the method
// and the body; a 301, 302 or 303 turns a POST into a GET without the
// body and its Content-Type; plain headers go along on the same origin.
func TestRedirectBodyFollowsBrowserRules(t *testing.T) {
	got := make(chan received, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- received{method: r.Method, header: r.Header.Clone(), body: string(b), path: r.URL.Path}
		if code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/")); err == nil {
			http.Redirect(w, r, "/end", code)
		}
	}))
	defer srv.Close()
	for _, c := range []struct {
		code             int
		method, body, ct string
	}{
		{http.StatusMovedPermanently, "GET", "", ""},
		{http.StatusFound, "GET", "", ""},
		{http.StatusSeeOther, "GET", "", ""},
		{http.StatusTemporaryRedirect, "POST", "payload", "text/plain"},
		{http.StatusPermanentRedirect, "POST", "payload", "text/plain"},
	} {
		params := httpParams(srv.URL+"/"+strconv.Itoa(c.code), map[string]any{
			"method": "POST", "body": "payload", "content_type": "text/plain", "headers": []any{"X-Trace: t1"},
		})
		obs := run(context.Background(), writer(), params, "")
		probetest.WantUp(t, obs, "http")
		first, second := next(t, got), next(t, got)
		if first.method != "POST" || first.body != "payload" {
			t.Fatalf("%d: first hop %s %q", c.code, first.method, first.body)
		}
		if second.path != "/end" || second.method != c.method || second.body != c.body {
			t.Fatalf("%d: second hop %s %s %q, want %s %q", c.code, second.path, second.method, second.body, c.method, c.body)
		}
		if ct := second.header.Get("Content-Type"); ct != c.ct {
			t.Fatalf("%d: second hop Content-Type %q, want %q", c.code, ct, c.ct)
		}
		if second.header.Get("X-Trace") != "t1" {
			t.Fatalf("%d: plain header lost on a same-origin redirect: %v", c.code, second.header)
		}
	}
}

// TestCrossOriginRedirectDropsConfiguredHeaders: once a redirect leaves
// the first request's origin, neither the secret's header nor any plain
// header goes along (fix round 1, M1), also not one named like the
// secret's.
func TestCrossOriginRedirectDropsConfiguredHeaders(t *testing.T) {
	other, got := recorder(t)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/landed", http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	params := httpParams(first.URL, map[string]any{"secret": "api", "headers": []any{"X-Tenant: a", "X-Trace-Id: s"}})
	obs := runWith(context.Background(), probetest.LoopbackGuard(), params, "", map[string]string{"header": "X-Tenant", "value": "from-secret"})
	probetest.WantUp(t, obs, "http")
	s := next(t, got)
	if s.path != "/landed" {
		t.Fatalf("path = %s", s.path)
	}
	for _, h := range []string{"X-Tenant", "X-Trace-Id"} {
		if v := s.header.Values(h); len(v) != 0 {
			t.Fatalf("%s = %v went to another origin", h, v)
		}
	}
	if s.header.Get("User-Agent") != "Pulsix-Probe/1" {
		t.Fatalf("headers = %v, want the probe's own kept", s.header)
	}
}

// TestWritesNeedTheOperatorsSwitch: POST, PUT, PATCH, DELETE or a body
// go out only through a guard that allows writes
// (PULSIX_PROBE_WRITE_METHODS); otherwise the attempt fails with that
// reason and nothing is sent, never a plain GET. GET, HEAD and OPTIONS
// with headers need no switch.
func TestWritesNeedTheOperatorsSwitch(t *testing.T) {
	srv, got := recorder(t)
	off := probetest.LoopbackGuard()
	for _, extra := range []map[string]any{
		{"method": "POST"}, {"method": "PUT", "body": "x"}, {"method": "PATCH", "body": "x"}, {"method": "DELETE"},
	} {
		obs := run(context.Background(), off, httpParams(srv.URL, extra), "")
		if o := probetest.ByMetric(t, obs)["http.up"]; o.Err != "state-changing requests are turned off on this server (PULSIX_PROBE_WRITE_METHODS)" {
			t.Fatalf("%v: http.up = %+v", extra, o)
		}
		select {
		case r := <-got:
			t.Fatalf("%v: the server saw %+v", extra, r)
		default:
		}
	}
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		obs := run(context.Background(), off, httpParams(srv.URL, map[string]any{"method": m, "headers": []any{"X-A: 1"}}), "")
		probetest.WantUp(t, obs, "http")
		if r := next(t, got); r.method != m || r.header.Get("X-A") != "1" {
			t.Fatalf("server saw %+v", r)
		}
	}
}

// TestRequestValuesNeverInObservations: neither the body, a header value
// nor the content type appears in what the probe reports, also when the
// attempt fails.
func TestRequestValuesNeverInObservations(t *testing.T) {
	srv, got := recorder(t)
	params := httpParams(srv.URL, map[string]any{
		"method": "POST", "body": "BODY-MARKER", "content_type": "text/x-marker",
		"headers":    []any{"X-Marker: HEADER-MARKER"},
		"assertions": []any{`h=header("Content-Type") exists`, `b=body contains "ok"`},
	})
	check := func(obs []probe.Observation) {
		t.Helper()
		for _, o := range obs {
			label := ""
			if o.Label != nil {
				label = *o.Label
			}
			for _, s := range []string{label, o.Err, o.Item} {
				for _, marker := range []string{"BODY-MARKER", "HEADER-MARKER", "x-marker"} {
					if strings.Contains(s, marker) {
						t.Fatalf("observation %+v carries %q", o, marker)
					}
				}
			}
		}
	}
	check(run(context.Background(), writer(), params, ""))
	next(t, got)
	srv.Close()
	check(run(context.Background(), writer(), params, ""))
	check(run(context.Background(), probetest.LoopbackGuard(), params, ""))
}

// TestProbeRefusesUnvalidatedRequest: params that never passed the
// descriptor's validation fail the attempt without sending anything and
// without quoting what was refused.
func TestProbeRefusesUnvalidatedRequest(t *testing.T) {
	srv, got := recorder(t)
	cases := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"TRACE", map[string]any{"method": "TRACE"}, "invalid method"},
		{"a header with a line break", map[string]any{"headers": []any{"X-A: a\r\nX-Evil: SECRET"}}, "invalid request header"},
		{"a credential header", map[string]any{"headers": []any{"Authorization: Bearer SECRET"}}, "invalid request header"},
		{"headers not a list", map[string]any{"headers": "X-A: SECRET"}, "invalid request header"},
		{"a header entry not a string", map[string]any{"headers": []any{1.0}}, "invalid request header"},
		{"a body with GET", map[string]any{"body": "SECRET"}, "invalid request body"},
		{"a body not a string", map[string]any{"method": "POST", "body": 1.0}, "invalid request body"},
		{"a body over 8 KiB", map[string]any{"method": "POST", "body": strings.Repeat("S", 8193)}, "invalid request body"},
		{"a bad content type", map[string]any{"method": "POST", "body": "x", "content_type": "SECRET\r\nX: 1"}, "invalid request body"},
		{"a content type without a body", map[string]any{"method": "POST", "content_type": "text/plain"}, "invalid request body"},
		{"a body with NUL", map[string]any{"method": "POST", "body": "SECRET\x00"}, "invalid request body"},
		{"a body not UTF-8", map[string]any{"method": "POST", "body": "SECRET\xff"}, "invalid request body"},
		{"a routing header", map[string]any{"headers": []any{"X-Forwarded-For: SECRET"}}, "invalid request header"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obs := run(context.Background(), writer(), httpParams(srv.URL, c.params), "")
			m := probetest.ByMetric(t, obs)
			if o := m["http.up"]; o.Err != c.want {
				t.Fatalf("http.up = %+v, want the error %q", o, c.want)
			}
			for _, o := range obs {
				if strings.Contains(o.Err, "SECRET") {
					t.Fatalf("observation %+v quotes the request", o)
				}
			}
			select {
			case s := <-got:
				t.Fatalf("server saw a request: %+v", s)
			default:
			}
		})
	}
}
