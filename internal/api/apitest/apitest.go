// Package apitest spins up the full API against the test database.
package apitest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/setup"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// SentMail is one message captured by Mailer.
type SentMail struct{ To, Subject, Body string }

// Mailer is a recording tenancy.Mailer used in place of the real SMTP
// sender Task 12 wires in production. It is safe for concurrent use since
// the httptest server may serve requests on more than one goroutine.
type Mailer struct {
	mu   sync.Mutex
	sent []SentMail
}

// Send records the message and always succeeds.
func (m *Mailer) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, SentMail{To: to, Subject: subject, Body: body})
	return nil
}

// Sent returns every message recorded so far, oldest first.
func (m *Mailer) Sent() []SentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SentMail, len(m.sent))
	copy(out, m.sent)
	return out
}

// testMasterKey is a fixed, non-secret key used only to seal TOTP secrets in
// tests. Never used outside apitest.
var testMasterKey = [32]byte{'p', 'u', 'l', 's', 'i', 'x', '-', 't', 'e', 's', 't', '-', 'k', 'e', 'y'}

// Server is a running API plus helpers.
type Server struct {
	T        *testing.T
	DB       *db.DB
	Q        *gen.Queries
	Sessions *auth.Sessions
	Mail     *Mailer
	Deps     api.Deps
	HTTP     *httptest.Server
}

// Option customizes a Server's Deps before its httptest.Server starts.
type Option func(*Server)

// WithSecureCookies sets Deps.Secure, so tests can exercise the Secure
// attribute on the real Set-Cookie response of the login endpoint instead
// of only asserting it on code with no caller (Finding 3, fix round 1).
func WithSecureCookies() Option {
	return func(s *Server) { s.Deps.Secure = true }
}

// WithAuthWrapper replaces Deps.Auth with wrap's return value, applied to
// the default real auth.Service. It exists so a test can make one method
// fail on demand — the real service, backed by a working test database,
// otherwise has no easy way to fail (Finding 1's forced-revoke-failure
// test, Finding 6's unmapped-error test).
func WithAuthWrapper(wrap func(api.AuthService) api.AuthService) Option {
	return func(s *Server) { s.Deps.Auth = wrap(s.Deps.Auth) }
}

// failingAuditStore always fails Insert while forwarding List/ListForActor
// to a real store, so a test can drive the API normally (its reads keep
// working) while every Record call fails.
type failingAuditStore struct{ audit.Store }

func (failingAuditStore) Insert(context.Context, audit.Event) error {
	return errors.New("simulated audit store failure")
}

// WithFailingAudit replaces Deps.Audit with a recorder whose store always
// fails to Insert. It exists for
// TestRecordingFailureDoesNotBreakRequest (audit_handlers_test.go), the one
// test in this codebase for the tradeoff auditMiddleware's own doc comment
// describes: a mutating request's own effect and response must not depend
// on whether recording that it happened succeeds.
func WithFailingAudit() Option {
	return func(s *Server) {
		s.Deps.Audit = audit.NewRecorder(failingAuditStore{Store: audit.NewSQLStore(s.Q)}, time.Now)
	}
}

