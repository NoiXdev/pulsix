package api

import (
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/clientip"
)

// The forgot route's per-address limit: forgotRateLimit requests per
// forgotRateWindow from one client address.
const (
	forgotRateLimit  = 10
	forgotRateWindow = 15 * time.Minute
)

// withForgotRateLimit is the operation modifier of POST
// /auth/password/forgot: a per-client-address limit that counts every
// request. The auth limiter (withAuthRateLimit) charges only refused
// attempts, and this route answers 202 to every well-formed request
// whatever it asked for, so that limiter would never charge it. The limit
// is fixed rather than tied to PULSIX_AUTH_RATE_LIMIT: each request may
// send a mail, and the per-address limit in the accounts service only
// bounds the mails one account gets, not how many addresses one client
// tries.
//
// The address is resolved as every other limit here resolves it
// (clientAddr: the trusted-proxy rules of PULSIX_TRUSTED_PROXIES). Beyond
// the limit it answers 429 with Retry-After and never runs the handler.
func withForgotRateLimit(deps Deps) func(o *huma.Operation) {
	lim := clientip.NewLimiter(forgotRateLimit, forgotRateWindow, time.Now)
	return func(o *huma.Operation) {
		o.Middlewares = append(o.Middlewares, func(ctx huma.Context, next func(huma.Context)) {
			if _, remaining, ok := lim.Reserve(clientAddr(deps, ctx)); !ok {
				writeRateLimited(ctx, remaining, "too many requests from this address; retry later")
				return
			}
			next(ctx)
		})
	}
}
