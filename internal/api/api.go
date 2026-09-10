// Package api builds the HTTP handler for the Pulsix manager.
package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// Deps carries everything the handlers need. Handlers call services only.
type Deps struct {
	DB       *db.DB
	Sessions *auth.Sessions
	Auth     AuthService
	TOTP     *auth.TOTP
	Passkeys *auth.Passkeys
	Tokens   *auth.Tokens
	Tenancy  *tenancy.Service
	Secure   bool // set Secure on cookies (true behind HTTPS)
}

// scalarVersion and scalarIntegrity pin the third-party script the API
// explorer loads. Both were captured by fetching the published file for
// this exact version and hashing it, not invented:
//
//	curl -s https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.68.0 | openssl dgst -sha384 -binary | openssl base64 -A
//
// jsdelivr serves npm packages with an immutable, version-pinned URL, so
// this hash will keep matching that URL forever; bumping scalarVersion
// requires recomputing scalarIntegrity the same way.
const (
	scalarVersion   = "1.68.0"
	scalarIntegrity = "sha384-ayGz8N+NChlUEfR0zr5Zy3T6Q4lhcdiASJNoshS6+vxV56ZE300qfWNBjj9pqsLN"
)

type healthOutput struct {
	Body struct {
		Status string `json:"status" example:"ok"`
	}
}

// validate reports which required fields are missing. The auth routes are
// registered unconditionally below and dereference Sessions/Auth on every
// request, so a Deps that omits either must fail loudly at construction
// time instead of turning into a nil-pointer panic (recovered into an
// opaque 500) on the first real request.
func (d Deps) validate() error {
	var missing []string
	if d.Sessions == nil {
		missing = append(missing, "Sessions")
	}
	if d.Auth == nil {
		missing = append(missing, "Auth")
	}
	if d.TOTP == nil {
		missing = append(missing, "TOTP")
	}
	if d.Passkeys == nil {
		missing = append(missing, "Passkeys")
	}
	if d.Tokens == nil {
		missing = append(missing, "Tokens")
	}
	if d.Tenancy == nil {
		missing = append(missing, "Tenancy")
	}
	if len(missing) > 0 {
		return errors.New("api.New: Deps missing required field(s): " + strings.Join(missing, ", "))
	}
	return nil
}

// New returns the root http.Handler: chi router + huma API under /api/v1.
// It refuses to build a handler for a half-wired Deps rather than serve
// routes that cannot work; see Deps.validate.
func New(deps Deps) (http.Handler, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	// middleware.RealIP is intentionally omitted: it is deprecated in this
	// chi version because it mutates RemoteAddr from client-controlled
	// headers without a trusted-proxy allowlist (IP spoofing risk). A
	// later task can add a spoof-safe replacement once the deployment's
	// reverse-proxy trust boundary is known.
	r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)
	r.Use(auth.CookieMiddleware(deps.Sessions))
	// auth.BearerMiddleware is deliberately NOT mounted here (Finding 1, fix
	// round 1; carried forward from Task 9). Mounting it on this router —
	// the router every route in this package attaches to — would make a
	// bearer principal (which Tokens.Resolve always marks MFAComplete:
	// true, by construction) reach every route mounted below, including the
	// account-security routes (passkey register/list/delete, TOTP
	// enrol/confirm/delete) that only gate on requireMFAComplete: a
	// viewer-scoped token could register an attacker-controlled passkey or
	// plant a TOTP secret on its owner's account and take it over
	// completely. Those routes use requireInteractiveSession
	// (auth_handlers.go), which rejects a token principal outright, so they
	// stay safe regardless — but nothing about this router-level mount
	// would be scoped to only the routes that actually need a bearer
	// credential.
	//
	// Task 10 mounts BearerMiddleware instead on a *huma.Group scoped to
	// exactly the org-scoped routes that consult
	// auth.TokenScopeFromContext (via requireOrg, authz.go) — organizations,
	// members, invites and tokens under /api/v1/orgs/{org}/... — see
	// registerRoutes below and bearerAuthMiddleware (org_handlers.go) for
	// how a *huma.Group achieves per-operation scoping without a second chi
	// router.
	r.Use(requireMFAForExplorer)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	r.Get("/api/explorer", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Pulsix API</title></head><body>
<script id="api-reference" data-url="/api/openapi.json"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@` + scalarVersion + `"
        integrity="` + scalarIntegrity + `"
        crossorigin="anonymous"></script></body></html>`))
	})

	cfg := huma.DefaultConfig("Pulsix API", "0.1.0")
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = "" // explorer is mounted above, served by Scalar instead of huma's default docs
	hapi := humachi.New(r, cfg)
	registerRoutes(hapi, deps)
	return r, nil
}

// requireMFAForExplorer gates the API explorer page and the OpenAPI spec it
// loads behind an MFA-complete session. Both sit on the same origin as the
// session cookie and, unauthenticated, hand any caller the full shape of
// the API (and, via the explorer, a working request console) — so they use
// the same requireMFAComplete check as everything except the small set of
// routes (logout, me, totp/verify, passkey login) that must stay reachable
// to a half-authenticated session. This runs as router-level middleware,
// not a huma operation, because the OpenAPI spec route is registered
// internally by huma/humachi with no handler of ours to wrap.
func requireMFAForExplorer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/explorer" || strings.HasPrefix(r.URL.Path, "/api/openapi") {
			if _, err := requireMFAComplete(r.Context()); err != nil {
				status := http.StatusUnauthorized
				var se huma.StatusError
				if errors.As(err, &se) {
					status = se.GetStatus()
				}
				http.Error(w, "login required", status)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// registerRoutes is extended by later tasks (one register* call per resource).
func registerRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/health", func(ctx context.Context, _ *struct{}) (*healthOutput, error) {
		out := &healthOutput{}
		out.Body.Status = "ok"
		return out, nil
	})
	registerAuthRoutes(hapi, deps)
	registerMFARoutes(hapi, deps)
	registerPasskeyRoutes(hapi, deps)
	registerOrgRoutes(hapi, deps)         // POST/GET /orgs — no org in the path, no bearer credential accepted
	registerInviteAcceptRoute(hapi, deps) // POST /invites/accept — public, no principal at all
	registerAdminRoutes(hapi, deps)       // POST /admin/users/{id}/deactivate — instance admin, session only

	// Everything below takes an org in its path and resolves it through
	// requireOrg, which consults auth.TokenScopeFromContext — this is the
	// "subtree that actually consults TokenScope" BearerMiddleware is
	// scoped to (see New's comment on why it is not mounted on the root
	// router). huma.Group.UseMiddleware wraps each operation registered on
	// the group individually, so this scoping holds per-operation without a
	// second chi router or a duplicate OpenAPI document.
	orgScoped := huma.NewGroup(hapi)
	orgScoped.UseMiddleware(bearerAuthMiddleware(deps.Tokens))
	registerOrgScopedRoutes(orgScoped, deps) // GET/PATCH /orgs/{org}
	registerMemberRoutes(orgScoped, deps)    // members + invites, all /orgs/{org}/...
	registerTokenRoutes(orgScoped, deps)     // /orgs/{org}/tokens...
}
