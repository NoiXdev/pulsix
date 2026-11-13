package webcheck_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

const (
	plantUser = "plantuser-7f3"
	plantPass = "plantpass-9c1"
)

func TestFetchBody(t *testing.T) {
	var gotUA, gotAuth string
	var hasAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAuth = r.Header.Get("Authorization")
		_, hasAuth = r.Header["Authorization"]
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	body, reason := webcheck.Fetch(t.Context(), srv.URL, webcheck.Options{})
	if reason != "" || string(body) != "hello" {
		t.Fatalf("Fetch = %q, %q; want hello, no reason", body, reason)
	}
	if gotUA != "pulsix-agent" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if hasAuth {
		t.Errorf("Authorization sent without a secret: %q", gotAuth)
	}

	_, reason = webcheck.Fetch(t.Context(), srv.URL, webcheck.Options{Auth: webcheck.Auth{User: plantUser, Password: plantPass, Set: true}})
	if reason != "" {
		t.Fatalf("reason with auth = %q", reason)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	req.SetBasicAuth(plantUser, plantPass)
	if gotAuth != req.Header.Get("Authorization") {
		t.Errorf("Authorization = %q, want %q", gotAuth, req.Header.Get("Authorization"))
	}
}

func TestFetchAuthWithEmptyPassword(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	webcheck.Fetch(t.Context(), srv.URL, webcheck.Options{Auth: webcheck.Auth{User: plantUser, Set: true}})
	req, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	req.SetBasicAuth(plantUser, "")
	if gotAuth != req.Header.Get("Authorization") {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

func TestFetchStatusReasons(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{http.StatusUnauthorized, "authentication failed (HTTP 401)"},
		{http.StatusForbidden, "permission denied (HTTP 403)"},
		{http.StatusInternalServerError, "HTTP status 500"},
		{http.StatusNotFound, "HTTP status 404"},
		{http.StatusNoContent, "HTTP status 204"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.code)
				_, _ = w.Write([]byte("secret page body " + plantUser + plantPass))
			}))
			defer srv.Close()
			body, reason := webcheck.Fetch(t.Context(), srv.URL, webcheck.Options{Auth: webcheck.Auth{User: plantUser, Password: plantPass, Set: true}})
			if reason != tt.want || body != nil {
				t.Fatalf("Fetch = %q, %q; want nil, %q", body, reason, tt.want)
			}
			for _, leak := range []string{plantUser, plantPass, srv.URL, "127.0.0.1", "secret page"} {
				if strings.Contains(reason, leak) {
					t.Errorf("reason %q carries %q", reason, leak)
				}
			}
		})
	}
}

func TestFetchDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/leak", http.StatusMovedPermanently)
	}))
	defer srv.Close()

	body, reason := webcheck.Fetch(t.Context(), srv.URL, webcheck.Options{Auth: webcheck.Auth{User: plantUser, Password: plantPass, Set: true}})
	if reason != "HTTP status 301" || body != nil {
		t.Fatalf("Fetch = %q, %q; want HTTP status 301", body, reason)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("the redirect target was requested %d times", targetHits.Load())
	}
}

func TestFetchBodyLimit(t *testing.T) {
	for _, tt := range []struct {
		name string
		size int
		want string
	}{
		{"exactly the limit", webcheck.MaxBody, ""},
		{"one byte over", webcheck.MaxBody + 1, "response too large"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat("a", tt.size)))
			}))
			defer srv.Close()
			body, reason := webcheck.Fetch(t.Context(), srv.URL, webcheck.Options{})
			if reason != tt.want {
				t.Fatalf("reason = %q, want %q", reason, tt.want)
			}
			if tt.want == "" && len(body) != tt.size {
				t.Fatalf("len(body) = %d", len(body))
			}
			if tt.want != "" && body != nil {
				t.Fatalf("body returned with reason %q", reason)
			}
		})
	}
}

func TestFetchSlowServerTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 5 s request deadline")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	body, reason := webcheck.Fetch(t.Context(), srv.URL, webcheck.Options{})
	if reason != "timed out" || body != nil {
		t.Fatalf("Fetch = %q, %q; want timed out", body, reason)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestFetchTricklingBodyTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 5 s request deadline")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for {
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-release:
				return
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	body, reason := webcheck.Fetch(t.Context(), srv.URL, webcheck.Options{})
	if reason != "timed out" || body != nil {
		t.Fatalf("Fetch = %d bytes, %q; want timed out", len(body), reason)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestFetchCallerContextEnding(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	body, reason := webcheck.Fetch(ctx, srv.URL, webcheck.Options{})
	if reason != "timed out" || body != nil {
		t.Fatalf("Fetch = %q, %q", body, reason)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("did not return with the caller's context")
	}
}

func TestFetchConnectionRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	body, reason := webcheck.Fetch(t.Context(), "http://"+addr+"/status", webcheck.Options{})
	if reason != "connection refused" || body != nil {
		t.Fatalf("Fetch = %q, %q", body, reason)
	}
	if strings.Contains(reason, addr) {
		t.Fatal("reason names the address")
	}
}

func TestFetchHostNotFound(t *testing.T) {
	_, reason := webcheck.Fetch(t.Context(), "http://host-that-does-not-exist.invalid/status", webcheck.Options{})
	if reason != "host not found" && reason != "timed out" {
		t.Fatalf("reason = %q", reason)
	}
	if strings.Contains(reason, "invalid") {
		t.Fatal("reason names the host")
	}
}

// A URL with user info is refused without a request, so net/http never
// derives an Authorization header from it.
func TestFetchRefusesUserInfo(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	u := "http://" + plantUser + ":" + plantPass + "@" + strings.TrimPrefix(srv.URL, "http://") + "/s"
	body, reason := webcheck.Fetch(t.Context(), u, webcheck.Options{})
	if body != nil || reason != "connection failed" {
		t.Fatalf("Fetch = %q, %q; want connection failed", body, reason)
	}
	if hits.Load() != 0 {
		t.Fatalf("server was hit %d times", hits.Load())
	}
}

func TestFetchBadURL(t *testing.T) {
	_, reason := webcheck.Fetch(t.Context(), "http://bad\x7fhost/"+plantUser, webcheck.Options{})
	if reason != "connection failed" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestFetchTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secure"))
	}))
	defer srv.Close()

	body, reason := webcheck.Fetch(t.Context(), srv.URL, webcheck.Options{VerifyTLS: true})
	if reason != "TLS handshake failed" || body != nil {
		t.Fatalf("verify: Fetch = %q, %q; want TLS handshake failed", body, reason)
	}
	body, reason = webcheck.Fetch(t.Context(), srv.URL, webcheck.Options{VerifyTLS: false})
	if reason != "" || string(body) != "secure" {
		t.Fatalf("skip verify: Fetch = %q, %q", body, reason)
	}
}

