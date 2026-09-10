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
	"github.com/noixdev/pulsix/internal/tenancy"
)

// testTenancy builds a *tenancy.Service with a nil store, good enough to
// satisfy api.Deps.validate in tests that never actually drive an
// organization operation (nil interface values are only a problem once
// called), mirroring testPasskeys below.
func testTenancy() *tenancy.Service {
	return tenancy.NewService(nil, nil, "http://localhost", time.Now)
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

func TestHealthz(t *testing.T) {
	h, err := api.New(api.Deps{
		Sessions: auth.NewSessions(nil, time.Now), Auth: fakeAuthService{}, TOTP: auth.NewTOTP(nil, nil, "test", time.Now),
		Passkeys: testPasskeys(t), Tokens: auth.NewTokens(nil, time.Now), Tenancy: testTenancy(),
	})
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
}
