package worker

import (
	"context"
	"log/slog"
	"sync"

	"google.golang.org/grpc"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// connGen is one generation of ReconnectingClient's underlying
// connection: the *grpc.ClientConn, its client and a count of calls
// still in flight on it. A generation is closed only once inFlight
// reaches zero — see ReconnectingClient.Redial.
type connGen struct {
	conn     *grpc.ClientConn
	client   workerpb.WorkerServiceClient
	inFlight sync.WaitGroup
}

// ReconnectingClient is a workerpb.WorkerServiceClient whose underlying
// connection can be swapped without the caller knowing: the claim loop
// and the renewer each hold this one value for the worker's whole
// lifetime, so a certificate renewal can move future calls onto a fresh
// connection (Redial) without restarting either.
//
// This exists because tls.Config.GetClientCertificate only runs on a new
// handshake — a connection already established before a renewal keeps
// presenting the certificate it handshook with. The manager re-verifies
// that certificate's validity window on every RPC, so a long-lived
// connection would otherwise start failing every call the moment its
// original (pre-renewal) leaf expires, even though a fresh one has sat
// ready in Identity for weeks. Redialing after each successful renewal
// keeps every connection's negotiated certificate close to its issue
// date; the server's own MaxConnectionAge (internal/grpcsrv/server.go) is
// the backstop for a redial that silently fails.
//
// A call already in flight when Redial runs is unaffected: it was issued
// against the connection generation captured at call time — in
// particular, a RunEvents stream's Send/Recv keep using the transport
// that opened it, for as long as that run takes, however long that is.
// The superseded generation's connection is closed only once every call
// and stream opened on it has finished (tracked by connGen.inFlight), or
// at Close (process shutdown) — never on a blind timer, which would cut
// an ansible run that happened to still be going.
//
// The current generation is read and its inFlight count incremented
// (acquire) under the SAME mutex Redial uses to swap generations, as one
// step. Without that, a call could read the about-to-be-superseded
// generation, and Redial's cleanup goroutine could observe its count at
// zero and start closing it, both before the call's own Add(1) lands —
// violating sync.WaitGroup's contract that an Add from zero happens
// before the matching Wait returns, and racing the connection's Close
// against a call still using it (fix round 3, New Breakage 2 in the
// second re-review).
type ReconnectingClient struct {
	mu  sync.Mutex
	cur *connGen
	log *slog.Logger
}

// NewReconnectingClient wraps conn's client as the current one.
func NewReconnectingClient(conn *grpc.ClientConn, log *slog.Logger) *ReconnectingClient {
	return &ReconnectingClient{cur: &connGen{conn: conn, client: workerpb.NewWorkerServiceClient(conn)}, log: log}
}

// acquire returns the current generation with its in-flight count
// already incremented for the duration of one call. Every method below
// calls it instead of reading cur directly — see the type's own doc
// comment for why the read and the Add must be one atomic step.
func (r *ReconnectingClient) acquire() *connGen {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.cur
	g.inFlight.Add(1)
	return g
}

// Redial dials a fresh connection through dial and, on success, moves
// every future call to it. The connection it replaces is closed once it
// goes idle (every call and stream already open on it has finished) or
// ctx ends (worker shutdown), whichever comes first — so an in-flight
// RunEvents stream for a run still going is never cut off by a redial. A
// dial failure changes nothing and is logged: the worker keeps running on
// its current connection, and the next renewal check tries again.
func (r *ReconnectingClient) Redial(ctx context.Context, dial func(ctx context.Context) (*grpc.ClientConn, error)) {
	newConn, err := dial(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "reconnect with the renewed certificate failed; the current connection keeps working until it next cycles (or the manager cycles it via its own MaxConnectionAge)", "err", err)
		return
	}
	newGen := &connGen{conn: newConn, client: workerpb.NewWorkerServiceClient(newConn)}
	r.mu.Lock()
	old := r.cur
	r.cur = newGen
	r.mu.Unlock()
	r.log.InfoContext(ctx, "reconnected with the renewed certificate")
	go closeWhenIdle(ctx, old)
}

// Close closes the current connection. cmd/worker/main.go and the
// embedded worker defer it at shutdown instead of closing the original
// *grpc.ClientConn directly, because Redial may have moved "current" to
// a different one since dial time.
func (r *ReconnectingClient) Close() error {
	r.mu.Lock()
	g := r.cur
	r.mu.Unlock()
	return g.conn.Close()
}

// closeWhenIdle waits for gen's in-flight calls and streams to drain, or
// for ctx to end (worker shutdown — nothing is worth waiting for anymore
// at that point), and closes its connection either way.
func closeWhenIdle(ctx context.Context, gen *connGen) {
	idle := make(chan struct{})
	go func() {
		gen.inFlight.Wait()
		close(idle)
	}()
	select {
	case <-idle:
	case <-ctx.Done():
	}
	_ = gen.conn.Close()
}

func (r *ReconnectingClient) ClaimJob(ctx context.Context, in *workerpb.ClaimJobRequest, opts ...grpc.CallOption) (*workerpb.ClaimJobResponse, error) {
	g := r.acquire()
	defer g.inFlight.Done()
	return g.client.ClaimJob(ctx, in, opts...)
}

func (r *ReconnectingClient) ReportResult(ctx context.Context, in *workerpb.ReportResultRequest, opts ...grpc.CallOption) (*workerpb.ReportResultResponse, error) {
	g := r.acquire()
	defer g.inFlight.Done()
	return g.client.ReportResult(ctx, in, opts...)
}

func (r *ReconnectingClient) Enroll(ctx context.Context, in *workerpb.EnrollRequest, opts ...grpc.CallOption) (*workerpb.EnrollResponse, error) {
	g := r.acquire()
	defer g.inFlight.Done()
	return g.client.Enroll(ctx, in, opts...)
}

func (r *ReconnectingClient) RenewCertificate(ctx context.Context, in *workerpb.RenewCertificateRequest, opts ...grpc.CallOption) (*workerpb.RenewCertificateResponse, error) {
	g := r.acquire()
	defer g.inFlight.Done()
	return g.client.RenewCertificate(ctx, in, opts...)
}

// RunEvents opens a stream on the CURRENT generation and keeps that
// generation "in flight" (so Redial's cleanup will not close its
// connection) until the stream itself ends — Recv returning any error,
// per gRPC's own client-streaming contract, is that signal — not until
// this call returns, which happens almost immediately while the run
// keeps streaming for as long as it takes.
func (r *ReconnectingClient) RunEvents(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[workerpb.RunEvent, workerpb.ManagerDirective], error) {
	g := r.acquire()
	stream, err := g.client.RunEvents(ctx, opts...)
	if err != nil {
		g.inFlight.Done()
		return nil, err
	}
	return &trackedStream{BidiStreamingClient: stream, gen: g}, nil
}

// trackedStream marks its generation done the first time Recv reports
// the stream has ended (an error, including io.EOF), whichever of
// possibly several goroutines calling Recv observes it first.
type trackedStream struct {
	grpc.BidiStreamingClient[workerpb.RunEvent, workerpb.ManagerDirective]
	gen  *connGen
	once sync.Once
}

func (s *trackedStream) Recv() (*workerpb.ManagerDirective, error) {
	m, err := s.BidiStreamingClient.Recv()
	if err != nil {
		s.once.Do(s.gen.inFlight.Done)
	}
	return m, err
}
