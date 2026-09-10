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
	slog.Info("pulsix-manager listening", "addr", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, api.New(api.Deps{})); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