func TestParseAuth(t *testing.T) {
	tests := []struct {
		name    string
		fields  map[string]string
		want    webcheck.Auth
		wantErr string
	}{
		{"nil means no authentication", nil, webcheck.Auth{}, ""},
		{"user and password", map[string]string{"basic_auth_user": "u", "basic_auth_password": "p"}, webcheck.Auth{User: "u", Password: "p", Set: true}, ""},
		{"empty password allowed", map[string]string{"basic_auth_user": "u"}, webcheck.Auth{User: "u", Set: true}, ""},
		{"user missing", map[string]string{"basic_auth_password": "p"}, webcheck.Auth{}, "secret field `basic_auth_user` is required"},
		{"empty map", map[string]string{}, webcheck.Auth{}, "secret field `basic_auth_user` is required"},
		{"unknown field", map[string]string{"basic_auth_user": "u", "password": "p"}, webcheck.Auth{}, "secret field `password` is not known"},
		{"redis style secret is refused", map[string]string{"password": "p"}, webcheck.Auth{}, "secret field `basic_auth_user` is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := webcheck.ParseAuth(tt.fields)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ParseAuth = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestWithQueryKey(t *testing.T) {
	tests := []struct{ name, in, key, want string }{
		{"no query", "http://h/server-status", "auto", "http://h/server-status?auto"},
		{"empty query", "http://h/s?", "auto", "http://h/s?auto"},
		{"other keys", "http://h/s?x=1", "auto", "http://h/s?x=1&auto"},
		{"bare key present", "http://h/s?auto", "auto", "http://h/s?auto"},
		{"key with value present", "http://h/s?auto=1", "auto", "http://h/s?auto=1"},
		{"key present among others", "http://h/s?a=1&json&b=2", "json", "http://h/s?a=1&json&b=2"},
		{"longer key is another key", "http://h/s?automatic", "auto", "http://h/s?automatic&auto"},
		{"key as a value is not a key", "http://h/s?x=auto", "auto", "http://h/s?x=auto&auto"},
		{"json", "http://h/status", "json", "http://h/status?json"},
		{"ipv6 and port", "https://[::1]:8443/status?full", "json", "https://[::1]:8443/status?full&json"},
		{"trailing ampersand", "http://h/s?x=1&", "auto", "http://h/s?x=1&auto"},
		{"only ampersand", "http://h/s?&", "auto", "http://h/s?&auto"},
		{"fragment kept", "http://h/s#f", "auto", "http://h/s?auto#f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webcheck.WithQueryKey(tt.in, tt.key); got != tt.want {
				t.Fatalf("WithQueryKey(%q, %q) = %q, want %q", tt.in, tt.key, got, tt.want)
			}
		})
	}
}

func TestURLParam(t *testing.T) {
	const def = "http://127.0.0.1/status"
	tests := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"nil params", nil, def},
		{"absent", map[string]any{}, def},
		{"empty", map[string]any{"url": ""}, def},
		{"wrong type", map[string]any{"url": 3}, def},
		{"given", map[string]any{"url": "http://h/s"}, "http://h/s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webcheck.URLParam(tt.params, "url", def); got != tt.want {
				t.Fatalf("URLParam = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerifyTLSParam(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   bool
	}{
		{"nil params", nil, true},
		{"absent", map[string]any{}, true},
		{"true", map[string]any{"verify_tls": true}, true},
		{"false", map[string]any{"verify_tls": false}, false},
		{"wrong type", map[string]any{"verify_tls": "false"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webcheck.VerifyTLS(tt.params); got != tt.want {
				t.Fatalf("VerifyTLS = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDownItem(t *testing.T) {
	obs := webcheck.DownItem("www", "phpfpm.pool", "phpfpm.up", []string{"phpfpm.active", "phpfpm.idle"}, "connection refused")
	if len(obs) != 3 {
		t.Fatalf("len = %d, want 3", len(obs))
	}
	for _, o := range obs {
		if o.Item != "www" {
			t.Errorf("item = %q, want www", o.Item)
		}
	}
	if obs[0].Metric != "phpfpm.up" || obs[0].Err != "" {
		t.Errorf("first = %+v, want the up value", obs[0])
	}
	if obs[1].Err != "phpfpm.pool: connection refused" {
		t.Errorf("second = %+v", obs[1])
	}
}
