package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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
