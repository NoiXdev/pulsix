package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// errLeaky is an internal error whose text must never reach a client.
var errLeaky = errors.New("pq: connection to 10.0.0.5:5432 refused (secret detail)")

// failingTOTPStore fails every read with errLeaky.
type failingTOTPStore struct{ auth.TOTPStore }

func (failingTOTPStore) GetTOTPSecret(context.Context, uuid.UUID) (auth.TOTPSecretRow, error) {
	return auth.TOTPSecretRow{}, errLeaky
}

// meOnlyAuth answers Me for any principal.
type meOnlyAuth struct{ AuthService }

func (meOnlyAuth) Me(_ context.Context, p auth.Principal) (auth.User, error) {
	return auth.User{ID: p.UserID, Email: "me@example.test", DisplayName: "Me"}, nil
}

// TestAuthMeDoesNotLeakInternalErrors covers M1: GET /auth/me used to return
// the TOTP store's raw error, which huma serialises into the response body.
// It now goes through mapAuthErr like every other handler — logged, and
// answered with a generic 500.
func TestAuthMeDoesNotLeakInternalErrors(t *testing.T) {
	r := chi.NewRouter()
	hapi := humachi.New(r, huma.DefaultConfig("me-errors-test", "0.0.0"))
	registerAuthRoutes(hapi, Deps{
		Auth: meOnlyAuth{},
		TOTP: auth.NewTOTP(failingTOTPStore{}, nil, "test", time.Now),
	})
	api := humatest.Wrap(t, hapi)

	rec := api.DoCtx(ctxWithPrincipal(uuid.Must(uuid.NewV7()), true), http.MethodGet, "/api/v1/auth/me")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "10.0.0.5") || strings.Contains(body, "secret detail") {
		t.Fatalf("response body leaks the internal error: %s", body)
	}
}
