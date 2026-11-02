package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// TestServeWithGracefulShutdownWaitsForDrain is a regression test for
// Finding 2 (Task 12 fix round 1): SIGTERM did not drain in-flight
// requests, because run() waited only on ListenAndServe, which returns as
// soon as Shutdown closes the listener — before Shutdown's own drain of
// in-flight connections has finished. serveWithGracefulShutdown must not
// return until that drain genuinely completes.
//
// It drives the same ordering the real signal path exercises — cancel the
// context, expect Shutdown to block on an in-flight request, expect
// serveWithGracefulShutdown to keep blocking too — but with a context this
// test cancels directly instead of a real SIGTERM, and a plain net.Listener
// instead of a bound cfg.Listen address, so the test needs neither a signal
// nor a fixed port.
func TestServeWithGracefulShutdownWaitsForDrain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	handlerStarted := make(chan struct{})
	release := make(chan struct{})
	var handlerFinished atomic.Bool

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(handlerStarted)
		<-release // held open to simulate a request still in flight at shutdown time
		handlerFinished.Store(true)
		w.WriteHeader(http.StatusOK)
	})}

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		returned <- serveWithGracefulShutdown(ctx, cancel, srv, func() error { return srv.Serve(ln) })
	}()

	reqDone := make(chan struct{})
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String()) //nolint:noctx
		if err == nil {
			_ = resp.Body.Close()
		}
		close(reqDone)
	}()
	<-handlerStarted

	// This is the SIGTERM moment: cancelling ctx is what signal.NotifyContext
	// would do on the real path.
	cancel()

	// serveWithGracefulShutdown must still be blocked, draining the
	// in-flight handler above — this is exactly the property the buggy
	// version violated (it returned here, as soon as Serve's listener
	// closed, without waiting for Shutdown's drain).
	select {
	case err := <-returned:
		t.Fatalf("serveWithGracefulShutdown returned (%v) before the in-flight request finished", err)
	case <-time.After(100 * time.Millisecond):
	}
	if handlerFinished.Load() {
		t.Fatal("test setup bug: handler finished before being released")
	}

	close(release)
	<-reqDone

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("serveWithGracefulShutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveWithGracefulShutdown never returned after the in-flight request finished")
	}
	if !handlerFinished.Load() {
		t.Fatal("handler never finished")
	}
}

// TestServeGRPCWithGracefulStop (Task 4): the gRPC server serves on the
// given listener and GracefulStop completes when the context is cancelled —
// signalled by the returned channel closing, which is the ordering main.go
// sequences its cleanup on. A plain, non-TLS server is enough: this covers
// start/stop wiring, not the mTLS layer (internal/grpcsrv's own tests cover
// that).
func TestServeGRPCWithGracefulStop(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := serveGRPCWithGracefulStop(ctx, srv, lis)
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("GracefulStop did not complete after the context was cancelled")
	}
}

// blockingWorker is a WorkerService whose RunEvents handler holds the
// stream open until its transport is torn down — the in-flight RPC that
// makes GracefulStop wait, and the hang the bounded stop exists for.
type blockingWorker struct {
	workerpb.UnimplementedWorkerServiceServer
	started sync.Once
	entered chan struct{}
}

func (b *blockingWorker) RunEvents(stream workerpb.WorkerService_RunEventsServer) error {
	b.started.Do(func() { close(b.entered) })
	<-stream.Context().Done() // release only when the transport is closing
	return nil
}

// TestServeGRPCGracefulStopIsBounded (fix round 1): an in-flight RPC — a
// worker holding a RunEvents stream open — makes GracefulStop wait
// unboundedly. The stop must fall back to srv.Stop() at the timeout so the
// deferred wait in serve() can never hang the exit.
func TestServeGRPCGracefulStopIsBounded(t *testing.T) {
	restore := grpcShutdownTimeout
	grpcShutdownTimeout = 200 * time.Millisecond
	t.Cleanup(func() { grpcShutdownTimeout = restore })

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	w := &blockingWorker{entered: make(chan struct{})}
	srv := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(srv, w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := serveGRPCWithGracefulStop(ctx, srv, lis)

	// A client holds one RunEvents stream open — the RPC GracefulStop
	// would wait for forever.
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }() // best-effort: the test is done with the conn
	stream, err := workerpb.NewWorkerServiceClient(conn).RunEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.CloseSend() }()
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocking handler never started")
	}

	// Fire the shutdown and demand it completes despite the open stream.
	cancel()
	st := time.Now()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded graceful stop did not complete while a stream was open — exit would hang")
	}
	if elapsed := time.Since(st); elapsed < 150*time.Millisecond {
		t.Fatalf("stop completed after %v, want it to have waited at least the 200ms bound first", elapsed)
	}
}

