package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey struct{}
type scopeKey struct{}

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

// TokenScopeFromContext returns the bearer token's scope set by
// BearerMiddleware, if any. It carries no meaning for a cookie session — a
// browser session is not pinned to a single org or capped at a role — so a
// handler must treat a missing scope as "unrestricted by a token" rather
// than as an error; Task 10's authorization uses this to narrow, never
// widen, what a caller may do.
func TokenScopeFromContext(ctx context.Context) (TokenScope, bool) {
	s, ok := ctx.Value(scopeKey{}).(TokenScope)
	return s, ok
}

// BearerMiddleware resolves "Authorization: Bearer pxt_…" into a Principal
// (with TokenID set and MFAComplete true — see Tokens.Resolve) and its
// TokenScope.
//
// Unlike CookieMiddleware, an Authorization header that names itself as a
// bearer credential but fails to resolve is rejected outright with 401
// rather than left for the request to continue unauthenticated: a caller
// presenting "Authorization: Bearer <garbage>" is unambiguously attempting
// bearer authentication, so silently falling through to anonymous would let
// a typo'd or revoked token be mistaken for "no credential supplied" by
// whatever route-level check runs next (requireSession would correctly
// still reject an anonymous request, but requireSession is not every
// route's only gate once Task 10 adds token-scoped authorization that reads
// TokenScopeFromContext — a route relying on that must never observe "no
// scope" when the caller's intent was plainly to authenticate with one). A
// request with no Authorization header at all, or one naming a different
// auth scheme, is untouched and falls through exactly like a request with
// no cookie.
func BearerMiddleware(t *Tokens) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cred, ok := bearerCredential(r.Header.Get("Authorization")); ok {
				p, scope, err := t.Resolve(r.Context(), cred)
				if err != nil {
					// Written by hand rather than http.Error: http.Error
					// unconditionally sets Content-Type to text/plain,
					// which would silently mislabel this JSON body.
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"title":"Unauthorized","status":401}`))
					return
				}
				ctx := WithPrincipal(r.Context(), p)
				r = r.WithContext(context.WithValue(ctx, scopeKey{}, scope))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerCredential extracts the credential from an Authorization header
// whose scheme is "Bearer". Per RFC 7235 §2.1, the scheme token is
// case-insensitive ("bearer", "BEARER", "Bearer" are all the same scheme),
// so the comparison uses strings.EqualFold rather than a literal prefix
// match; the credential itself is returned untouched (it is not a scheme
// token and is not case-insensitive). ok is false for any other scheme, an
// empty header, or a header with no space separating scheme from
// credential.
func bearerCredential(header string) (string, bool) {
	scheme, cred, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return cred, true
}
