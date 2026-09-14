package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/setup"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// testTenancy builds a *tenancy.Service with a nil store, good enough to
// satisfy api.Deps.validate in tests that never actually drive an
// organization operation (nil interface values are only a problem once
// called), mirroring testPasskeys below.
func testTenancy() *tenancy.Service {
	return tenancy.NewService(nil, nil, "http://localhost", time.Now)
}

// testAudit builds an *audit.Recorder with a nil store, good enough to
// satisfy api.Deps.validate in tests that never actually record or list an
// event (nil interface values are only a problem once called), mirroring
// testTenancy and testPasskeys above.
func testAudit() *audit.Recorder {
	return audit.NewRecorder(nil, time.Now)
}

// testSetup builds a *setup.Service with nil store/sessions deps, good
// enough to satisfy api.Deps.validate in tests that never actually run the
// wizard (nil interface values are only a problem once called), mirroring
// testTenancy and testAudit above.
func testSetup() *setup.Service {
	return setup.NewService(nil, nil, crypto.NewBox([32]byte{}), time.Now)
}

// testMailer builds a *mail.SMTP whose loader never actually runs in tests
// that only exercise routing.
func testMailer() *mail.SMTP {
	return mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) { return mail.SMTPSettings{}, nil })
}

// testHosts builds a *hosts.Service with a nil store, good enough to
// satisfy api.Deps.validate in tests that never actually drive a zone or
// host operation (nil interface values are only a problem once called),
// mirroring testTenancy and testAudit above.
func testHosts() *hosts.Service {
	return hosts.NewService(nil, time.Now)
}

// testCredentials builds a *credentials.Service with a nil store, good
// enough to satisfy api.Deps.validate in tests that never actually drive a
// credential operation (nil interface values are only a problem once
// called), mirroring testHosts above.
func testCredentials() *credentials.Service {
	return credentials.NewService(nil, nil, time.Now, nil)
}

// testSSH builds a *sshconf.Service with nil store/lookups, good enough to
// satisfy api.Deps.validate in tests that never actually drive a jump host
// or host SSH operation (nil interface values are only a problem once
// called), mirroring testHosts above.
func testSSH() *sshconf.Service {
	return sshconf.NewService(nil, nil, nil)
}

// testInventory builds an *inventory.Service with nil store/lookups, good
// enough to satisfy api.Deps.validate in tests that never actually drive an
// inventory operation (nil interface values are only a problem once
// called), mirroring testSSH above.
func testInventory() *inventory.Service {
	return inventory.NewService(nil, nil, nil, nil)
}

// testRenderer builds an *inventory.Renderer with nil store/sources, good
// enough to satisfy api.Deps.validate in tests that never actually render an
// inventory (nil interface values are only a problem once called),
// mirroring testInventory above.
func testRenderer() *inventory.Renderer {
	return inventory.NewRenderer(nil, nil, nil, nil, nil)
}

// testRepos builds a *repos.Service with nil store/lookups, good enough to
// satisfy api.Deps.validate in tests that never actually drive a repo
// operation (nil interface values are only a problem once called),
// mirroring testInventory above.
func testRepos() *repos.Service {
	return repos.NewService(nil, nil, nil)
}

// testPlaybooks builds a *playbooks.Service with nil store/repo source,
// good enough to satisfy api.Deps.validate in tests that never actually
// drive a playbook operation (nil interface values are only a problem once
// called), mirroring testRepos above.
func testPlaybooks() *playbooks.Service {
	return playbooks.NewService(nil, nil)
}

// fakeAuthService satisfies api.AuthService without touching a database, so
// tests that only exercise routing (not the auth flow itself) don't need a
// real *auth.Service.
type fakeAuthService struct{}

func (fakeAuthService) Login(context.Context, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{}, nil
}
func (fakeAuthService) Logout(context.Context, auth.Principal) error { return nil }
func (fakeAuthService) Me(context.Context, auth.Principal) (auth.User, error) {
	return auth.User{}, nil
}

