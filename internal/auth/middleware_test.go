package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestWithPrincipalFromContext(t *testing.T) {
	p := auth.Principal{UserID: uuid.Must(uuid.NewV7())}
	ctx := auth.WithPrincipal(context.Background(), p)
	got, ok := auth.FromContext(ctx)
	if !ok || got != p {
		t.Fatalf("FromContext = %+v, %v, want %+v, true", got, ok, p)
	}

	if _, ok := auth.FromContext(context.Background()); ok {
		t.Fatal("FromContext on a bare context reported a principal")
	}
}

func TestCookieMiddleware(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "mw@example.test", DisplayName: "MW"}); err != nil {
		t.Fatal(err)
	}
	s := auth.NewSessions(q, time.Now)
	_, cookie, err := s.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}

	var seenPrincipal auth.Principal
	var seenOK bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPrincipal, seenOK = auth.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler := auth.CookieMiddleware(s)(next)

	t.Run("valid cookie sets the principal", func(t *testing.T) {
		seenOK = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if !seenOK || seenPrincipal.UserID != uid {
			t.Fatalf("principal = %+v, ok = %v", seenPrincipal, seenOK)
		}
	})

	t.Run("no cookie leaves the request unauthenticated but not rejected", func(t *testing.T) {
		seenOK = true
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if seenOK {
			t.Fatal("principal set without a cookie")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (middleware must not reject)", rec.Code)
		}
	})

	t.Run("invalid cookie leaves the request unauthenticated but not rejected", func(t *testing.T) {
		seenOK = true
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "not-a-session"})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if seenOK {
			t.Fatal("principal set from an invalid cookie")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (middleware must not reject)", rec.Code)
		}
	})
}

// fakeBearerStore is a minimal auth.TokenStore that resolves exactly one
// known secret hash, so BearerMiddleware can be exercised without a
// database.
type fakeBearerStore struct {
	hash   []byte
	tok    auth.Token
	admin  bool
	getErr error
}

func (f *fakeBearerStore) Insert(context.Context, auth.Token, []byte) error { return nil }

func (f *fakeBearerStore) GetByHash(_ context.Context, hash []byte, _ time.Time) (auth.Token, bool, error) {
	if f.getErr != nil {
		return auth.Token{}, false, f.getErr
	}
	if string(hash) != string(f.hash) {
		return auth.Token{}, false, auth.ErrNotFound
	}
	return f.tok, f.admin, nil
}

func (f *fakeBearerStore) Usable(context.Context, uuid.UUID, time.Time) (bool, error) {
	return true, nil
}

func (f *fakeBearerStore) ListForOrg(context.Context, uuid.UUID) ([]auth.Token, error) {
	return nil, nil
}
func (f *fakeBearerStore) Revoke(context.Context, uuid.UUID, uuid.UUID) error        { return nil }
func (f *fakeBearerStore) TouchLastUsed(context.Context, uuid.UUID, time.Time) error { return nil }

func TestBearerMiddleware(t *testing.T) {
	store := &fakeBearerStore{}
	tk := auth.NewTokens(store, time.Now)
	uid, org, tokID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	plain, _, err := tk.Create(context.Background(), uid, org, "ci", "operator", nil)
	if err != nil {
		t.Fatal(err)
	}
	// fakeBearerStore.Insert is a deliberate no-op (this test only needs
	// GetByHash to resolve one fixed token), so point the fixture at the
	// hash Create actually produced instead of relying on a real write.
	sum := sha256.Sum256(mustDecodeToken(t, plain))
	store.hash = sum[:]
	store.tok = auth.Token{ID: tokID, UserID: uid, OrgID: org, Name: "ci", Role: "operator"}

	var seenPrincipal auth.Principal
	var seenOK, nextCalled bool
	var seenScope auth.TokenScope
	var seenScopeOK bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		seenPrincipal, seenOK = auth.FromContext(r.Context())
		seenScope, seenScopeOK = auth.TokenScopeFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler := auth.BearerMiddleware(tk)(next)

	t.Run("valid bearer token sets the principal and scope", func(t *testing.T) {
		seenOK, seenScopeOK = false, false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+plain)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if !seenOK || seenPrincipal.UserID != uid || seenPrincipal.TokenID == nil || *seenPrincipal.TokenID != tokID || !seenPrincipal.MFAComplete {
			t.Fatalf("principal = %+v, ok = %v", seenPrincipal, seenOK)
		}
		if !seenScopeOK || seenScope.OrgID != org || seenScope.Role != "operator" {
			t.Fatalf("scope = %+v, ok = %v", seenScope, seenScopeOK)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("no Authorization header leaves the request unauthenticated but not rejected", func(t *testing.T) {
		seenOK = true
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if seenOK {
			t.Fatal("principal set without an Authorization header")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (middleware must not reject an absent header)", rec.Code)
		}
	})

	t.Run("non-bearer Authorization header falls through untouched", func(t *testing.T) {
		seenOK = true
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if seenOK {
			t.Fatal("principal set from a non-bearer scheme")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("invalid bearer token is rejected outright, not left anonymous", func(t *testing.T) {
		nextCalled = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer pxt_not-a-real-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if nextCalled {
			t.Fatal("next handler was called for an invalid bearer token")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (must reject outright, not fall through as anonymous)", rec.Code)
		}
	})

	t.Run("a genuine store failure is also rejected, not treated as anonymous", func(t *testing.T) {
		store.getErr = errors.New("store unavailable")
		defer func() { store.getErr = nil }()
		nextCalled = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+plain)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if nextCalled {
			t.Fatal("next handler was called despite a store failure")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}

// mustDecodeToken extracts the raw secret bytes from a "pxt_..." plaintext,
// duplicating just enough of Tokens.Resolve's own decoding to build a test
// fixture, without exporting that logic from the package under test.
func mustDecodeToken(t *testing.T, plain string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(plain, "pxt_"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