// TestDispatchRejectsUnknownArguments (fix round 1): an argument list that
// names no known subcommand is a usage error (exit 2) — it must never fall
// through to starting a second manager server. The first case is exactly
// what `docker compose run manager /pulsix-manager worker add …` passes,
// since the image's ENTRYPOINT already is /pulsix-manager.
func TestDispatchRejectsUnknownArguments(t *testing.T) {
	cases := [][]string{
		{"/pulsix-manager", "worker", "add", "--zone", "default", "--name", "w1"},
		{"worker"},
		{"worker", "remove"},
		{"serve"},
		{"--help"},
	}
	for _, args := range cases {
		var stderr bytes.Buffer
		if code := dispatch(args, io.Discard, &stderr); code != 2 {
			t.Errorf("dispatch %v: exit code = %d, want 2 (usage error)", args, code)
		}
		if !strings.Contains(stderr.String(), "usage:") {
			t.Errorf("dispatch %v: stderr = %q, want a usage message", args, stderr.String())
		}
	}
}

// TestDispatchRoutesSupervisorArg: the embedded worker re-executes this
// binary as the play supervisor, so the reserved argument has to reach the
// supervisor — with no further arguments it refuses (exit 1) rather than
// falling into the unknown-command usage error (exit 2).
func TestDispatchRoutesSupervisorArg(t *testing.T) {
	var stderr bytes.Buffer
	if code := dispatch([]string{supervisor.Arg}, io.Discard, &stderr); code != 1 {
		t.Fatalf("dispatch supervisor arg: exit code = %d, want 1 (the supervisor's refusal)", code)
	}
	if strings.Contains(stderr.String(), "usage:") {
		t.Errorf("stderr = %q, the supervisor arg must not reach the usage error", stderr.String())
	}
}

// TestWorkerAddFlagValidation: missing flags fail fast with exit 1, before
// any database or configuration is touched.
func TestWorkerAddFlagValidation(t *testing.T) {
	cases := [][]string{
		nil,
		{"--zone", "fra-1"},
		{"--name", "fra-1-a"},
		// --wait only makes sense for the unattended bundle mode.
		{"--zone", "default", "--name", "w1", "--wait"},
	}
	for _, args := range cases {
		if code := runWorkerAdd(args, io.Discard, io.Discard); code != 1 {
			t.Errorf("worker add %v: exit code = %d, want 1", args, code)
		}
	}
}

// TestServeAbortsWhenGRPCBindFails (fix round 1): when PULSIX_GRPC_LISTEN
// names a port that cannot be bound (here: bound twice), serve must abort
// with exit code 1 BEFORE the HTTP server starts — a manager must never
// come up healthy with its configured worker endpoint dead.
func TestServeAbortsWhenGRPCBindFails(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }() // holds the port until the test ends

	// Capture the default logger so the test can prove serve aborted
	// rather than proceeded: the bind-failure log must be there, the
	// "listening" line (logged only once HTTP serving starts) must not.
	// The mutex guards slog writes from serve's goroutine against the
	// assertions below.
	var logsMu sync.Mutex
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&syncWriter{mu: &logsMu, w: &logs}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &app.App{GRPC: grpc.NewServer()}
	cfg := config.Config{Listen: "127.0.0.1:0", GRPCListen: occupied.Addr().String()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- serve(ctx, cancel, cfg, a) }()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("serve exit code = %d, want 1 (abort on bind failure)", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve kept running after the gRPC bind failure — it must abort startup")
	}
	logsMu.Lock()
	out := logs.String()
	logsMu.Unlock()
	if !strings.Contains(out, "failed to bind") || strings.Contains(out, "pulsix-manager listening") {
		t.Fatalf("serve must abort before the HTTP server starts; logs:\n%s", out)
	}
}

// syncWriter is the mutex-protected io.Writer the slog captures above use,
// because the default logger writes from serve's goroutine.
type syncWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// TestServeShutsDownCleanly (fix round 1): normal cancellation shuts both
// servers down and serve returns 0 — within a timeout, so a regression
// that hangs the deferred gRPC wait fails loudly instead of freezing CI.
func TestServeShutsDownCleanly(t *testing.T) {
	a := &app.App{GRPC: grpc.NewServer()}
	cfg := config.Config{Listen: "127.0.0.1:0", GRPCListen: "127.0.0.1:0"}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan int, 1)
	go func() { done <- serve(ctx, cancel, cfg, a) }()
	time.Sleep(100 * time.Millisecond) // let both listeners come up
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exit code = %d, want 0 on clean shutdown", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after cancellation — shutdown hangs")
	}
}