// New builds Deps from the test database and starts an httptest server.
//
// The WebAuthn relying party id and origin are derived from the test
// server's own URL via config.Config.RPID/Origin — the same derivation
// production uses from PULSIX_PUBLIC_URL — rather than hardcoding a literal
// origin that cannot be known before the server has a URL. That URL is not
// known until a listener exists, and building auth.Passkeys is what needs
// it, so the httptest.Server is created unstarted first (which does bind
// its listener up front) purely to learn its address; the real handler,
// built once Deps (including Passkeys) is complete, is only attached to it
// afterwards, before Start is called.
//
// httptest's default listener binds to the loopback IP (127.0.0.1), but
// go-webauthn (as of v0.18.1) rejects an RPID that is an IP address — it
// must be a domain string, per the spec. "localhost" resolves to the same
// loopback address on essentially every system without needing a listener
// bound to a literal hostname (net.Listen can't bind to "localhost" as
// such; it always resolves to an IP first), so PublicURL and s.HTTP.URL
// both use "localhost" with the listener's actual port instead of the
// address Start() would otherwise report.
func New(t *testing.T, opts ...Option) *Server {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	s := &Server{T: t, DB: d, Q: q, Sessions: auth.NewSessions(q, time.Now), Mail: &Mailer{}}

	ts := httptest.NewUnstartedServer(nil)
	_, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	publicURL := "http://localhost:" + port
	cfg := config.Config{PublicURL: publicURL}

	totp := auth.NewTOTP(auth.NewSQLTOTPStore(d.Pool()), crypto.NewBox(testMasterKey), "Pulsix Test", time.Now)
	passkeys, err := auth.NewPasskeys(auth.NewSQLPasskeyStore(q), auth.NewSQLStore(q), s.Sessions, cfg.RPID(), "Pulsix Test", cfg.Origin(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// A passkey must count as a second factor at password login the same as
	// TOTP does, not only at the passkey login endpoints — otherwise a
	// passkey-only account's password alone would produce an MFA-complete
	// session (Finding 2, fix round 1).
	setupSvc := setup.NewService(setup.NewSQLStore(d.Pool()), s.Sessions, crypto.NewBox(testMasterKey), time.Now)
	s.Deps = api.Deps{
		DB: d, Sessions: s.Sessions, Auth: auth.NewService(auth.NewSQLStore(q), s.Sessions, auth.AnyEnabled(totp, passkeys)), TOTP: totp, Passkeys: passkeys,
		Tokens:  auth.NewTokens(auth.NewSQLTokenStore(q), time.Now),
		Tenancy: tenancy.NewService(tenancy.NewSQLStore(q), s.Mail, publicURL, time.Now),
		Audit:   audit.NewRecorder(audit.NewSQLStore(q), time.Now),
		Setup:   setupSvc,
		// Wired to the same setup.Service, so POST /admin/settings/smtp/test
		// exercises whatever a test actually stored through
		// PUT /admin/settings/smtp — no separate fake needed, since the
		// real *mail.SMTP simply fails to dial when nothing is configured
		// (or the test's own tests want that failure).
		Mailer: mail.NewSMTP(setupSvc.LoadSMTP),
	}
	for _, opt := range opts {
		opt(s)
	}
	handler, err := api.New(s.Deps)
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = handler
	ts.Start()
	ts.URL = publicURL
	t.Cleanup(ts.Close)
	s.HTTP = ts
	return s
}

// Client returns an HTTP client with a cookie jar.
func (s *Server) Client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// SeedUser inserts a user with a password.
func (s *Server) SeedUser(email, password string) uuid.UUID {
	s.T.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.T.Fatal(err)
	}
	id := uuid.Must(uuid.NewV7())
	if _, err := s.Q.CreateUser(context.Background(), gen.CreateUserParams{ID: id, Email: email, DisplayName: email, PasswordHash: &hash}); err != nil {
		s.T.Fatal(err)
	}
	return id
}

// SeedOrg inserts an org with ownerID as owner.
func (s *Server) SeedOrg(slug string, ownerID uuid.UUID) uuid.UUID {
	s.T.Helper()
	ctx := context.Background()
	id := uuid.Must(uuid.NewV7())
	if _, err := s.Q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: id, Slug: slug, Name: slug}); err != nil {
		s.T.Fatal(err)
	}
	if err := s.Q.AddMembership(ctx, gen.AddMembershipParams{OrgID: id, UserID: ownerID, Role: gen.OrgRoleOwner}); err != nil {
		s.T.Fatal(err)
	}
	return id
}

// AddMember adds userID to orgID with role, without going through the
// tenancy service or any invite flow — for tests that need a member at a
// specific role (not the owner SeedOrg already creates) with no interest in
// exercising ChangeRole/Invite themselves.
func (s *Server) AddMember(orgID, userID uuid.UUID, role tenancy.Role) {
	s.T.Helper()
	if err := s.Q.AddMembership(context.Background(), gen.AddMembershipParams{OrgID: orgID, UserID: userID, Role: gen.OrgRole(role)}); err != nil {
		s.T.Fatal(err)
	}
}

// LoginAs returns a client whose jar holds a fully authenticated
// (MFA-complete) session cookie.
func (s *Server) LoginAs(userID uuid.UUID) *http.Client {
	s.T.Helper()
	return s.loginAsWithMFA(userID, true)
}

// LoginAsPending returns a client whose jar holds a session cookie that has
// not completed a required second factor (mfa_complete = false). It exists
// to test requireMFAComplete's rejection of half-authenticated sessions
// without needing Task 7's TOTP flow to actually create one (Finding 8,
// fix round 1).
func (s *Server) LoginAsPending(userID uuid.UUID) *http.Client {
	s.T.Helper()
	return s.loginAsWithMFA(userID, false)
}

func (s *Server) loginAsWithMFA(userID uuid.UUID, mfaComplete bool) *http.Client {
	s.T.Helper()
	_, cookie, err := s.Sessions.Create(context.Background(), userID, mfaComplete)
	if err != nil {
		s.T.Fatal(err)
	}
	c := s.Client()
	u, _ := url.Parse(s.HTTP.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: auth.CookieName, Value: cookie}})
	return c
}
