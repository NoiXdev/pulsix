package grpcsrv

import (
	"context"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

func TestStreamRegistry(t *testing.T) {
	r := NewStreamRegistry()
	w := uuid.Must(uuid.NewV7())
	a, releaseA := r.track(w)
	_, releaseB := r.track(w)
	releaseB()
	stored := 0
	appendOne := func() error { stored++; return nil }
	if err := a.store(appendOne); err != nil || stored != 1 {
		t.Fatalf("store on an open stream = %v, %d appends, want one", err, stored)
	}
	if n := r.CloseWorker(w); n != 1 {
		t.Fatalf("CloseWorker = %d, want the one open stream", n)
	}
	select {
	case <-a.revoked:
	default:
		t.Fatal("the open stream was not signalled")
	}
	if err := a.store(appendOne); err != nil || stored != 1 {
		t.Fatalf("store on a cut stream = %v, %d appends, want it dropped", err, stored)
	}
	releaseA() // after close: a no-op
	if n := r.CloseWorker(w); n != 0 {
		t.Fatalf("second CloseWorker = %d", n)
	}
}

// TestConcurrentCloseWorkerWaitsForTheAppendInFlight (backlog 2.54 review
// I1): the serving manager's own revocation watcher can call CloseWorker
// while Service.Revoke's CloseWorker is still to come. Whichever call comes
// second must not return early with 0: both report the stream and both
// return only once the append in flight has finished, so the requeue that
// follows Revoke's call never races a stray event.
func TestConcurrentCloseWorkerWaitsForTheAppendInFlight(t *testing.T) {
	r := NewStreamRegistry()
	w := uuid.Must(uuid.NewV7())
	ts, release := r.track(w)
	defer release()
	appending, finish := make(chan struct{}), make(chan struct{})
	appended := make(chan error, 1)
	go func() {
		appended <- ts.store(func() error { close(appending); <-finish; return nil })
	}()
	<-appending

	type result struct{ n int }
	first, second := make(chan result, 1), make(chan result, 1)
	go func() { first <- result{r.CloseWorker(w)} }()  // the watcher
	<-ts.revoked                                       // the first close is under way
	go func() { second <- result{r.CloseWorker(w)} }() // Service.Revoke's own
	select {
	case res := <-second:
		t.Fatalf("the second CloseWorker returned %d while an append was in flight", res.n)
	case res := <-first:
		t.Fatalf("the first CloseWorker returned %d while an append was in flight", res.n)
	case <-time.After(200 * time.Millisecond):
	}
	close(finish)
	if err := <-appended; err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]chan result{"first": first, "second": second} {
		select {
		case res := <-c:
			if res.n != 1 {
				t.Fatalf("%s CloseWorker = %d, want the one stream", name, res.n)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s CloseWorker never returned", name)
		}
	}
	if err := ts.store(func() error { t.Fatal("an append ran after the cut"); return nil }); err != nil {
		t.Fatal(err)
	}
	if got := r.Workers(); len(got) != 0 {
		t.Fatalf("Workers after the cut = %v", got)
	}
}

// TestStreamRegistryWorkers: Workers lists each worker with an open stream
// once; a released or closed worker drops out.
func TestStreamRegistryWorkers(t *testing.T) {
	r := NewStreamRegistry()
	if got := r.Workers(); len(got) != 0 {
		t.Fatalf("Workers of an empty registry = %v", got)
	}
	a, b, c := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	r.track(a)
	r.track(a)
	r.track(b)
	_, releaseC := r.track(c)
	releaseC()
	got := r.Workers()
	if len(got) != 2 || !slices.Contains(got, a) || !slices.Contains(got, b) {
		t.Fatalf("Workers = %v, want %s and %s once each", got, a, b)
	}
	r.CloseWorker(a)
	if got := r.Workers(); len(got) != 1 || got[0] != b {
		t.Fatalf("Workers after closing %s = %v, want only %s", a, got, b)
	}
}

// TestRevocationEndsOpenStreams: CloseWorker ends the worker's RunEvents
// stream with Unauthenticated.
func TestRevocationEndsOpenStreams(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.claimAndBind(jobID, orgID, zoneID)
	stream := h.openStream(jobID)
	if err := sendEvent(stream, jobID, 1, "web1", "ok"); err != nil {
		t.Fatal(err)
	}
	certPEM, _ := h.worker()
	leaf, _ := parseCert(certPEM)
	w, err := h.store.GetBySerial(t.Context(), leaf.SerialNumber)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.srv.streams.CloseWorker(w.ID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stream never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code := status.Code(expectEndRecv(stream)); code != codes.Unauthenticated {
		t.Fatalf("stream ended with %s, want Unauthenticated", code)
	}
}

// TestRevocationBetweenAuthAndTrackEndsTheStream: a revocation that
// commits after the interceptor resolved the serial but before RunEvents
// registered the stream is missed by CloseWorker; the handler re-checks
// the serial once the stream is tracked and ends it with Unauthenticated.
func TestRevocationBetweenAuthAndTrackEndsTheStream(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.claimAndBind(jobID, orgID, zoneID)
	certPEM, _ := h.worker()
	leaf, _ := parseCert(certPEM)
	// The stream's own authentication is the next lookup: it succeeds, and
	// the revocation lands right after it.
	h.store.mu.Lock()
	h.store.revokeAfterLookup = leaf.SerialNumber.String()
	h.store.mu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream := h.openStreamMeta(jobID, metadata.AppendToOutgoingContext(ctx,
		metadataJobID, jobID.String(), metadataWorkerToken, testToken(jobID)), h.workerCert, h.workerKey)
	if code := status.Code(expectEndRecv(stream)); code != codes.Unauthenticated {
		t.Fatalf("stream ended with %s, want Unauthenticated", code)
	}
}

// scriptedEventStream is a RunEvents server stream without a transport:
// Recv answers from a script, Send drops directives. The embedded
// ServerStream stays nil — RunEvents never calls its other methods.
type scriptedEventStream struct {
	grpc.ServerStream
	ctx  context.Context
	mu   sync.Mutex
	n    int
	recv func(n int) (*workerpb.RunEvent, error)
}

func (s *scriptedEventStream) Context() context.Context              { return s.ctx }
func (s *scriptedEventStream) Send(*workerpb.ManagerDirective) error { return nil }

func (s *scriptedEventStream) Recv() (*workerpb.RunEvent, error) {
	s.mu.Lock()
	s.n++
	n := s.n
	s.mu.Unlock()
	return s.recv(n)
}

// authenticatedStreamContext is the context the interceptor and the
// transport hand RunEvents for jobID: the binding metadata plus the
// verified identity and worker row of the standard worker certificate.
func (h *harness) authenticatedStreamContext(jobID uuid.UUID) (context.Context, workers.Worker) {
	h.t.Helper()
	certPEM, _ := h.worker()
	leaf, err := parseCert(certPEM)
	if err != nil {
		h.t.Fatal(err)
	}
	identity, err := h.ca.VerifyClient([][]byte{leaf.Raw})
	if err != nil {
		h.t.Fatal(err)
	}
	w, err := h.store.GetBySerial(h.t.Context(), leaf.SerialNumber)
	if err != nil {
		h.t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(h.t.Context(), metadata.Pairs(
		metadataJobID, jobID.String(), metadataWorkerToken, testToken(jobID)))
	ctx = context.WithValue(context.WithValue(ctx, identityKey{}, identity), workerKey{}, w)
	return ctx, w
}

// TestRevocationBetweenRecvAndAppendDropsTheEvent: a revocation that lands
// after the receive loop read an event but before it stored it (final
// review v0.2c Minor 6) must drop that event. Once CloseWorker returns the
// revoke requeues the job, so a late append would land in the requeued
// job's log. The script forces the interleaving: the first Recv returns an
// event only after the revocation has cut the stream; the second Recv
// blocks until the handler has returned — as the transport's does — so its
// call proves the loop is done with the first event either way.
func TestRevocationBetweenRecvAndAppendDropsTheEvent(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.srv.registry.register(jobID, testToken(jobID))
	ctx, w := h.authenticatedStreamContext(jobID)

	handlerReturned := make(chan struct{})
	secondRecv := make(chan struct{})
	stream := &scriptedEventStream{ctx: ctx, recv: func(n int) (*workerpb.RunEvent, error) {
		switch n {
		case 1:
			if cut := h.srv.streams.CloseWorker(w.ID); cut != 1 {
				t.Errorf("CloseWorker cut %d streams, want the one open stream", cut)
			}
			return &workerpb.RunEvent{JobId: jobID.String(), Seq: 2, Task: "run", State: "stdout"}, nil
		case 2:
			close(secondRecv)
		}
		<-handlerReturned
		return nil, io.EOF
	}}

	err := h.srv.RunEvents(stream)
	close(handlerReturned)
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Fatalf("RunEvents = %v, want Unauthenticated", err)
	}
	select {
	case <-secondRecv:
	case <-time.After(5 * time.Second):
		t.Fatal("the receive loop never went back to Recv after the revoked event")
	}
	if events := h.events.Events(); len(events) != 0 {
		t.Fatalf("stored %d events after the revocation, want none: %+v", len(events), events)
	}
}

// TestRunEventsTracksTheStreamBeforeTheRevocationRecheck pins the ORDER of
// the two statements the window above depends on: RunEvents must register
// the stream and only then re-read the serial.
// TestRevocationBetweenAuthAndTrackEndsTheStream proves the re-read
// happens, but it would stay green with the two lines swapped — and
// swapped they close nothing: a revocation committing between them would
// find no stream to cut AND a serial that still reads valid.
//
// The handler is called directly, so the context already carries what the
// interceptor resolved and the re-read is the handler's only lookup. The
// hook cuts the worker's streams from inside it: one stream cut means
// tracking had already happened.
func TestRunEventsTracksTheStreamBeforeTheRevocationRecheck(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.srv.registry.register(jobID, testToken(jobID))
	ctx, w := h.authenticatedStreamContext(jobID)

	cut := -1
	h.store.onLookup = func() { cut = h.srv.streams.CloseWorker(w.ID) }

	// Recv waits for the handler to return, so the cut — not an EOF racing
	// it — is what ends the stream. The wait is bounded: with the two
	// statements swapped nothing cuts the stream, and the test must report
	// the order rather than hang. The bound costs nothing when the order
	// is right, because the cut has already happened by the first Recv.
	handlerReturned := make(chan struct{})
	stream := &scriptedEventStream{ctx: ctx, recv: func(int) (*workerpb.RunEvent, error) {
		select {
		case <-handlerReturned:
		case <-time.After(5 * time.Second):
		}
		return nil, io.EOF
	}}
	err := h.srv.RunEvents(stream)
	close(handlerReturned)

	if cut != 1 {
		t.Fatalf("CloseWorker during the re-check cut %d streams, want the one this handler had already tracked", cut)
	}
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Fatalf("RunEvents = %v, want Unauthenticated from the cut", err)
	}
}
