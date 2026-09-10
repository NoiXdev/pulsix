// Command manager runs the Pulsix manager (API, SPA, scheduler).
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/config"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}
	// api.New refuses to build a handler for a half-wired Deps (see Task 6
	// fix round 1, Finding 5): the auth routes it registers dereference
	// Sessions/Auth on every request, so serving them nil would panic
	// under the recoverer on the first real login attempt. The database,
	// session store and auth service are wired up by a later task; until
	// then this exits loudly at startup instead of serving a manager whose
	// auth endpoints cannot work.
	handler, err := api.New(api.Deps{})
	if err != nil {
		slog.Error("manager not yet wired: database, sessions and auth service are wired up by a later task", "err", err)
		os.Exit(1)
	}
	slog.Info("pulsix-manager listening", "addr", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, handler); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
