package api

import (
	"context"
	"encoding/json"
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

// fakeTokenStore is a minimal auth.TokenStore good enough to drive
// registerTokenRoutes without a database — this file is a white-box test
// (package api, not api_test) specifically so it can call the unexported
// registerTokenRoutes directly. It cannot use the apitest package for that
// same reason: apitest imports api, so a file inside package api importing
// apitest would be an import cycle.
type fakeTokenStore struct {
	byID map[uuid.UUID]auth.Token
}

func newFakeTokenStore() *fakeTokenStore { return &fakeTokenStore{byID: map[uuid.UUID]auth.Token{}} }

func (f *fakeTokenStore) Insert(_ context.Context, t auth.Token, _ []byte) error {
	f.byID[t.ID] = t
	return nil
}

func (f *fakeTokenStore) GetByHash(context.Context, []byte, time.Time) (auth.Token, bool, error) {
	return auth.Token{}, false, auth.ErrNotFound // unused by these routes
}

func (f *fakeTokenStore) ListForOrg(_ context.Context, orgID uuid.UUID) ([]auth.Token, error) {
	var out []auth.Token
	for _, t := range f.byID {
		if t.OrgID == orgID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeTokenStore) Revoke(_ context.Context, orgID, id uuid.UUID) error {
	t, ok := f.byID[id]
	if !ok || t.OrgID != orgID {
		return auth.ErrNotFound
	}
	delete(f.byID, id)
	return nil
}

func (f *fakeTokenStore) TouchLastUsed(context.Context, uuid.UUID, time.Time) error { return nil }

// newTokenTestAPI mounts only registerTokenRoutes on a bare chi router —
// deliberately not api.New's full router, so this exercises the handlers'
// own logic without going through CookieMiddleware, BearerMiddleware, or
// (most importantly) any org-membership authorization, none of which exist
// for these routes until Task 10 mounts them for real (see
// registerTokenRoutes' doc comment). A principal is injected straight into
// the request context via ctxWithPrincipal, standing in for whatever
// middleware chain Task 10 will put in front of these routes.
func newTokenTestAPI(t *testing.T, deps Deps) humatest.TestAPI {
	t.Helper()
	r := chi.NewRouter()
	hapi := humachi.New(r, huma.DefaultConfig("token-routes-test", "0.0.0"))
	registerTokenRoutes(hapi, deps)
	return humatest.Wrap(t, hapi)
}

func ctxWithPrincipal(userID uuid.UUID, mfaComplete bool) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{UserID: userID, MFAComplete: mfaComplete})
}

