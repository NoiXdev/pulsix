package auth

import (
	"context"
	"net/http"
)

type ctxKey struct{}

// CookieName is the session cookie.
const CookieName = "pulsix_session"

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal set by a middleware.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// CookieMiddleware resolves the session cookie into a Principal when valid.
func CookieMiddleware(s *Sessions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie(CookieName); err == nil {
				if p, err := s.Lookup(r.Context(), c.Value); err == nil {
					r = r.WithContext(WithPrincipal(r.Context(), p))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
