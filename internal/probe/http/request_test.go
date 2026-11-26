package http_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
			obs := run(context.Background(), probetest.LoopbackGuard(), params, "")
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
	params := httpParams(srv.URL, map[string]any{"secret": "api", "headers": []any{"X-Api-Key: plain", "X-Other: 1"}})
	obs := runWith(context.Background(), probetest.LoopbackGuard(), params, "", map[string]string{"header": "X-Api-Key", "value": "s3cret"})
	probetest.WantUp(t, obs, "http")
	s := next(t, got)
	if v := s.header.Values("X-Api-Key"); len(v) != 1 || v[0] != "s3cret" {
		t.Fatalf("X-Api-Key = %v, want only the secret's value", v)
	}
	if s.header.Get("X-Other") != "1" {
		t.Fatalf("headers = %v", s.header)
	}
}

// TestRedirectBodyFollowsBrowserRules: a 307 resends the method and the
// body, a 303 turns into a GET without one; plain headers go along on the
// same origin.
func TestRedirectBodyFollowsBrowserRules(t *testing.T) {
	got := make(chan received, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- received{method: r.Method, header: r.Header.Clone(), body: string(b), path: r.URL.Path}
		switch r.URL.Path {
		case "/307":
			http.Redirect(w, r, "/end", http.StatusTemporaryRedirect)
		case "/303":
			http.Redirect(w, r, "/end", http.StatusSeeOther)
		}
	}))
	defer srv.Close()
	for _, c := range []struct {
		path, method, body string
	}{
		{"/307", "POST", "payload"},
		{"/303", "GET", ""},
	} {
		params := httpParams(srv.URL+c.path, map[string]any{"method": "POST", "body": "payload", "headers": []any{"X-Trace: t1"}})
		obs := run(context.Background(), probetest.LoopbackGuard(), params, "")
		probetest.WantUp(t, obs, "http")
		first, second := next(t, got), next(t, got)
		if first.method != "POST" || first.body != "payload" {
			t.Fatalf("%s: first hop %s %q", c.path, first.method, first.body)
		}
		if second.path != "/end" || second.method != c.method || second.body != c.body {
			t.Fatalf("%s: second hop %s %s %q, want %s %q", c.path, second.path, second.method, second.body, c.method, c.body)
		}
		if second.header.Get("X-Trace") != "t1" {
			t.Fatalf("%s: plain header lost on the redirect: %v", c.path, second.header)
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
	check(run(context.Background(), probetest.LoopbackGuard(), params, ""))
	next(t, got)
	srv.Close()
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, c.params), "")
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
