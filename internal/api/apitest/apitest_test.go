// Package coverage note: internal/api/auth_handlers_test.go already exercises
// apitest.New through its full login/logout flow, but a plain `go test ./...`
// only credits statement coverage to the package that owns the test file
// being run, not to a helper package a test imports. Without a test file of
// its own, apitest would show 0% and fail the coverage gate even though its
// code runs on every API test. This file closes that gap directly.
package apitest_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
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