func TestRegisterTokenRoutesCreateListRevoke(t *testing.T) {
	store := newFakeTokenStore()
	deps := Deps{Tokens: auth.NewTokens(store, time.Now)}
	api := newTokenTestAPI(t, deps)
	uid, org := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	ctx := ctxWithPrincipal(uid, true)

	rec := api.PostCtx(ctx, "/api/v1/orgs/"+org.String()+"/tokens", map[string]any{"name": "ci", "role": "operator"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, want 201, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Token     string     `json:"token"`
		ID        string     `json:"id"`
		Name      string     `json:"name"`
		Role      string     `json:"role"`
		ExpiresAt *time.Time `json:"expires_at"`
		CreatedAt time.Time  `json:"created_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "pxt_") || created.ID == "" || created.Name != "ci" || created.Role != "operator" {
		t.Fatalf("create response = %+v", created)
	}

	// The list response must carry the token's metadata but never the
	// plaintext or anything that could reproduce it — assert this on the
	// raw JSON keys, not just a Go struct with no Token field (which would
	// pass even if the handler accidentally serialized an extra key a
	// stricter struct silently dropped).
	rec = api.GetCtx(ctx, "/api/v1/orgs/"+org.String()+"/tokens")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d, body %s", rec.Code, rec.Body.String())
	}
	var raw struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Tokens) != 1 {
		t.Fatalf("list = %+v, want exactly one token", raw.Tokens)
	}
	if _, leaked := raw.Tokens[0]["token"]; leaked {
		t.Fatalf("list response leaked the plaintext token: %+v", raw.Tokens[0])
	}
	if raw.Tokens[0]["id"] != created.ID {
		t.Fatalf("listed id = %v, want %v", raw.Tokens[0]["id"], created.ID)
	}

	rec = api.DeleteCtx(ctx, "/api/v1/orgs/"+org.String()+"/tokens/"+created.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: status %d, body %s", rec.Code, rec.Body.String())
	}

	rec = api.GetCtx(ctx, "/api/v1/orgs/"+org.String()+"/tokens")
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Tokens) != 0 {
		t.Fatalf("list after revoke = %+v, want empty", raw.Tokens)
	}
}

func TestRegisterTokenRoutesRequireMFAComplete(t *testing.T) {
	deps := Deps{Tokens: auth.NewTokens(newFakeTokenStore(), time.Now)}
	api := newTokenTestAPI(t, deps)
	org := uuid.Must(uuid.NewV7())

	rec := api.PostCtx(context.Background(), "/api/v1/orgs/"+org.String()+"/tokens", map[string]any{"name": "ci", "role": "operator"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("create with no principal: status %d, want 401", rec.Code)
	}

	pending := ctxWithPrincipal(uuid.Must(uuid.NewV7()), false)
	rec = api.PostCtx(pending, "/api/v1/orgs/"+org.String()+"/tokens", map[string]any{"name": "ci", "role": "operator"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create with a pending session: status %d, want 403", rec.Code)
	}
}

func TestRegisterTokenRoutesCreateRejectsInvalidRole(t *testing.T) {
	deps := Deps{Tokens: auth.NewTokens(newFakeTokenStore(), time.Now)}
	api := newTokenTestAPI(t, deps)
	ctx := ctxWithPrincipal(uuid.Must(uuid.NewV7()), true)
	org := uuid.Must(uuid.NewV7())

	rec := api.PostCtx(ctx, "/api/v1/orgs/"+org.String()+"/tokens", map[string]any{"name": "ci", "role": "superuser"})
	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("create with an invalid role: status %d, want 400 or 422, body %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterTokenRoutesRejectMalformedIDs(t *testing.T) {
	deps := Deps{Tokens: auth.NewTokens(newFakeTokenStore(), time.Now)}
	api := newTokenTestAPI(t, deps)
	ctx := ctxWithPrincipal(uuid.Must(uuid.NewV7()), true)

	if rec := api.PostCtx(ctx, "/api/v1/orgs/not-a-uuid/tokens", map[string]any{"name": "ci", "role": "operator"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("create with a malformed org id: status %d, want 400", rec.Code)
	}
	if rec := api.GetCtx(ctx, "/api/v1/orgs/not-a-uuid/tokens"); rec.Code != http.StatusBadRequest {
		t.Fatalf("list with a malformed org id: status %d, want 400", rec.Code)
	}
	org := uuid.Must(uuid.NewV7())
	if rec := api.DeleteCtx(ctx, "/api/v1/orgs/"+org.String()+"/tokens/not-a-uuid"); rec.Code != http.StatusBadRequest {
		t.Fatalf("revoke with a malformed token id: status %d, want 400", rec.Code)
	}
	if rec := api.DeleteCtx(ctx, "/api/v1/orgs/not-a-uuid/tokens/"+org.String()); rec.Code != http.StatusBadRequest {
		t.Fatalf("revoke with a malformed org id: status %d, want 400", rec.Code)
	}
}

// failingInsertTokenStore wraps a working fakeTokenStore but forces Insert
// to fail with an arbitrary, unmapped error, so TestRegisterTokenRoutesCreateUnmappedErrorReturns500
// can drive mapTokenErr's fallback to mapAuthErr without a real database.
type failingInsertTokenStore struct {
	*fakeTokenStore
	err error
}

func (f failingInsertTokenStore) Insert(context.Context, auth.Token, []byte) error { return f.err }

// TestRegisterTokenRoutesCreateUnmappedErrorReturns500 covers mapTokenErr's
// fallback branch: an error that is not auth.ErrNotFound must go through
// mapAuthErr unchanged, which — for anything it doesn't specifically
// recognize — becomes a generic 500 rather than leaking the underlying
// detail, the same guarantee TestUnmappedAuthErrorDoesNotLeakDetail asserts
// for the login routes.
func TestRegisterTokenRoutesCreateUnmappedErrorReturns500(t *testing.T) {
	store := failingInsertTokenStore{fakeTokenStore: newFakeTokenStore(), err: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")}
	deps := Deps{Tokens: auth.NewTokens(store, time.Now)}
	api := newTokenTestAPI(t, deps)
	ctx := ctxWithPrincipal(uuid.Must(uuid.NewV7()), true)
	org := uuid.Must(uuid.NewV7())

	rec := api.PostCtx(ctx, "/api/v1/orgs/"+org.String()+"/tokens", map[string]any{"name": "ci", "role": "operator"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("create with a store failure: status %d, want 500, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("response leaked internal detail: %s", rec.Body.String())
	}
}

// TestRegisterTokenRoutesRevokeUnknownReturns404 asserts mapTokenErr's
// override: an unknown (or wrong-org) token id is reported as 404, not the
// generic 401 mapAuthErr gives auth.ErrNotFound everywhere else.
func TestRegisterTokenRoutesRevokeUnknownReturns404(t *testing.T) {
	deps := Deps{Tokens: auth.NewTokens(newFakeTokenStore(), time.Now)}
	api := newTokenTestAPI(t, deps)
	ctx := ctxWithPrincipal(uuid.Must(uuid.NewV7()), true)
	org, id := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	rec := api.DeleteCtx(ctx, "/api/v1/orgs/"+org.String()+"/tokens/"+id.String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown: status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}
