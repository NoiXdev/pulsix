package api

import "net/http"

// The response headers every request carries (backlog 1.29). Before this
// the manager set none of them: the SPA could be framed, a browser was
// free to sniff a response's type, and a full URL — including the
// organization slug and object ids of whatever page an operator was on —
// travelled as the referrer to any third party a page reached.
//
// The content policy is strict everywhere: scripts come from this origin
// and nowhere else, which the SPA satisfies by construction (one module
// script from /assets, no inline code). The single exception is the API
// explorer, which is a third-party viewer loaded from a CDN — see
// explorerCSP.

// explorerPath is the one route whose policy differs, matched exactly:
// a prefix match would hand the exception to anything below it.
const explorerPath = "/api/explorer"

// strictCSP is the policy for the SPA, the API and every static asset.
//
// style-src admits 'unsafe-inline' deliberately: the SPA sets style
// attributes (a status dot's colour, a progress width), which CSP counts
// as inline styles. The risk that buys is a styling trick, not code
// execution — script-src stays pinned to this origin, which is what
// actually matters.
const strictCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'"

// explorerCSP is the API explorer's policy. The page is a single script
// tag loading the Scalar viewer from jsdelivr, pinned by subresource
// integrity (see api.go), and the viewer injects its own styles and
// fetches fonts and images at runtime.
//
// What it does NOT add is 'unsafe-inline' or 'unsafe-eval' for scripts.
// The page's own `<script id="api-reference">` element is empty — the
// viewer reads its data-url attribute, it has no code to run — so blocking
// its execution costs nothing but a console entry, and paying that is
// better than opening inline scripts on a same-origin page that carries
// the session cookie.
const explorerCSP = "default-src 'self'; " +
	"script-src 'self' https://cdn.jsdelivr.net; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https:; " +
	"font-src 'self' data: https://cdn.jsdelivr.net; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'"

// permissionsPolicy switches off the browser features this application
// never uses. Naming them explicitly is what keeps a future dependency
// from quietly reaching for one.
const permissionsPolicy = "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()"

// securityHeaders sets the headers on every response, before the handler
// runs, so they are present on error answers too — a 404 or a 500 is
// exactly as framable as a 200.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		csp := strictCSP
		if r.URL.Path == explorerPath {
			csp = explorerCSP
		}
		h.Set("Content-Security-Policy", csp)
		// X-Frame-Options alongside frame-ancestors: the CSP directive is
		// the one that counts in current browsers, this one is what an
		// older or embedded engine understands.
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", permissionsPolicy)
		next.ServeHTTP(w, r)
	})
}
