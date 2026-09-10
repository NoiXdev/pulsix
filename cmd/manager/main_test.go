package main

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
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
	case <-returned:
		t.Fatal("serveWithGracefulShutdown returned before the in-flight request finished")
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
