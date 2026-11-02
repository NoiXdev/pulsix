package api

import "net/http"

// crossOriginProtection refuses a browser's state-changing request that
// comes from another origin (backlog 7.38). The session cookie is
// SameSite=Lax, which keeps it off requests from other sites but not from
// another origin of the same site — a sibling subdomain — and huma parses
// a body with no Content-Type as JSON, so such a page could drive any
// cookie-authenticated mutation with a plain form post or fetch.
//
// net/http's CrossOriginProtection decides by Sec-Fetch-Site, which every
// current browser sends, and without it by an Origin whose host is not
// the request's own. Safe methods pass, and so does a request carrying
// neither header — every non-browser client: API tokens, webhook senders,
// the agent and worker installers. Bearer-authenticated browser calls
// from another origin were never possible (the API answers no CORS), so
// nothing legitimate changes.
func crossOriginProtection(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"title":"Forbidden","status":403,"detail":"cross-origin request refused"}`))
	}))
	return cop.Handler(next)
}
