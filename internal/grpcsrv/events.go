// RunEvents: the run's event stream. The worker opens it right after a
// successful claim, presenting job_id + worker_token metadata; the manager
// binds the stream to the claim (single-use, 30 s TTL), appends every
// event, and — polled concurrently — sends the cancel directive exactly
// once when the job enters the cancelling state. The stream stays open
// until the worker closes it, so events keep flowing either way.
package grpcsrv

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// Stream metadata keys a worker sets when opening RunEvents.
const (
	metadataJobID       = "job_id"
	metadataWorkerToken = "worker_token"
)

// Ticker is the sliver of time.Ticker the cancel poll drives; injected so
// tests can tick deterministically.
type Ticker interface {
	Tick() <-chan time.Time
	Stop()
}

// wallTicker wraps time.Ticker as the production Ticker.
type wallTicker struct{ t *time.Ticker }

func (w *wallTicker) Tick() <-chan time.Time { return w.t.C }
func (w *wallTicker) Stop()                  { w.t.Stop() }

// RunEvents implements workerpb.WorkerServiceServer.RunEvents.
func (s *Server) RunEvents(stream workerpb.WorkerService_RunEventsServer) error {
	ctx := stream.Context()
	jobID, err := s.bindStream(ctx)
	if err != nil {
		return err
	}

	// Recv and Send are each single-goroutine, but a Send goroutine may run
	// next to the Recv loop — so the cancel poll sends directly.
	done := make(chan struct{})
	defer close(done)
	go s.pollCancels(ctx, jobID, stream, done)

	// Events after a cancel directive are still recorded: the worker needs
	// a little more time to shut its run down, and those events are what
	// the run's UI shows.
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err // transport-level failure: pass the status through
		}
		if id := ev.GetJobId(); id != "" && id != jobID.String() {
			return status.Errorf(codes.InvalidArgument, "event job id %q does not match the bound stream", id)
		}
		if err := s.deps.Events.Append(ctx, Event{
			JobID:   jobID.String(),
			Seq:     ev.GetSeq(),
			Host:    ev.GetHost(),
			Task:    ev.GetTask(),
			State:   ev.GetState(),
			Payload: ev.GetPayload(),
			At:      eventAt(ev, s.deps.Clock),
		}); err != nil {
			return status.Errorf(codes.Internal, "append run event: %v", err)
		}
	}
}

// eventAt resolves the event's timestamp; a worker that omits one gets the
// server's clock.
func eventAt(ev *workerpb.RunEvent, clock func() time.Time) time.Time {
	if ts := ev.GetAt(); ts != nil {
		return ts.AsTime()
	}
	return clock()
}

// bindStream validates the metadata against the claim registry: both keys
// must be present, the job id must parse, and the pair must match a
// registered, unexpired binding — which is then consumed (single use).
func (s *Server) bindStream(ctx context.Context) (uuid.UUID, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return uuid.Nil, status.Error(codes.PermissionDenied, "stream is missing its binding metadata")
	}
	jobID, token := firstMetadata(md, metadataJobID), firstMetadata(md, metadataWorkerToken)
	if token == "" {
		return uuid.Nil, status.Error(codes.PermissionDenied, "stream is missing the worker_token")
	}
	if jobID == "" {
		return uuid.Nil, status.Error(codes.PermissionDenied, "stream is missing the job_id")
	}
	id, err := uuid.Parse(jobID)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "job id %q: %v", jobID, err)
	}
	if !s.registry.consume(id, token) {
		return uuid.Nil, status.Errorf(codes.PermissionDenied, "stream token for job %s is unknown, expired or already used", id)
	}
	return id, nil
}

// firstMetadata returns the first value of key in md, or "".
func firstMetadata(md metadata.MD, key string) string {
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// pollCancels asks the EventRecorder whether the bound job should cancel
// every CancelPollInterval and sends the directive exactly once; it then
// keeps polling nothing — it returns as soon as the directive went out or
// the stream ended. sent is only touched by this goroutine.
func (s *Server) pollCancels(ctx context.Context, jobID uuid.UUID, stream workerpb.WorkerService_RunEventsServer, done <-chan struct{}) {
	ticker := s.newTicker(s.deps.CancelPollInterval)
	defer ticker.Stop()
	sent := false
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.Tick():
			cancelled, err := s.deps.Events.Cancelled(ctx, jobID)
			if err == nil && cancelled && !sent {
				// Exactly once: after this send the flag keeps every later
				// tick silent, while events keep flowing either way.
				if err := stream.Send(&workerpb.ManagerDirective{Cancel: true}); err != nil {
					return
				}
				sent = true
			}
		}
	}
}

// newTicker builds the poll ticker; injected for tests through Deps.
func (s *Server) newTicker(d time.Duration) Ticker {
	if f := s.deps.NewTicker; f != nil {
		return f(d)
	}
	return &wallTicker{t: time.NewTicker(d)}
}
