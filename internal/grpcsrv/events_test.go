// RunEvents: the bound stream — events flow in order, the cancel directive
// arrives exactly once, events keep flowing after it, and every way to
// present a wrong, missing or expired binding is PermissionDenied.
package grpcsrv

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// manualTicker is the injected ticker the cancel-poll tests drive.
type manualTicker struct{ c chan time.Time }

func newManualTicker() *manualTicker { return &manualTicker{c: make(chan time.Time, 8)} }

func (m *manualTicker) Tick() <-chan time.Time { return m.c }
func (m *manualTicker) Stop()                  {}
func (m *manualTicker) tick()                  { m.c <- time.Now() }

// worker issues the standard worker certificate once and caches it.
func (h *harness) worker() (certPEM, keyPEM []byte) {
	h.t.Helper()
	if h.workerCert == nil {
		certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
		h.workerCert, h.workerKey = certPEM, keyPEM
	}
	return h.workerCert, h.workerKey
}

// claimAndBind enqueues a claim and consumes it over the wire, so the
// binding token lands in the registry exactly as ClaimJob leaves it.
func (h *harness) claimAndBind(jobID, orgID, zoneID uuid.UUID) {
	h.t.Helper()
	h.enqueueClaim(jobID, orgID, zoneID)
	certPEM, keyPEM := h.worker()
	client := h.client(certPEM, keyPEM)
	if _, err := client.ClaimJob(h.t.Context(), claimRequest(zoneID)); err != nil {
		h.t.Fatal(err)
	}
}

// openStream opens a RunEvents stream with the correct binding metadata.
func (h *harness) openStream(jobID uuid.UUID) workerpb.WorkerService_RunEventsClient {
	h.t.Helper()
	certPEM, keyPEM := h.worker()
	return h.openStreamMeta(jobID, metadata.AppendToOutgoingContext(h.t.Context(),
		metadataJobID, jobID.String(), metadataWorkerToken, testToken(jobID)), certPEM, keyPEM)
}