// testPasskeys builds a *auth.Passkeys with nil store/users/sessions deps,
// good enough to satisfy api.Deps.validate in tests that never actually
// drive a passkey ceremony (nil interface values are only a problem once
// called).
func testPasskeys(t *testing.T) *auth.Passkeys {
	t.Helper()
	pk, err := auth.NewPasskeys(nil, nil, nil, "localhost", "Test", "http://localhost", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return pk
}

// healthzBody is a fake http response body shape for /healthz — it is a
// package-level type only so both TestHealthz cases below can decode into
// it without repeating the struct definition.
type healthzBody struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func healthzRequest(t *testing.T, deps api.Deps) healthzBody {
	t.Helper()
	h, err := api.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body healthzBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func baseHealthzDeps(t *testing.T) api.Deps {
	t.Helper()
	return api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(),
		Setup: testSetup(), Mailer: testMailer(), Hosts: testHosts(), Credentials: testCredentials(), SSH: testSSH(),
		Inventory: testInventory(), Renderer: testRenderer(),
		Repos: testRepos(), Playbooks: testPlaybooks(),
	}
}

func TestHealthz(t *testing.T) {
	body := healthzRequest(t, baseHealthzDeps(t))
	if body.Status != "ok" {
		t.Fatalf("status field = %q, want ok", body.Status)
	}
}

// TestHealthzVersionDefaultsToDev covers api.Deps.Version's zero value: a
// caller that never sets it (every existing api.Deps{} literal except
// app.Build's) must still get a healthz version, matching the "dev"
// default cmd/manager/main.go's own `version` variable falls back to for
// an unstamped `go build`/`go run`.
func TestHealthzVersionDefaultsToDev(t *testing.T) {
	deps := baseHealthzDeps(t)
	deps.Version = ""
	body := healthzRequest(t, deps)
	if body.Version != "dev" {
		t.Fatalf("version field = %q, want dev", body.Version)
	}
}

// TestHealthzReportsConfiguredVersion covers the path app.Build actually
// takes: a non-empty Deps.Version must be reported verbatim, not
// overridden by the "dev" default.
func TestHealthzReportsConfiguredVersion(t *testing.T) {
	deps := baseHealthzDeps(t)
	deps.Version = "v1.2.3"
	body := healthzRequest(t, deps)
	if body.Version != "v1.2.3" {
		t.Fatalf("version field = %q, want v1.2.3", body.Version)
	}
}

// TestNewRejectsHalfWiredDeps covers Finding 5: api.New must refuse to
// build a handler when a required dependency is missing, instead of
// registering auth routes that would nil-panic on the first request.
func TestNewRejectsHalfWiredDeps(t *testing.T) {
	if _, err := api.New(api.Deps{}); err == nil {
		t.Fatal("api.New(Deps{}) did not return an error")
	}
	if _, err := api.New(api.Deps{Sessions: auth.NewSessions(nil, time.Now)}); err == nil {
		t.Fatal("api.New without Auth did not return an error")
	}
	if _, err := api.New(api.Deps{Auth: fakeAuthService{}}); err == nil {
		t.Fatal("api.New without Sessions did not return an error")
	}
	if _, err := api.New(api.Deps{Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}}); err == nil {
		t.Fatal("api.New without TOTP did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
	}); err == nil {
		t.Fatal("api.New without Passkeys did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now), Passkeys: testPasskeys(t),
	}); err == nil {
		t.Fatal("api.New without Tokens did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now),
	}); err == nil {
		t.Fatal("api.New without Tenancy did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(),
	}); err == nil {
		t.Fatal("api.New without Audit did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(),
	}); err == nil {
		t.Fatal("api.New without Setup did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(), Setup: testSetup(),
	}); err == nil {
		t.Fatal("api.New without Mailer did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(), Setup: testSetup(),
		Mailer: testMailer(),
	}); err == nil {
		t.Fatal("api.New without Hosts did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(), Setup: testSetup(),
		Mailer: testMailer(), Hosts: testHosts(),
	}); err == nil {
		t.Fatal("api.New without Credentials did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(), Setup: testSetup(),
		Mailer: testMailer(), Hosts: testHosts(), Credentials: testCredentials(),
	}); err == nil {
		t.Fatal("api.New without SSH did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(), Setup: testSetup(),
		Mailer: testMailer(), Hosts: testHosts(), Credentials: testCredentials(), SSH: testSSH(),
	}); err == nil {
		t.Fatal("api.New without Inventory did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(), Setup: testSetup(),
		Mailer: testMailer(), Hosts: testHosts(), Credentials: testCredentials(), SSH: testSSH(), Inventory: testInventory(),
	}); err == nil {
		t.Fatal("api.New without Renderer did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(), Setup: testSetup(),
		Mailer: testMailer(), Hosts: testHosts(), Credentials: testCredentials(), SSH: testSSH(), Inventory: testInventory(),
		Renderer: testRenderer(),
	}); err == nil {
		t.Fatal("api.New without Repos did not return an error")
	}
	if _, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(), Setup: testSetup(),
		Mailer: testMailer(), Hosts: testHosts(), Credentials: testCredentials(), SSH: testSSH(), Inventory: testInventory(),
		Renderer: testRenderer(), Repos: testRepos(),
	}); err == nil {
		t.Fatal("api.New without Playbooks did not return an error")
	}
}

// testDeps returns a fully-wired (if inert) Deps, exactly like
// TestHealthz's, for tests below that need a real api.New handler rather
// than probing Deps.validate.
func testDeps(t *testing.T) api.Deps {
	t.Helper()
	return api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(), Audit: testAudit(),
		Setup: testSetup(), Mailer: testMailer(), Hosts: testHosts(), Credentials: testCredentials(), SSH: testSSH(),
		Inventory: testInventory(), Renderer: testRenderer(),
		Repos: testRepos(), Playbooks: testPlaybooks(),
	}
}

// TestNotFoundServesSPA covers Task 15's embedding contract: a request
// that does not match any registered API route — every client-side SPA
// route, since the router only knows about /healthz, /api/explorer and the
// huma-registered /api/v1/... operations — falls through to chi's
// NotFound handler, which api.New wires to web.Handler(). That handler's
// own tests (internal/web/embed_test.go) cover its fallback logic in
// detail; this test only checks that api.New actually mounts it, using the
// placeholder internal/web/dist/index.html committed for exactly this
// (see that file's own comment).
func TestNotFoundServesSPA(t *testing.T) {
	h, err := api.New(testDeps(t))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/o/acme/hosts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /o/acme/hosts: status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("GET /o/acme/hosts: body does not look like the SPA index page: %q", rec.Body.String())
	}
}

// TestNotFoundAPIPathStays404 is the other half: an unmatched /api/...
// path must still 404, not silently fall back to the SPA's index page —
// web.Handler enforces this itself (see internal/web/embed_test.go), and
// this test confirms api.New's NotFound wiring does not bypass it.
func TestNotFoundAPIPathStays404(t *testing.T) {
	h, err := api.New(testDeps(t))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/does-not-exist: status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("GET /api/v1/does-not-exist: 404 body looks like the SPA index page, not a real 404: %q", rec.Body.String())
	}
}
