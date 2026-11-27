// Package webchecktest holds the test helpers the web server check plugins
// share: a status page server, a fake clock and observation assertions. It
// is imported from _test.go files only.
package webchecktest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// The planted login every leak test uses.
const (
	PlantUser = "plantuser-5e2"
	PlantPass = "plantpass-8b4"
)

// Server serves one configurable status and body and records each
// request's Authorization header and request URI.
type Server struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	body   string
	auth   []string
	uris   []string
}

// NewServer starts a server answering 200 with body; it closes with t.
func NewServer(t *testing.T, body string) *Server {
	t.Helper()
	s := &Server{status: http.StatusOK, body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.uris = append(s.uris, r.URL.RequestURI())
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.Close)
	return s
}

// Set changes what the next requests get.
func (s *Server) Set(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body = status, body
}

// Auths is the Authorization header of every request so far ("" if none).
func (s *Server) Auths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}

// URIs is the request URI (path and query) of every request so far.
func (s *Server) URIs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.uris...)
}

// NewHangingServer starts a server that accepts requests and never
// answers; it releases its handlers and closes with t.
func NewHangingServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close) // runs after the release below
	t.Cleanup(func() { close(release) })
	return srv
}

// Clock is a settable clock; plugins take its Now as their time source.
type Clock struct{ T time.Time }

// NewClock starts at a fixed instant.
func NewClock() *Clock { return &Clock{T: time.Unix(1_000_000, 0)} }

// Now is the plugins' time source.
func (c *Clock) Now() time.Time { return c.T }

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) { c.T = c.T.Add(d) }

// Find returns the observation for metric on item ""; it panics when one
// has an item (a server metric never does; FindItem reads the per-pool and
// API items).
func Find(obs []checks.Observation, metric string) (checks.Observation, bool) {
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

// MustValue asserts metric is a plain value.
func MustValue(t *testing.T, obs []checks.Observation, metric string, want float64) {
	t.Helper()
	o, ok := Find(obs, metric)
	if !ok {
		t.Fatalf("no observation %q in %+v", metric, obs)
	}
	if o.Value == nil || *o.Value != want || o.Err != "" {
		t.Fatalf("%q = %+v, want value %v", metric, o, want)
	}
}

// MustAbsent asserts metric is not reported.
func MustAbsent(t *testing.T, obs []checks.Observation, metric string) {
	t.Helper()
	if o, ok := Find(obs, metric); ok {
		t.Fatalf("%q reported: %+v", metric, o)
	}
}

// MustErr asserts metric is an error observation "<prefix>: <reason>" with
// no value and no label.
func MustErr(t *testing.T, obs []checks.Observation, prefix, metric, reason string) {
	t.Helper()
	o, ok := Find(obs, metric)
	if !ok || o.Value != nil || o.Label != nil || o.Err != prefix+": "+reason {
		t.Fatalf("%q = %+v, want error %q", metric, o, prefix+": "+reason)
	}
}

// MustDown asserts the whole down set: up = 0 and the reason on exactly the
// other metrics, nothing else.
func MustDown(t *testing.T, obs []checks.Observation, prefix, up string, others []string, reason string) {
	t.Helper()
	MustValue(t, obs, up, 0)
	if len(obs) != 1+len(others) {
		t.Fatalf("got %d observations, want %d: %+v", len(obs), 1+len(others), obs)
	}
	for _, m := range others {
		MustErr(t, obs, prefix, m, reason)
	}
}

// NoSecretIn asserts neither the planted login nor the server's host:port
// appears in any observation (item, metric, error, label) or in err.
func NoSecretIn(t *testing.T, srv *Server, obs []checks.Observation, err error) {
	t.Helper()
	var all []string
	for _, o := range obs {
		all = append(all, o.Item, o.Metric, o.Err)
		if o.Label != nil {
			all = append(all, *o.Label)
		}
	}
	if err != nil {
		all = append(all, err.Error())
	}
	text := strings.Join(all, "\n")
	for _, s := range []string{PlantUser, PlantPass, strings.TrimPrefix(srv.URL, "http://")} {
		if strings.Contains(text, s) {
			t.Fatalf("%q leaked into %q", s, text)
		}
	}
}

// FindItem returns the observation for metric on item (backlog 3.11b: the
// nginx and caddy API items).
func FindItem(obs []checks.Observation, item, metric string) (checks.Observation, bool) {
	for _, o := range obs {
		if o.Item == item && o.Metric == metric {
			return o, true
		}
	}
	return checks.Observation{}, false
}

// MustItemValue asserts metric on item is a plain value.
func MustItemValue(t *testing.T, obs []checks.Observation, item, metric string, want float64) {
	t.Helper()
	o, ok := FindItem(obs, item, metric)
	if !ok {
		t.Fatalf("no observation %q on %q in %+v", metric, item, obs)
	}
	if o.Value == nil || *o.Value != want || o.Err != "" {
		t.Fatalf("%q on %q = %+v, want value %v", metric, item, o, want)
	}
}

// MustItemErr asserts metric on item is an error observation
// "<prefix>: <reason>" with no value and no label.
func MustItemErr(t *testing.T, obs []checks.Observation, prefix, item, metric, reason string) {
	t.Helper()
	o, ok := FindItem(obs, item, metric)
	if !ok || o.Value != nil || o.Label != nil || o.Err != prefix+": "+reason {
		t.Fatalf("%q on %q = %+v, want error %q", metric, item, o, prefix+": "+reason)
	}
}

// MustItemAbsent asserts metric on item is not reported.
func MustItemAbsent(t *testing.T, obs []checks.Observation, item, metric string) {
	t.Helper()
	if o, ok := FindItem(obs, item, metric); ok {
		t.Fatalf("%q on %q reported: %+v", metric, item, o)
	}
}

// Items is the set of items with any observation, "" included.
func Items(obs []checks.Observation) map[string]bool {
	out := map[string]bool{}
	for _, o := range obs {
		out[o.Item] = true
	}
	return out
}
