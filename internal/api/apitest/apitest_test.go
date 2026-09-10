// Package coverage note: internal/api/auth_handlers_test.go already exercises
// apitest.New through its full login/logout flow, but a plain `go test ./...`
// only credits statement coverage to the package that owns the test file
// being run, not to a helper package a test imports. Without a test file of
// its own, apitest would show 0% and fail the coverage gate even though its
// code runs on every API test. This file closes that gap directly.
package apitest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/auth"
)

func TestServerHelpers(t *testing.T) {
	s := apitest.New(t)
	if s.DB == nil || s.Q == nil || s.Sessions == nil || s.HTTP == nil {
		t.Fatalf("New left a field unset: %+v", s)
	}

	userID := s.SeedUser("helper@example.test", "correct horse battery")
	if userID == uuid.Nil {
		t.Fatal("SeedUser returned a nil UUID")
	}

	orgID := s.SeedOrg("helper-org", userID)
	if orgID == uuid.Nil {
		t.Fatal("SeedOrg returned a nil UUID")
	}

	client := s.Client()
	if client.Jar == nil {
		t.Fatal("Client() returned a client without a cookie jar")
	}

	authed := s.LoginAs(userID)
	resp, err := authed.Get(s.HTTP.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("LoginAs did not produce a valid session: status %d", resp.StatusCode)
	}
}

// TestLoginAsPending exercises the Option added in fix round 1 for Finding
// 8: a session created without a completed second factor.
func TestLoginAsPending(t *testing.T) {
	s := apitest.New(t)
	userID := s.SeedUser("pending-helper@example.test", "correct horse battery")

	pending := s.LoginAsPending(userID)
	resp, err := pending.Get(s.HTTP.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("LoginAsPending did not produce a session reachable by requireSession: status %d", resp.StatusCode)
	}
}

// TestWithSecureCookies and TestWithAuthWrapper exercise the two apitest
// Options added in fix round 1 (Findings 3 and 1/6): both must actually
// change the Deps a Server is built with, not just compile.
func TestWithSecureCookies(t *testing.T) {
	s := apitest.New(t, apitest.WithSecureCookies())
	if !s.Deps.Secure {
		t.Fatal("WithSecureCookies did not set Deps.Secure")
	}
}

func TestWithAuthWrapper(t *testing.T) {
	sentinel := errors.New("wrapped logout failure")
	s := apitest.New(t, apitest.WithAuthWrapper(func(a api.AuthService) api.AuthService {
		return failingLogoutAuth{AuthService: a, err: sentinel}
	}))
	if err := s.Deps.Auth.Logout(context.Background(), auth.Principal{}); !errors.Is(err, sentinel) {
		t.Fatalf("Deps.Auth.Logout = %v, want the wrapper's sentinel error", err)
	}
}

type failingLogoutAuth struct {
	api.AuthService
	err error
}

func (f failingLogoutAuth) Logout(context.Context, auth.Principal) error { return f.err }
