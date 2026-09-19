package grpcsrv

import (
	"context"
	"io"
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
