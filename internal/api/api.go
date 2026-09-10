// Package api builds the HTTP handler for the Pulsix manager.
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Deps carries everything the handlers need. Later tasks add fields.
type Deps struct{}

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

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	cfg := huma.DefaultConfig("Pulsix API", "0.1.0")
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = "" // explorer is mounted by Task 6
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
}
