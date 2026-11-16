package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// seen records the credential headers of the last request a server got.
type seen struct {
	mu      sync.Mutex
	headers http.Header
	hits    atomic.Int32
}

func (s *seen) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headers = r.Header.Clone()
	s.hits.Add(1)
}

func (s *seen) get(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headers.Get(name)
}

// recordingServer records every request and answers 200.
func recordingServer(t *testing.T) (*httptest.Server, *seen) {
	t.Helper()
	s := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { s.record(r) }))
	t.Cleanup(srv.Close)
	return srv, s
}

func withSecret(url string, extra map[string]any) map[string]any {
	p := httpParams(url, extra)
	p["secret"] = "api"
	return p
}

// TestHTTPCredentialForms: each of the three forms reaches the server.
func TestHTTPCredentialForms(t *testing.T) {
	srv, s := recordingServer(t)
	for _, tc := range []struct {
		name   string
		fields map[string]string
		header string
		want   string
	}{
		{"basic", map[string]string{"username": "monitor", "password": "pa:ss wörd"}, "Authorization", "Basic bW9uaXRvcjpwYTpzcyB3w7ZyZA=="},
		{"bearer", map[string]string{"token": "t0ken"}, "Authorization", "Bearer t0ken"},
		{"header", map[string]string{"header": "x-api-key", "value": "k3y"}, "X-Api-Key", "k3y"},
		{"other fields are ignored", map[string]string{"token": "t0ken", "note": "rotated monthly"}, "Authorization", "Bearer t0ken"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obs := runWith(context.Background(), probetest.LoopbackGuard(), withSecret(srv.URL, nil), "", tc.fields)
			if v := probetest.WantValue(t, probetest.ByMetric(t, obs), "http.status_code"); v != 200 {
				t.Fatalf("http.status_code = %v, want 200", v)
			}
			if got := s.get(tc.header); got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

// TestHTTPNoSecretParamNoCredentials: fields without a secret param are
// never sent; the runner hands none then anyway.
func TestHTTPNoSecretParamNoCredentials(t *testing.T) {
	srv, s := recordingServer(t)
	runWith(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, nil), "", map[string]string{"token": "t0ken"})
	if got := s.get("Authorization"); got != "" || s.hits.Load() != 1 {
		t.Fatalf("Authorization = %q after %d requests, want none after one", got, s.hits.Load())
	}
}

// TestHTTPBadCredentials: a secret that is not exactly one usable form is
// an error on every metric, naming the secret and the problem, never a
// value; nothing is requested.
func TestHTTPBadCredentials(t *testing.T) {
	const mustHold = `secret "api" must hold username and password, token, or header and value`
	const reserved = `secret "api": field "header" must not be Host, Content-Length, Transfer-Encoding, Connection or User-Agent`
	srv, s := recordingServer(t)
	for _, tc := range []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"none", nil, mustHold},
		{"empty", map[string]string{}, mustHold},
		{"unrelated only", map[string]string{"community": "s3cret"}, mustHold},
		{"username only", map[string]string{"username": "u-s3cret"}, mustHold},
		{"password only", map[string]string{"password": "p-s3cret"}, mustHold},
		{"header only", map[string]string{"header": "X-Key"}, mustHold},
		{"value only", map[string]string{"value": "v-s3cret"}, mustHold},
		{"token and basic", map[string]string{"token": "t-s3cret", "username": "u-s3cret", "password": "p-s3cret"}, mustHold},
		{"token and header", map[string]string{"token": "t-s3cret", "header": "X-Key", "value": "v-s3cret"}, mustHold},
		{"all", map[string]string{"token": "t-s3cret", "username": "u-s3cret", "password": "p-s3cret", "header": "X-Key", "value": "v-s3cret"}, mustHold},
		{"host", map[string]string{"header": "Host", "value": "v-s3cret"}, reserved},
		{"content-length", map[string]string{"header": "content-length", "value": "1"}, reserved},
		{"transfer-encoding", map[string]string{"header": "TRANSFER-ENCODING", "value": "chunked"}, reserved},
		{"connection", map[string]string{"header": "Connection", "value": "close"}, reserved},
		{"user-agent", map[string]string{"header": "user-agent", "value": "curl"}, reserved},
		{"header with a space", map[string]string{"header": "X Key", "value": "v-s3cret"}, `secret "api": field "header" must be an HTTP header name`},
		{"header with a colon", map[string]string{"header": "X-Key:", "value": "v-s3cret"}, `secret "api": field "header" must be an HTTP header name`},
		{"value with a newline", map[string]string{"header": "X-Key", "value": "v-s3cret\r\nX-Evil: 1"}, `secret "api": field "value" must be a valid HTTP header value`},
		{"token with a newline", map[string]string{"token": "t-s3cret\nX-Evil: 1"}, `secret "api": field "token" must be a valid HTTP header value`},
		{"username with a colon", map[string]string{"username": "u:s3cret", "password": "p-s3cret"}, `secret "api": field "username" must not contain a colon`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obs := runWith(context.Background(), probetest.LoopbackGuard(), withSecret(srv.URL, map[string]any{
				"body_contains": "x", "json_values": values("a=$.a"),
			}), "", tc.fields)
			m := byItem(t, obs)
			want := []mi{{"http.body_match", ""}, {"http.json.value", "a"}}
			for _, metric := range failAllHostMetrics {
				want = append(want, mi{metric, ""})
			}
			if len(m) != len(want) {
				t.Fatalf("observations = %+v, want an error on %v", obs, want)
			}
			for _, k := range want {
				if m[k].Err != tc.want {
					t.Fatalf("%v = %+v, want error %q", k, m[k], tc.want)
				}
				if strings.Contains(m[k].Err, "s3cret") {
					t.Fatalf("%v carries a secret value: %q", k, m[k].Err)
				}
			}
		})
	}
	if n := s.hits.Load(); n != 0 {
		t.Fatalf("the server got %d requests with unusable credentials", n)
	}
}

