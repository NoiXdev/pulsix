package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/auth"
)

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

func TestHealthz(t *testing.T) {
	h, err := api.New(api.Deps{Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now)})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ok" {
		t.Fatalf("status field = %q, want ok", body.Status)
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
}
