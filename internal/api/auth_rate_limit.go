package api

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/clientip"
)

// authRateWindow is the counting window of the auth endpoints' per-address
// limit (PULSIX_AUTH_RATE_LIMIT, requests per minute per address).
const authRateWindow = time.Minute

// newAuthRateLimiter builds the per-address limiter for one auth route
// family: at most deps.AuthRateLimit failed attempts per minute, counted in
// process (the same clientip.Limiter the webhook receiver and the worker
// enrollment use). Each endpoint counts its own budget, so flooding one
// cannot starve the others; a nil limiter — deps.AuthRateLimit 0, what
// PULSIX_AUTH_RATE_LIMIT=0 asks for and what every test Deps without the
// option gets — disables the limit.
func newAuthRateLimiter(deps Deps) *clientip.Limiter {
	if deps.AuthRateLimit <= 0 {
		return nil
	}
	return clientip.NewLimiter(deps.AuthRateLimit, authRateWindow, time.Now)
}

// withAuthRateLimit is an operation modifier that attaches the per-address
// authentication rate limiter to exactly one mutating auth endpoint (login,
// TOTP verify, passkey login finish, invite accept — backlog 1.20). It
// mirrors withAudit's shape: an operation modifier setting operation
// middleware, so the limiting sits at the route registration, runs before
// the handler, and shows up on exactly the operations named here.
func withAuthRateLimit(deps Deps) func(o *huma.Operation) {
	lim := newAuthRateLimiter(deps)
	return func(o *huma.Operation) {
		o.Middlewares = append(o.Middlewares, authRateLimitMiddleware(deps, lim))
	}
}

// authRateLimitMiddleware refuses an address that has spent its failed-
// attempt budget and charges the budget once the attempt has failed:
//
//   - Before the handler: when the address is refused, answer 429 with a
//     Retry-After of the seconds until the window rolls over (ceil) and
//     never run the handler — the limiter's entire point is not spending
//     argon2id and database work on a flooded endpoint.
//   - After the handler: any 4xx response counts as one failed attempt and
//     is charged. A 2xx (the attempt succeeded) and a 5xx (the manager
//     failed, not the caller) are never charged, matching the hook
//     limiter's semantics (internal/triggers.Receive: verified deliveries
//     do not count) — counting all attempts instead would let one caller
//     lock legitimate users who share its address out of signing in.
//
// The 429 itself is not charged: it is the limiter's own answer, not a
// credential failure, and charging it would let a flood keep its window
// (and so the lockout) alive forever.
//
// The address is resolved through the same trusted-proxy rules every other
// rate limit here keys on (PULSIX_TRUSTED_PROXIES; see clientAddr), so a
// caller cannot rotate its apparent address through an untrusted proxy and
// callers behind a configured proxy are counted separately.
func authRateLimitMiddleware(deps Deps, lim *clientip.Limiter) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if lim == nil {
			next(ctx)
			return
		}
		ip := clientAddr(deps, ctx)
		if remaining, refused := lim.Refused(ip); refused {
			writeRateLimited(ctx, remaining)
			return
		}
		next(ctx)
		if s := ctx.Status(); s >= http.StatusBadRequest && s < http.StatusInternalServerError &&
			s != http.StatusTooManyRequests {
			lim.Charge(ip)
		}
	}
}

// writeRateLimited answers a refused attempt: 429 with Retry-After naming
// the seconds (ceil, at least 1) until the address's window ends and its
// budget returns. The body is written by hand rather than through
// huma.WriteErr so the answer never depends on the caller's Accept header —
// a hostile client must not be able to trade its 429 for a 406 by sending
// garbage — in exactly the shape huma's own error model produces.
func writeRateLimited(ctx huma.Context, remaining time.Duration) {
	secs := int(math.Ceil(remaining.Seconds()))
	if secs < 1 {
		secs = 1
	}
	body, _ := json.Marshal(struct {
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}{
		Title:  http.StatusText(http.StatusTooManyRequests),
		Status: http.StatusTooManyRequests,
		Detail: "too many failed attempts from this address; retry later",
	})
	ctx.SetHeader("Content-Type", "application/json")
	ctx.AppendHeader("Retry-After", strconv.Itoa(secs))
	ctx.SetStatus(http.StatusTooManyRequests)
	_, _ = ctx.BodyWriter().Write(body)
}
