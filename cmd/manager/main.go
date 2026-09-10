// Command manager runs the Pulsix manager (API, SPA, scheduler).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/config"
)

// shutdownTimeout bounds how long graceful shutdown waits for in-flight
// requests to finish before giving up.
const shutdownTimeout = 10 * time.Second

// main only translates run's exit code: the actual logic lives in run so
// that every defer in it (stop(), a.Close()) actually executes — os.Exit
// called from inside a function with pending defers skips them, which is
// why this indirection exists rather than calling os.Exit directly from
// deeper in the function, per this repo's golangci-lint (gocritic
// exitAfterDefer).
func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("configuration error", "err", err)
		return 1
	}

	// Cancelled on SIGTERM (the signal a container orchestrator sends for a
	// graceful stop) or SIGINT (Ctrl-C during local development) — either
	// starts the shutdown sequence below.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	a, err := app.Build(ctx, cfg)
	if err != nil {
		// This is where a misconfigured deployment fails: a database role
		// that can bypass row-level security, an unreachable database, or
		// any other wiring problem app.Build detects — never a server that
		// starts and only leaks tenant data on the first real request. See
		// app.Build's own doc comment.
		slog.Error("failed to build application", "err", err)
		return 1
	}
	// a.Close() tears down the database pool. serveWithGracefulShutdown
	// below must have fully drained in-flight requests before this defer
	// runs, or a request still executing when the listener closed would
	// have the pool pulled out from under it.
	defer a.Close()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	slog.Info("pulsix-manager listening", "addr", cfg.Listen)
	if err := serveWithGracefulShutdown(ctx, stop, srv, srv.ListenAndServe); err != nil {
		slog.Error("server stopped", "err", err)
		return 1
	}
	return 0
}

// serveWithGracefulShutdown runs serve (normally srv.ListenAndServe) until
// ctx is cancelled, then waits for srv.Shutdown to actually finish draining
// in-flight requests before returning.
//
// serve's own return does not mean that drain has happened: http.Server's
// ListenAndServe/Serve returns as soon as the listener closes, which
// Shutdown does immediately, well before the requests it is waiting on have
// finished — Shutdown itself keeps blocking in the background. Calling
// serve in the foreground and Shutdown from a goroutine (the shape this
// function replaces) therefore lets the caller return, and any deferred
// cleanup run, the moment the listener closes rather than once the drain
// completes, tearing down whatever the caller cleans up (here, the
// database pool) out from under a request Shutdown is still waiting on.
//
// This function closes that gap: the shutdown goroutine signals its own
// completion on shutdownDone, and this function does not return until that
// signal arrives, however serve itself returned. stop is called
// unconditionally so the shutdown goroutine's <-ctx.Done() always unblocks
// — including when serve returned for a reason other than ctx firing (e.g.
// a genuine listen error) — rather than only waiting for a signal that may
// never come.
//
// Extracted out of run() as its own function — rather than run() itself
// juggling the wait — specifically so the drain ordering can be exercised
// by a test with a context this package cancels directly, without going
// through a real OS signal (see TestServeWithGracefulShutdownWaitsForDrain
// in main_test.go).
func serveWithGracefulShutdown(ctx context.Context, stop context.CancelFunc, srv *http.Server, serve func() error) error {
	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed", "err", err)
		}
		close(shutdownDone)
	}()

	err := serve()
	stop()
	<-shutdownDone

	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
