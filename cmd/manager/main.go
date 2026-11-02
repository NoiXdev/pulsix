// Command manager runs the Pulsix manager (API, SPA, scheduler) and its
// operator subcommands (`worker add`, `backup`, `restore`).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	// The IANA zone database, embedded. Schedules, maintenance windows,
	// alert routes, on-call rotations and digests all resolve a named zone
	// through time.LoadLocation, which reads the host's zoneinfo files;
	// a host that ships none fails all of them at once, and reports it as
	// a bad zone the operator saved (internal/schedules' "the schedule's
	// timetable is invalid; edit and save it"), which no edit can fix.
	// This copy is only a fallback — the host's own files still win, and
	// still receive the host's updates (backlog 2.88).
	_ "time/tzdata"

	"google.golang.org/grpc"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// shutdownTimeout bounds how long graceful shutdown waits for in-flight
// requests to finish before giving up.
const shutdownTimeout = 10 * time.Second

// version is the manager's build version, reported on /healthz so an
// operator can tell exactly what is running without inspecting the image
// tag. It stays "dev" for a local `go build`/`go test`/`go run`; the
// release Dockerfile overrides it with `-ldflags "-X main.version=..."`
// set from the release tag, so the value baked into a shipped image always
// matches the version that tag released.
var version = "dev"

// main only translates run's exit code: the actual logic lives in run so
// that every defer in it (stop(), a.Close()) actually executes — os.Exit
// called from inside a function with pending defers skips them, which is
// why this indirection exists rather than calling os.Exit directly from
// deeper in the function, per this repo's golangci-lint (gocritic
// exitAfterDefer).
func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch routes to the "openapi" subcommand (writes the OpenAPI document
// to stdout and exits, no server or database involved — see
// api.Document's doc comment), the "worker add" enrollment subcommand (see
// app.RunWorkerAdd), the "backup" and "restore" commands (backupcmd.go),
// the play supervisor's reserved argument, or — with no
// arguments at all — starts the manager server. The supervisor case (and
// the sweep beside it, backlog 7.21) exists
// because the embedded worker runs plays by re-executing this very binary
// (worker.Config.PlaySupervisor is the manager's own path there); it must
// stay a case of its own, ahead of the usage error, and does not touch the
// environment or the configuration. Any other argument list is a usage
// error (exit 2), never a server start:
// `docker compose run manager /pulsix-manager worker add …`
// (the image's ENTRYPOINT already is /pulsix-manager, so the binary sees
// its own path as the first argument) used to fall through to run() and
// start a second manager against the same database.
func dispatch(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 0:
		return run()
	case args[0] == supervisor.Arg:
		return supervisor.Main(args[1:])
	case args[0] == supervisor.SweepArg:
		return supervisor.Sweep(stderr)
	case args[0] == "openapi":
		return runOpenAPI(stdout)
	case len(args) >= 2 && args[0] == "worker" && args[1] == "add":
		return runWorkerAdd(args[2:], stdout, stderr)
	case args[0] == "backup":
		return runBackup(args[1:], stdout, stderr)
	case args[0] == "restore":
		return runRestore(args[1:], stdout, stderr)
	}
	_, _ = fmt.Fprintf(stderr, "pulsix-manager: unknown command %q\n%s", strings.Join(args, " "), usageText)
	return 2
}

// usageText is the command synopsis a usage error prints.
const usageText = `usage:
  pulsix-manager                  start the manager (configuration from PULSIX_* variables)
  pulsix-manager openapi          print the OpenAPI document
  pulsix-manager worker add --zone <slug> --name <name> [--org <slug>] [--bundle-dir <dir> [--wait]] [--replace]
                                  enroll a worker; --replace enrolls it again when its name is taken
  pulsix-manager backup [--to <dir>|s3://bucket/prefix] [--without-metrics]
                                  take a backup (default: the destination set in the web interface)
  pulsix-manager backup key       print the passphrase that decrypts backups with the age tool
  pulsix-manager restore --list [--from <dir>|s3://bucket/prefix]
                                  list backups (default: /var/lib/pulsix/backups)
  pulsix-manager restore [--from <dir>|s3://bucket/prefix] [--replace] [--yes] <name>|-
                                  restore a backup (- reads it from standard input); stop the manager first
`