// redirector answers every request with a redirect to target.
func redirector(t *testing.T, tlsServer bool, target func() string) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target(), http.StatusFound)
	})
	var srv *httptest.Server
	if tlsServer {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv
}

// TestHTTPCredentialsOnRedirects: credentials follow a redirect to the
// same scheme, host and port only; once dropped they stay dropped.
func TestHTTPCredentialsOnRedirects(t *testing.T) {
	forms := map[string]struct {
		fields map[string]string
		header string
	}{
		"bearer": {map[string]string{"token": "t0ken"}, "Authorization"},
		"basic":  {map[string]string{"username": "u", "password": "p"}, "Authorization"},
		"header": {map[string]string{"header": "X-Api-Key", "value": "k3y"}, "X-Api-Key"},
	}
	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			t.Run("same origin keeps them", func(t *testing.T) {
				s := &seen{}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/start" {
						http.Redirect(w, r, "/end", http.StatusFound)
						return
					}
					s.record(r)
				}))
				t.Cleanup(srv.Close)
				runWith(context.Background(), probetest.LoopbackGuard(), withSecret(srv.URL+"/start", nil), "", form.fields)
				if s.hits.Load() != 1 || s.get(form.header) == "" {
					t.Fatalf("%s = %q after %d hits, want it kept", form.header, s.get(form.header), s.hits.Load())
				}
			})
			t.Run("another port drops them", func(t *testing.T) {
				end, s := recordingServer(t)
				start := redirector(t, false, func() string { return end.URL + "/end" })
				runWith(context.Background(), probetest.LoopbackGuard(), withSecret(start.URL, nil), "", form.fields)
				if s.hits.Load() != 1 || s.get(form.header) != "" {
					t.Fatalf("%s = %q after %d hits, want it dropped", form.header, s.get(form.header), s.hits.Load())
				}
			})
			t.Run("https to http drops them", func(t *testing.T) {
				end, s := recordingServer(t)
				start := redirector(t, true, func() string { return end.URL + "/end" })
				runWith(context.Background(), probetest.LoopbackGuard(), withSecret(start.URL, map[string]any{"verify_tls": false}), "", form.fields)
				if s.hits.Load() != 1 || s.get(form.header) != "" {
					t.Fatalf("%s = %q after %d hits, want it dropped", form.header, s.get(form.header), s.hits.Load())
				}
			})
			t.Run("back to the origin stays without them", func(t *testing.T) {
				s := &seen{}
				var bounceURL string
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/start" {
						http.Redirect(w, r, bounceURL+"/bounce", http.StatusFound)
						return
					}
					s.record(r)
				}))
				t.Cleanup(origin.Close)
				bounceURL = redirector(t, false, func() string { return origin.URL + "/end" }).URL
				runWith(context.Background(), probetest.LoopbackGuard(), withSecret(origin.URL+"/start", nil), "", form.fields)
				if s.hits.Load() != 1 || s.get(form.header) != "" {
					t.Fatalf("%s = %q after %d hits, want it dropped", form.header, s.get(form.header), s.hits.Load())
				}
			})
		})
	}
}
