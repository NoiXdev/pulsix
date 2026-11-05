package api

import (
	"encoding/json"
	"math"
	"net/http"
	"slices"
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
// authentication rate limiter to exactly one mutating auth endpoint. It is
// on login, TOTP verify, passkey login finish and invite accept (backlog
// 1.20), setup completion (backlog 7.27) and the password reset (backlog
// 7.74); every 4xx those answer is a failed attempt and keeps its slot.
// The password change uses withAuthRateLimitCharging instead, which charges
// only a wrong current password. It mirrors withAudit's shape: an operation
// modifier setting operation middleware, so the limiting sits at the route
// registration, runs before the handler, and shows up on exactly the
// operations named here.
func withAuthRateLimit(deps Deps) func(o *huma.Operation) {
	return withAuthRateLimitCharging(deps)
}

// withAuthRateLimitCharging is withAuthRateLimit for a route where only some
// refusals are guesses (backlog 7.82): when charged names statuses, only a
// response with one of them keeps its slot, and every other one is refunded.
// POST /auth/password charges only its 400 (a wrong current password, or an
// expired temporary one): a new password the policy refuses (422) or a stale
// second factor (403 step_up_required) tells the caller nothing about the
// current password, and charging those would lock someone typing a too-short
// password out of the guess the limit is there to bound. With no statuses it
// is withAuthRateLimit.
func withAuthRateLimitCharging(deps Deps, charged ...int) func(o *huma.Operation) {
	lim := newAuthRateLimiter(deps)
	return func(o *huma.Operation) {
		o.Middlewares = append(o.Middlewares, authRateLimitMiddleware(deps, lim, charged))
	}
}

// authRateLimitMiddleware refuses an address that has spent its failed-
// attempt budget:
//
//   - Before the handler: the attempt reserves one slot of the address's
//     budget atomically (clientip.Limiter.Reserve). When none is left, it
//     answers 429 with a Retry-After of the seconds until the window rolls
//     over (ceil) and never runs the handler — the limiter's entire point
//     is not spending argon2id and database work on a flooded endpoint.
//     Reserving before the handler, rather than charging after it, is what
//     stops a burst of concurrent attempts from all getting through before
//     the first failure is counted (security audit run 1, backlog 7.8).
//   - After the handler: only a 4xx response is a failed attempt and keeps
//     its slot. A 2xx (the attempt succeeded) and a 5xx (the manager
//     failed, not the caller) hand the slot back, matching the hook
//     limiter's semantics (internal/triggers.Receive: verified deliveries
//     do not count) — counting all attempts instead would let one caller
//     lock legitimate users who share its address out of signing in. While
//     a successful attempt is in flight it does hold a slot, so a burst of
//     concurrent sign-ins from one address can see a 429.
//
// A 429 is never charged. The limiter's own 429 reserved no slot (nothing to
// refund); a 429 the handler itself answered is refunded like any status not
// kept by the rule above, because it is not a credential failure and
// charging it would let a flood keep its window (and so the lockout) alive
// forever. When charged names statuses (withAuthRateLimitCharging), only
// those keep their slot and every other status, a 4xx included, is refunded.
//
// The address is resolved through the same trusted-proxy rules every other
// rate limit here keys on (PULSIX_TRUSTED_PROXIES; see clientAddr), so a
// caller cannot rotate its apparent address through an untrusted proxy and
// callers behind a configured proxy are counted separately.
func authRateLimitMiddleware(deps Deps, lim *clientip.Limiter, charged []int) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if lim == nil {
			next(ctx)
			return
		}
		ip := clientAddr(deps, ctx)
		r, remaining, ok := lim.Reserve(ip)
		if !ok {
			writeRateLimited(ctx, remaining, "too many failed attempts from this address; retry later")
			return
		}
		next(ctx)
		if !isCharged(ctx.Status(), charged) {
			lim.Refund(r)
		}
	}
}

// isCharged reports whether a response with status s is a failed attempt
// that keeps its slot: one of charged when that names any, otherwise any 4xx
// but a 429.
func isCharged(s int, charged []int) bool {
	if len(charged) > 0 {
		return slices.Contains(charged, s)
	}
	return s >= http.StatusBadRequest && s < http.StatusInternalServerError && s != http.StatusTooManyRequests
}

// writeRateLimited answers a refused attempt: 429 with Retry-After naming
// the seconds (ceil, at least 1) until the address's window ends and its
// budget returns, and detail saying what was counted. The body is written
// by hand rather than through huma.WriteErr so the answer never depends on
// the caller's Accept header — a hostile client must not be able to trade
// its 429 for a 406 by sending garbage — in exactly the shape huma's own
// error model produces.
func writeRateLimited(ctx huma.Context, remaining time.Duration, detail string) {
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
		Detail: detail,
	})
	ctx.SetHeader("Content-Type", "application/json")
	ctx.AppendHeader("Retry-After", strconv.Itoa(secs))
	ctx.SetStatus(http.StatusTooManyRequests)
	_, _ = ctx.BodyWriter().Write(body)
}