// runWorkerAdd parses `worker add`'s flags and enrolls a worker. Errors go
// to stderr; exit 1. Nothing is logged to stdout but the PEM blocks, so the
// command's output stays pipeable. With --bundle-dir the blocks go into
// files instead (the unattended compose enrollment service); --wait, only
// valid together with --bundle-dir, retries until the zone exists.
func runWorkerAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("worker add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	zoneSlug := fs.String("zone", "", "zone slug the worker serves (required)")
	orgSlug := fs.String("org", "", "organization slug (required only when several organizations use the same zone slug)")
	name := fs.String("name", "", "worker name, 1-63 lowercase letters, digits or hyphens (required)")
	bundleDir := fs.String("bundle-dir", "", "write cert.pem, key.pem and ca.pem into this directory instead of printing them; a still-valid bundle there is reused")
	wait := fs.Bool("wait", false, "with --bundle-dir: wait until the zone exists instead of failing")
	replace := fs.Bool("replace", false, "when the organization already has an active worker of that name (a restored database), revoke and delete it, then enroll")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *zoneSlug == "" || *name == "" {
		// stderr here is best-effort: the diagnostic is for the operator's
		// terminal, and a closed pipe must not turn a usage hint into a
		// different error. Every other exit path returns 1 explicitly.
		_, _ = fmt.Fprintln(stderr, "worker add: --zone and --name are required")
		fs.Usage()
		return 1
	}
	if *wait && *bundleDir == "" {
		_, _ = fmt.Fprintln(stderr, "worker add: --wait requires --bundle-dir")
		fs.Usage()
		return 1
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "worker add: unexpected arguments %v\n", fs.Args())
		fs.Usage()
		return 1
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "worker add: configuration error: %v\n", err)
		return 1
	}
	// SIGTERM ends a --wait loop cleanly (docker compose down while the
	// enrollment service still waits for the setup wizard).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	opts := app.WorkerAddOptions{ZoneSlug: *zoneSlug, OrgSlug: *orgSlug, Name: *name, BundleDir: *bundleDir, Wait: *wait, Replace: *replace}
	if err := app.RunWorkerAdd(ctx, cfg, opts, stdout, stderr); err != nil {
		_, _ = fmt.Fprintf(stderr, "worker add: %v\n", err)
		return 1
	}
	return 0
}

func run() int {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("configuration error", "err", err)
		return 1
	}
	cfg.Version = version

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
	// a.Close() tears down the database pool. The servers below must have
	// fully drained in-flight work before this defer runs, or a request or
	// RPC still executing when the listeners closed would have the pool
	// pulled out from under it — serve() waits for both drains before
	// returning, and function-own defers run after it returns.
	defer a.Close()

	return serve(ctx, stop, cfg, a)
}

// serve runs the HTTP server and — when the app wired one — the worker gRPC
// endpoint until ctx is cancelled, and returns the process exit code.
//
// The gRPC listener binds BEFORE the HTTP server starts serving: a bind
// failure (a port conflict on PULSIX_GRPC_LISTEN) aborts startup with a
// non-zero exit. A manager that reports healthy while its configured worker
// endpoint is dead is exactly the failure an orchestrator would otherwise
// never notice, so this is fail-fast, not log-and-continue — and on that
// path nothing was serving yet, so aborting cannot strand connections.
func serve(ctx context.Context, stop context.CancelFunc, cfg config.Config, a *app.App) int {
	if a.GRPC != nil {
		grpcLis, err := net.Listen("tcp", cfg.GRPCListen)
		if err != nil {
			slog.Error("worker gRPC listener failed to bind", "addr", cfg.GRPCListen, "err", err)
			return 1
		}
		// Serve until ctx fires. The deferred wait keeps run()'s own
		// sequencing — the gRPC drain completes before a.Close() runs —
		// and cannot hang forever: the graceful stop it waits on is
		// bounded by grpcShutdownTimeout (see serveGRPCWithGracefulStop).
		grpcStopped := serveGRPCWithGracefulStop(ctx, a.GRPC, grpcLis)
		defer func() { <-grpcStopped }()
	}

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

// grpcShutdownTimeout bounds how long graceful gRPC stop waits for
// in-flight RPCs before forcing the stop — a package var so tests can
// shrink it (a RunEvents stream held open by a worker would otherwise take
// the real timeout to prove the bound).
var grpcShutdownTimeout = shutdownTimeout

// serveGRPCWithGracefulStop serves the worker gRPC server on lis and
// triggers GracefulStop when ctx is cancelled. It returns a channel closed
// once the stop has completed, so the caller can sequence its cleanup
// after it.
//
// The stop is bounded by grpcShutdownTimeout: GracefulStop waits for
// in-flight RPCs — a worker holding a RunEvents stream open would make
// that wait unbounded — and past the bound the shutdown falls back to
// srv.Stop(), which closes transports immediately. Without that fallback
// an early serve error (whose caller has already cancelled ctx through
// serveWithGracefulShutdown) could hang the deferred wait on the way out.
func serveGRPCWithGracefulStop(ctx context.Context, srv *grpc.Server, lis net.Listener) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		slog.Info("shutting down gRPC server")
		gracefulDone := make(chan struct{})
		go func() {
			defer close(gracefulDone)
			srv.GracefulStop()
		}()
		select {
		case <-gracefulDone:
		case <-time.After(grpcShutdownTimeout):
			slog.Warn("gRPC graceful stop timed out; forcing stop", "timeout", grpcShutdownTimeout)
			srv.Stop()
		}
		close(done)
	}()
	slog.Info("pulsix-manager gRPC listening", "addr", lis.Addr())
	go func() {
		if err := srv.Serve(lis); err != nil {
			slog.Error("gRPC server stopped", "err", err)
		}
	}()
	return done
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
