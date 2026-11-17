package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// TestPersonalTokenRoutesRefuseBearer: a person's own tokens are managed
// from their browser session only — a bearer token (403), a locked session
// (403) and a session without its second factor (403) are all refused
// before any service is reached (Deps is empty here).
func TestPersonalTokenRoutesRefuseBearer(t *testing.T) {
	r := chi.NewRouter()
	hapi := humachi.New(r, huma.DefaultConfig("personal-token-test", "0.0.0"))
	registerPersonalTokenRoutes(hapi, Deps{})
	api := humatest.Wrap(t, hapi)

	user := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7()).String()
	locked := auth.WithPrincipal(context.Background(), auth.Principal{UserID: user, MFAComplete: true, Locked: true})
	grants := []map[string]any{{"org": "acme", "role": "viewer"}}
	for name, ctx := range map[string]context.Context{
		"token":          ctxWithTokenPrincipal(user),
		"locked session": locked,
		"pending factor": ctxWithPrincipal(user, false),
	} {
		for _, call := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, "/api/v1/auth/me/tokens", nil},
			{http.MethodPost, "/api/v1/auth/me/tokens", map[string]any{"name": "x", "grants": grants}},
			{http.MethodPatch, "/api/v1/auth/me/tokens/" + id, map[string]any{"name": "x"}},
			{http.MethodDelete, "/api/v1/auth/me/tokens/" + id, nil},
		} {
			if rec := doOptional(api, ctx, call.method, call.path, call.body); rec.Code != http.StatusForbidden {
				t.Errorf("%s: %s %s = %d (%s), want 403", name, call.method, call.path, rec.Code, rec.Body.String())
			}
		}
	}
}
