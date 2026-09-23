package api_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// countingDBTX is the observable seam for "a static asset request performs
// no session lookup" (backlog 1.22): it wraps the pool the API under test
// would query through and counts statements by their SQL text, so the test
// can assert the absence — and, as a control, the presence — of the
// GetSession read CookieMiddleware's lookup triggers. Sessions is a
// concrete *auth.Sessions built over gen.Queries, so the counter sits at
// the DBTX seam gen.New accepts, not around the Sessions type itself.
type countingDBTX struct {
	inner *db.DB

	mu     sync.Mutex
	counts map[string]int
}

func newCountingDBTX(inner *db.DB) *countingDBTX {
	return &countingDBTX{inner: inner, counts: make(map[string]int)}
}

func (c *countingDBTX) record(sql string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[sql]++
}

func (c *countingDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.record(sql)
	return c.inner.Exec(ctx, sql, args...)
}

func (c *countingDBTX) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.record(sql)
	return c.inner.Query(ctx, sql, args...)
}

func (c *countingDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.record(sql)
	return c.inner.QueryRow(ctx, sql, args...)
}

// count reports how many statements whose SQL contains substr ran so far.
func (c *countingDBTX) count(substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for sql, k := range c.counts {
		if strings.Contains(sql, substr) {
			n += k
		}
	}
	return n
}

// TestStaticAssetRequestSkipsSessionLookup drives backlog 1.22's read-
// amplification fix end to end: the session cookie rides every request the
// browser makes — including the SPA's static assets — but only API routes
// ever consult the session principal, so a static request must not pay the
// GetSession database read. The counting DBTX wired into Deps.Sessions is
// the spy: a request to the SPA's index and to an embedded file must leave
// the session-lookup count at zero, while the same client's request to the
// protected /api/v1/auth/me still resolves the session (the middleware
// chain for authenticated routes is unchanged).
func TestStaticAssetRequestSkipsSessionLookup(t *testing.T) {
	var counted *countingDBTX
	s := apitest.New(t, func(s *apitest.Server) {
		counted = newCountingDBTX(s.DB)
		s.Deps.Sessions = auth.NewSessions(gen.New(counted), time.Now)
	})
	uid := s.SeedUser("static-skip@example.test", "correct horse battery")
	client := s.LoginAs(uid)

	// The SPA index is served by web.Handler as the router's NotFound
	// fallback; with the session cookie present the request must not touch
	// the sessions table at all.
	resp, err := client.Get(s.HTTP.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<html") {
		t.Fatalf("GET /: status %d body %q, want 200 with the SPA index page", resp.StatusCode, body)
	}
	if n := counted.count("FROM sessions"); n != 0 {
		t.Fatalf("GET / performed %d session lookup(s), want 0", n)
	}

	// An embedded static file: same requirement.
	resp, err = client.Get(s.HTTP.URL + "/robots.txt")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /robots.txt: status %d, want 200", resp.StatusCode)
	}
	if n := counted.count("FROM sessions"); n != 0 {
		t.Fatalf("GET /robots.txt performed %d session lookup(s), want 0", n)
	}

	// Control: a protected API route with the same session cookie still
	// resolves the principal through the session store.
	resp, err = client.Get(s.HTTP.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/me with a valid session: status %d, want 200 (protected routes must keep authenticating)", resp.StatusCode)
	}
	if n := counted.count("FROM sessions"); n == 0 {
		t.Fatal("GET /api/v1/auth/me performed no session lookup, want at least one")
	}
}