// openStreamMeta opens a RunEvents stream with explicit metadata and cert
// material (wrong-token and no-metadata tests use this).
func (h *harness) openStreamMeta(_ uuid.UUID, ctx context.Context, certPEM, keyPEM []byte) workerpb.WorkerService_RunEventsClient {
	h.t.Helper()
	client := h.client(certPEM, keyPEM)
	stream, err := client.RunEvents(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return stream
}

// sendEvent streams one RunEvent into the open stream.
func sendEvent(stream workerpb.WorkerService_RunEventsClient, jobID uuid.UUID, seq uint64, host, state string) error {
	return stream.Send(&workerpb.RunEvent{
		JobId: jobID.String(), Seq: seq, Host: host, Task: "run", State: state,
		At: timestamppb.New(time.Now()),
	})
}

// expectEndRecv drains the stream and asserts it ends with want.
func expectEndRecv(stream workerpb.WorkerService_RunEventsClient) error {
	err := stream.RecvMsg(&workerpb.ManagerDirective{})
	return err
}

// TestRunEventsFlow: three events stream through in order, and closing the
// send side ends the handler cleanly.
func TestRunEventsFlow(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.claimAndBind(jobID, orgID, zoneID)

	stream := h.openStream(jobID)
	for i := uint64(1); i <= 3; i++ {
		if err := sendEvent(stream, jobID, i, "web-1", "ok"); err != nil {
			t.Fatalf("send event %d: %v", i, err)
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if err := expectEndRecv(stream); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("stream end = %v, want EOF", err)
	}

	events := h.events.Events()
	if len(events) != 3 {
		t.Fatalf("recorded events = %d, want 3", len(events))
	}
	for i, want := range []uint64{1, 2, 3} {
		if events[i].Seq != want || events[i].Host != "web-1" || events[i].State != "ok" || events[i].JobID != jobID.String() {
			t.Fatalf("event %d = %+v, want seq %d for job %s", i, events[i], want, jobID)
		}
	}
	if events[0].At.IsZero() {
		t.Fatalf("event At must carry the wire timestamp, got %v", events[0].At)
	}
}

// TestRunEventsCancelDirectiveExactlyOnce: the fake flips the job into
// 'cancelling'; the server answers the next poll with one Cancel directive
// and never sends a second, while events keep being recorded afterwards.
func TestRunEventsCancelDirectiveExactlyOnce(t *testing.T) {
	h := newHarness(t)
	tick := newManualTicker()
	h.srv.deps.NewTicker = func(time.Duration) Ticker { return tick }
	jobID := uuid.Must(uuid.NewV7())
	h.claimAndBind(jobID, orgID, zoneID)

	stream := h.openStream(jobID)
	if err := sendEvent(stream, jobID, 1, "web-1", "ok"); err != nil {
		t.Fatal(err)
	}
	h.events.SetCancelling(jobID, true)
	tick.tick()

	dir := &workerpb.ManagerDirective{}
	if err := stream.RecvMsg(dir); err != nil {
		t.Fatalf("receive cancel directive: %v", err)
	}
	if !dir.GetCancel() {
		t.Fatalf("directive = %+v, want cancel", dir)
	}

	// Events after the cancel directive are still recorded.
	if err := sendEvent(stream, jobID, 2, "web-1", "ok"); err != nil {
		t.Fatal(err)
	}

	// A second tick must not produce a second directive: after the stream
	// closes the next message is the end, never a directive.
	tick.tick()
	if err := sendEvent(stream, jobID, 3, "web-1", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if err := expectEndRecv(stream); !errors.Is(err, io.EOF) {
		t.Fatalf("after cancel + close = %v, want EOF (no second directive)", err)
	}
	if events := h.events.Events(); len(events) != 3 {
		t.Fatalf("recorded events = %d, want 3 (events flow after cancel too)", len(events))
	}
}

// TestRunEventsWrongToken: a stream bound with a token that does not match
// the claim is PermissionDenied.
func TestRunEventsWrongToken(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.claimAndBind(jobID, orgID, zoneID)

	certPEM, keyPEM := h.worker()
	ctx := metadata.AppendToOutgoingContext(h.t.Context(),
		metadataJobID, jobID.String(), metadataWorkerToken, "wrong-token")
	stream := h.openStreamMeta(jobID, ctx, certPEM, keyPEM)
	if err := stream.Send(&workerpb.RunEvent{JobId: jobID.String()}); err != nil {
		t.Fatal(err) // client-side sends succeed; the server rejects the stream
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	err := expectEndRecv(stream)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %s (%v), want PermissionDenied", status.Code(err), err)
	}
	if !strings.Contains(status.Convert(err).Message(), "unknown, expired or already used") {
		t.Fatalf("message = %q, must explain the binding failure", status.Convert(err).Message())
	}
}

// TestRunEventsExpiredToken: past the 30 s TTL the binding is dead.
func TestRunEventsExpiredToken(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.claimAndBind(jobID, orgID, zoneID)

	h.advance(31 * time.Second)
	stream := h.openStream(jobID)
	if err := stream.Send(&workerpb.RunEvent{JobId: jobID.String()}); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if code := status.Code(expectEndRecv(stream)); code != codes.PermissionDenied {
		t.Fatalf("code = %s, want PermissionDenied", code)
	}
}

// TestRunEventsTokenSingleUse: a second stream with the same (still valid)
// token is rejected — the binding is consumed by the first.
func TestRunEventsTokenSingleUse(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.claimAndBind(jobID, orgID, zoneID)

	first := h.openStream(jobID)
	if err := sendEvent(first, jobID, 1, "web-1", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := first.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if err := expectEndRecv(first); !errors.Is(err, io.EOF) {
		t.Fatalf("first stream = %v, want EOF", err)
	}

	second := h.openStream(jobID)
	if err := second.Send(&workerpb.RunEvent{JobId: jobID.String()}); err != nil {
		t.Fatal(err)
	}
	if err := second.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if code := status.Code(expectEndRecv(second)); code != codes.PermissionDenied {
		t.Fatalf("second stream code = %s, want PermissionDenied", code)
	}
}

// TestRunEventsMissingMetadata: a stream with no binding metadata at all is
// PermissionDenied.
func TestRunEventsMissingMetadata(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.claimAndBind(jobID, orgID, zoneID)

	certPEM, keyPEM := h.worker()
	stream := h.openStreamMeta(jobID, h.t.Context(), certPEM, keyPEM)
	if err := stream.Send(&workerpb.RunEvent{JobId: jobID.String()}); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if code := status.Code(expectEndRecv(stream)); code != codes.PermissionDenied {
		t.Fatalf("code = %s, want PermissionDenied", code)
	}
}

// TestRunEventsAppendError: a failing recorder surfaces as Internal with
// the wrapped context ("append run event: ..."), not as a bare error.
func TestRunEventsAppendError(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.claimAndBind(jobID, orgID, zoneID)
	h.events.SetAppendError(errors.New("insert failed"))

	stream := h.openStream(jobID)
	if err := sendEvent(stream, jobID, 1, "web-1", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	err := expectEndRecv(stream)
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %s (%v), want Internal", status.Code(err), err)
	}
	if msg := status.Convert(err).Message(); !strings.Contains(msg, "append run event") || !strings.Contains(msg, "insert failed") {
		t.Fatalf("message = %q, want wrapped 'append run event: insert failed'", msg)
	}
}

// TestRunEventsEventJobMismatch: an event naming another job than the
// bound stream is InvalidArgument.
func TestRunEventsEventJobMismatch(t *testing.T) {
	h := newHarness(t)
	jobID := uuid.Must(uuid.NewV7())
	h.claimAndBind(jobID, orgID, zoneID)

	stream := h.openStream(jobID)
	other := uuid.Must(uuid.NewV7())
	if err := stream.Send(&workerpb.RunEvent{JobId: other.String()}); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if code := status.Code(expectEndRecv(stream)); code != codes.InvalidArgument {
		t.Fatalf("code = %s, want InvalidArgument", code)
	}
}
