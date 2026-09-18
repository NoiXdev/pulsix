package grpcsrv

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStreamRegistry(t *testing.T) {
	r := NewStreamRegistry()
	w := uuid.Must(uuid.NewV7())
	a, releaseA := r.track(w)
	_, releaseB := r.track(w)
	releaseB()
	if n := r.CloseWorker(w); n != 1 {
		t.Fatalf("CloseWorker = %d, want the one open stream", n)
	}
	select {
	case <-a:
	default:
		t.Fatal("the open stream was not signalled")
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
