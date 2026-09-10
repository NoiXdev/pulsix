// Command manager runs the Pulsix manager (API, SPA, scheduler).
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/noixdev/pulsix/internal/api"
)

func main() {
	addr := os.Getenv("PULSIX_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	slog.Info("pulsix-manager listening", "addr", addr)
	if err := http.ListenAndServe(addr, api.New(api.Deps{})); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
