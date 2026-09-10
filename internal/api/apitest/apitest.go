// Package apitest spins up the full API against the test database.
package apitest

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// Server is a running API plus helpers.
type Server struct {
	T        *testing.T
	DB       *db.DB
	Q        *gen.Queries
	Sessions *auth.Sessions
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

// New builds Deps from the test database and starts an httptest server.
func New(t *testing.T, opts ...Option) *Server {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	s := &Server{T: t, DB: d, Q: q, Sessions: auth.NewSessions(q, time.Now)}
	s.Deps = api.Deps{DB: d, Sessions: s.Sessions, Auth: auth.NewService(auth.NewSQLStore(q), s.Sessions, auth.NoMFA{})}
	for _, opt := range opts {
		opt(s)
	}
	handler, err := api.New(s.Deps)
	if err != nil {
		t.Fatal(err)
	}
	s.HTTP = httptest.NewServer(handler)
	t.Cleanup(s.HTTP.Close)
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
