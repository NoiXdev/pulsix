package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
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

// TestBuildReconcilesZonelessOrganizations is Task 13 fix round 1's finding
// 3 test. An organization is seeded directly into the organizations table
// through a plain admin connection — bypassing tenancy.Service.CreateOrg
// entirely, so its OrgCreatedHook never runs — which is exactly the state a
// post-commit hook failure leaves an organization in: it exists, but has no
// "default" zone, and (per the finding) retrying org creation cannot repair
// it because the slug is already taken. Build's startup reconciliation pass
// (hostsSvc.ReconcileDefaultZones, wired in Build itself) must find and fix
// it without Build failing.
func TestBuildReconcilesZonelessOrganizations(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)

	admin, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	q := gen.New(admin.Pool())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{
		ID: uuid.Must(uuid.NewV7()), Slug: "reconcile-zoneless-org", Name: "Zoneless Org",
	})
	if err != nil {
		t.Fatal(err)
	}

	a, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build must not fail because of a zoneless organization: %v", err)
	}
	defer a.Close()

	zones, err := q.ListZonesForOrg(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 1 || zones[0].Slug != hosts.DefaultZoneSlug {
		t.Fatalf("zones for the zoneless org after Build = %+v, want exactly one default zone", zones)
	}
}

// TestBuildSeedsRuleSetOnOrgCreation is Task 5's own coverage of
// ChainOrgCreatedHooks' production composition (app.go): completing the
// first-run wizard must leave the new organization with exactly the
// five-row starter rule set rules.Service.SeedForOrg writes, each stamped
// origin "seed" and scope "org" — and with its check_definitions seed set
// still in place, proving rulesSvc.SeedForOrg was appended to the same
// chained hook rather than replacing checksSvc.SeedForOrg. A failing rule
// seed would fail this same request (setup.Service.Complete propagates the
// hook's error like CreateOrg does — OrgCreatedHook's own doc comment), so
// there is no separate negative test for propagation: it is the same code
// path this positive test already exercises.
func TestBuildSeedsRuleSetOnOrgCreation(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	a, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	body := `{"admin_email":"ruleseed-admin@example.test","admin_password":"correct horse battery","admin_name":"Root","org_slug":"ruleseed-org","org_name":"Rule Seed Org"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete setup: status %d, body %s", rec.Code, rec.Body.String())
	}

	// A plain admin connection, exactly like TestBuildReconcilesZonelessOrganizations
	// above: it bypasses row-level security, so it can read the new org's
	// rows without needing an authenticated session or org scope.
	admin, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	q := gen.New(admin.Pool())
	org, err := q.GetOrganizationBySlug(ctx, "ruleseed-org")
	if err != nil {
		t.Fatal(err)
	}

	var defCount int
	if err := admin.Pool().QueryRow(ctx, `SELECT count(*) FROM check_definitions WHERE org_id = $1`, org.ID).Scan(&defCount); err != nil {
		t.Fatal(err)
	}
	if defCount != 4 {
		t.Fatalf("check_definitions for the new org = %d, want the 4-plugin seed set (checksSvc.SeedForOrg must still run)", defCount)
	}

	rows, err := admin.Pool().Query(ctx, `SELECT origin, scope, metric, severity FROM check_rules WHERE org_id = $1`, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type ruleRow struct{ origin, scope, metric, severity string }
	var got []ruleRow
	for rows.Next() {
		var r ruleRow
		if err := rows.Scan(&r.origin, &r.scope, &r.metric, &r.severity); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("check_rules for the new org = %+v, want exactly 5 rows", got)
	}
	seen := map[string]bool{}
	for _, r := range got {
		if r.origin != "seed" || r.scope != "org" {
			t.Fatalf("rule row %+v, want origin=seed scope=org", r)
		}
		seen[r.metric+"/"+r.severity] = true
	}
	for _, want := range []string{"disk.used_pct/warn", "disk.used_pct/crit", "mem.used_pct/warn", "mem.used_pct/crit", "cpu.used_pct/warn"} {
		if !seen[want] {
			t.Fatalf("seed set missing %q, got %+v", want, got)
		}
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
