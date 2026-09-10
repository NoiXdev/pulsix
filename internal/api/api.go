// Package api builds the HTTP handler for the Pulsix manager.
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
)

// Deps carries everything the handlers need. Handlers call services only.
type Deps struct {
	DB       *db.DB
	Sessions *auth.Sessions
	Auth     *auth.Service
	Secure   bool // set Secure on cookies (true behind HTTPS)
}

type healthOutput struct {
	Body struct {
		Status string `json:"status" example:"ok"`
	}
}

// New returns the root http.Handler: chi router + huma API under /api/v1.
func New(deps Deps) http.Handler {
	r := chi.NewRouter()
	// middleware.RealIP is intentionally omitted: it is deprecated in this
	// chi version because it mutates RemoteAddr from client-controlled
	// headers without a trusted-proxy allowlist (IP spoofing risk). A
	// later task can add a spoof-safe replacement once the deployment's
	// reverse-proxy trust boundary is known.
	r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)
	if deps.Sessions != nil {
		r.Use(auth.CookieMiddleware(deps.Sessions))
	}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	r.Get("/api/explorer", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Pulsix API</title></head><body>
<script id="api-reference" data-url="/api/openapi.json"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script></body></html>`))
	})

	cfg := huma.DefaultConfig("Pulsix API", "0.1.0")
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = "" // explorer is mounted above, served by Scalar instead of huma's default docs
	hapi := humachi.New(r, cfg)
	registerRoutes(hapi, deps)
	return r
}

// registerRoutes is extended by later tasks (one register* call per resource).
func registerRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/health", func(ctx context.Context, _ *struct{}) (*healthOutput, error) {
		out := &healthOutput{}
		out.Body.Status = "ok"
		return out, nil
	})
	registerAuthRoutes(hapi, deps)
}
