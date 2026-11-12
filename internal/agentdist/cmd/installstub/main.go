// Command installstub is a test-only stand-in for the manager's agent
// distribution routes, for CI's install smoke on the Windows and macOS
// runners (scripts/ci/install-smoke-*.{sh,ps1}). It serves GET /install.sh
// and GET /install.ps1 rendered with its own address, and
// GET /agents/bin/{os}/{arch}[.sha256] from a directory of binaries built
// for the runner. Nothing else: no enrollment, no rate limit, no session.
//
// It is never shipped. It does not import internal/api so the smoke does
// not need the manager's whole dependency tree; the routes it mirrors are
// internal/api/agent_install.go's, and the installers it serves are the
// real ones, rendered by internal/agentdist.
//
// Usage: installstub -dist <dir> [-addr 127.0.0.1:18080]
// It prints "ready" on its own line once it listens.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/agentdist"
)

// targetPart is the shape of a GOOS or GOARCH word, as the manager's route
// validates it: the file name is built from validated words only.
var targetPart = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "installstub:", err)
		os.Exit(1)
	}
}

// run parses the flags, listens, prints "ready" to stdout and serves until
// ctx ends.
func run(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("installstub", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("addr", "127.0.0.1:18080", "address to listen on; the installers are rendered with http://<addr>")
	dist := fs.String("dist", "", "directory holding pulsix-agent-<os>-<arch> and its .sha256")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if *dist == "" {
		return errors.New("-dist is required")
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	srv := &http.Server{
		Handler:           newHandler("http://"+*addr, *dist),
		ReadHeaderTimeout: 10 * time.Second,
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	if _, err := fmt.Fprintln(stdout, "ready"); err != nil {
		_ = srv.Close()
		return fmt.Errorf("print ready: %w", err)
	}

	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// newHandler serves the two installers rendered with base and the agent
// binaries and checksums found in dist.
func newHandler(base, dist string) http.Handler {
	files := os.DirFS(dist)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /install.sh", func(w http.ResponseWriter, _ *http.Request) {
		writeScript(w, base, agentdist.RenderShellInstaller)
	})
	mux.HandleFunc("GET /install.ps1", func(w http.ResponseWriter, _ *http.Request) {
		writeScript(w, base, agentdist.RenderPowerShellInstaller)
	})
	mux.HandleFunc("GET /agents/bin/{os}/{arch}", func(w http.ResponseWriter, r *http.Request) {
		goos, arch := r.PathValue("os"), r.PathValue("arch")
		if !targetPart.MatchString(goos) || !targetPart.MatchString(strings.TrimSuffix(arch, agentdist.ChecksumSuffix)) {
			http.NotFound(w, r)
			return
		}
		// A missing file is a 404 from ServeFileFS itself.
		http.ServeFileFS(w, r, files, agentdist.BinaryPrefix+goos+"-"+arch)
	})
	return mux
}

// writeScript renders an installer per request: the stub is short-lived
// and the rendering cheap, and a render error is answered rather than
// stopping the stub.
func writeScript(w http.ResponseWriter, base string, render func(string) ([]byte, error)) {
	script, err := render(base)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(script)
}
