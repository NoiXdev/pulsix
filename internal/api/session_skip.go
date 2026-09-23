package api

import (
	"net/http"
	"strings"
)

// staticSessionSkip wraps the session-cookie middleware with the
// static-asset short-circuit of backlog 1.22: a request whose path can
// never consult the session principal skips the middleware — and with it
// the GetSession database read — entirely. The session cookie rides every
// request the browser makes, including the SPA's static assets, so without
// this every asset fetch paid a session lookup the response never used.
//
// The route set that skips is every path outside the API subtree — exactly
// the requests that end up at the router's NotFound fallback,
// web.Handler (internal/api/api.go): the SPA's client-side routes (/,
// /login, /o/{org}/hosts, ...), its embedded assets (/assets/*,
// /robots.txt, any other embedded file, and any non-API path that 404s),
// plus /healthz, whose handler reads no principal. The principal the
// session cookie resolves to is read exclusively under /api — every
// handler operation lives under /api/v1, and requireMFAForExplorer gates
// /api/explorer and /api/openapi — so no authenticated route is ever
// affected. An unmatched /api path (which web.Handler 404s) still pays the
// lookup on purpose: the decision is a single conservative prefix check
// rather than a second, drift-prone copy of chi's routing table.
func staticSessionSkip(sessionMW func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		sessioned := sessionMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skipsSessionLookup(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			sessioned.ServeHTTP(w, r)
		})
	}
}

// skipsSessionLookup reports whether serving a request for path never
// consults the session-cookie principal: the lookup is worth its database
// read only under /api (see staticSessionSkip for the route set).
func skipsSessionLookup(path string) bool {
	return !strings.HasPrefix(path, "/api")
}
