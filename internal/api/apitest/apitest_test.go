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
	"github.com/noixdev/pulsix/internal/tenancy"
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

	other := s.SeedUser("helper-member@example.test", "correct horse battery")
	s.AddMember(orgID, other, tenancy.RoleViewer)
	role, err := s.Deps.Tenancy.MembershipOf(context.Background(), orgID, other)
	if err != nil || role != tenancy.RoleViewer {
		t.Fatalf("AddMember did not add a viewer membership: role=%s err=%v", role, err)
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

// TestMailerRecordsSentMessages exercises Mailer.Send/Sent directly — the
// path every invite-mail test in internal/api drives indirectly through
// tenancy.Service.Invite, but (per this file's own package-level comment)
// that only credits the api package's own coverage, not apitest's.
func TestMailerRecordsSentMessages(t *testing.T) {
	m := &apitest.Mailer{}
	if got := m.Sent(); len(got) != 0 {
		t.Fatalf("Sent() before any Send = %+v, want empty", got)
	}
	if err := m.Send(context.Background(), "to@example.test", "subject", "body"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := m.Sent()
	if len(sent) != 1 || sent[0] != (apitest.SentMail{To: "to@example.test", Subject: "subject", Body: "body"}) {
		t.Fatalf("Sent() = %+v", sent)
	}
	// Sent returns a copy: mutating it must not affect the recorder's own
	// state, matching the same defensive-copy contract a caller would
	// expect from any accessor over shared, mutex-guarded state.
	sent[0].To = "mutated"
	if again := m.Sent(); again[0].To != "to@example.test" {
		t.Fatalf("Sent() leaked its internal slice: %+v", again)
	}
}
