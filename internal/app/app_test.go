package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// testConfig builds a valid config.Config against the shared test
// database, migrated by testdb.URL. "localhost" (not an IP literal) is
// used for PublicURL for the same reason apitest.New uses it: go-webauthn
// rejects an RPID that is an IP address.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Listen: ":0", DatabaseURL: testdb.URL(t), PublicURL: "http://localhost:8080",
		MasterKey: [32]byte{'t', 'e', 's', 't', '-', 'm', 'a', 's', 't', 'e', 'r', '-', 'k', 'e', 'y'},
	}
}

// TestBuildWithTestDatabase is the brief's own required test: Build against
// a real (migrated) database must produce a working handler — one that
// answers health checks and, more importantly, the setup status route that
// tells the SPA whether to redirect into the wizard.
func TestBuildWithTestDatabase(t *testing.T) {
	ctx := context.Background()
	a, err := Build(ctx, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"completed":false`) {
		t.Fatalf("setup status on a fresh database = %s, want completed:false", rec.Body.String())
	}
}

// TestBuildSetsSecureCookieForHTTPSPublicURL checks the one piece of Build
// that would otherwise be silently wrong in production and invisible in
// every other test here (which all use an http:// PublicURL, matching
// httptest): an https PublicURL must produce a Secure session cookie.
func TestBuildSetsSecureCookieForHTTPSPublicURL(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.PublicURL = "https://pulsix.example.test"
	a, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	body := `{"admin_email":"root@example.test","admin_password":"correct horse battery","admin_name":"Root","org_slug":"acme","org_name":"Acme"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete setup: status %d, body %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || !cookies[0].Secure {
		t.Fatalf("session cookie not Secure for an https PublicURL: %+v", cookies)
	}
}

// TestBuildFailsOnUnreachableDatabase covers Build's first failure path:
// an unreachable PULSIX_DATABASE_URL must be reported as an error, not a
// panic or a handler that appears to work until the first query.
func TestBuildFailsOnUnreachableDatabase(t *testing.T) {
	cfg := testConfig(t)
	cfg.DatabaseURL = "postgres://nope:nope@127.0.0.1:1/nope"
	if _, err := Build(context.Background(), cfg); err == nil {
		t.Fatal("Build against an unreachable database: got nil error")
	}
}

// TestAssertCannotBypassRLSRefusesSuperuser is the brief's other required
// coverage, verified directly against the guard function itself: a
// connection that never issued SET ROLE into pulsix_app — exactly what the
// bootstrap superuser testdb.URL(t) hands back connects as — is a
// superuser, and a superuser bypasses row-level security unconditionally,
// regardless of FORCE ROW LEVEL SECURITY. Build must never accept it.
func TestAssertCannotBypassRLSRefusesSuperuser(t *testing.T) {
	ctx := context.Background()
	superuserDB, err := db.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer superuserDB.Close()

	if err := assertCannotBypassRLS(ctx, superuserDB); err == nil {
		t.Fatal("assertCannotBypassRLS accepted a superuser connection with no SET ROLE")
	}
}

// TestAssertCannotBypassRLSAcceptsAppRole is the positive counterpart:
// pulsix_app itself — what Build actually connects requests through, via
// db.OpenAsRole — must pass the same check.
func TestAssertCannotBypassRLSAcceptsAppRole(t *testing.T) {
	ctx := context.Background()
	appDB, err := db.OpenAsRole(ctx, testdb.URL(t), appRole)
	if err != nil {
		t.Fatal(err)
	}
	defer appDB.Close()

	if err := assertCannotBypassRLS(ctx, appDB); err != nil {
		t.Fatalf("assertCannotBypassRLS rejected pulsix_app: %v", err)
	}
}

func TestPublicURLIsHTTPS(t *testing.T) {
	cases := map[string]bool{
		"https://pulsix.example.test": true,
		"http://pulsix.example.test":  false,
		"not a url at all":            false,
	}
	for in, want := range cases {
		if got := publicURLIsHTTPS(in); got != want {
			t.Errorf("publicURLIsHTTPS(%q) = %v, want %v", in, got, want)
		}
	}
}
