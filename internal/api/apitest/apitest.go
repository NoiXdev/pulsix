// Package apitest spins up the full API against the test database.
package apitest

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

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

// New builds Deps from the test database and starts an httptest server.
func New(t *testing.T) *Server {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	s := &Server{T: t, DB: d, Q: q, Sessions: auth.NewSessions(q)}
	s.Deps = api.Deps{DB: d, Sessions: s.Sessions, Auth: auth.NewService(auth.NewSQLStore(q), s.Sessions, auth.NoMFA{})}
	s.HTTP = httptest.NewServer(api.New(s.Deps))
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

// LoginAs returns a client whose jar holds a fully authenticated session.
func (s *Server) LoginAs(userID uuid.UUID) *http.Client {
	s.T.Helper()
	_, cookie, err := s.Sessions.Create(context.Background(), userID, true)
	if err != nil {
		s.T.Fatal(err)
	}
	c := s.Client()
	u, _ := url.Parse(s.HTTP.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: auth.CookieName, Value: cookie}})
	return c
}
