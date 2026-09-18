package worker

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// reconnectGrace bounds how long a superseded connection is kept open
// after Redial, so a RunEvents stream already in flight on it (an
// ansible run under way) is not cut off the moment new calls move to the
// fresh connection. It intentionally outlives almost every real run — a
// run still going after it is recovered exactly like a worker that died
// mid-run, through the manager's existing claim/requeue path.
const reconnectGrace = 30 * time.Minute

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
// A call already in flight when Swap runs is unaffected: it was issued
// against a concrete client value captured at call time (in particular, a
// RunEvents stream's Send/Recv keep using the connection that opened it),
// not against this wrapper.
type ReconnectingClient struct {
	cur atomic.Pointer[workerpb.WorkerServiceClient]

	mu   sync.Mutex
	conn *grpc.ClientConn
	log  *slog.Logger
}

// NewReconnectingClient wraps conn's client as the current one.
func NewReconnectingClient(conn *grpc.ClientConn, log *slog.Logger) *ReconnectingClient {
	rc := &ReconnectingClient{conn: conn, log: log}
	c := workerpb.WorkerServiceClient(workerpb.NewWorkerServiceClient(conn))
	rc.cur.Store(&c)
	return rc
}

// Redial dials a fresh connection through dial and, on success, moves
// every future call to it; the connection it replaces is closed after
// reconnectGrace, not immediately, so an in-flight stream can finish. A
// dial failure changes nothing and is logged: the worker keeps running on
// its current connection, and the next renewal check tries again.
func (r *ReconnectingClient) Redial(ctx context.Context, dial func(ctx context.Context) (*grpc.ClientConn, error)) {
	newConn, err := dial(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "reconnect with the renewed certificate failed; the current connection keeps working until it next cycles (or the manager cycles it via its own MaxConnectionAge)", "err", err)
		return
	}
	r.mu.Lock()
	old := r.conn
	r.conn = newConn
	r.mu.Unlock()
	c := workerpb.WorkerServiceClient(workerpb.NewWorkerServiceClient(newConn))
	r.cur.Store(&c)
	r.log.InfoContext(ctx, "reconnected with the renewed certificate")
	go func() {
		time.Sleep(reconnectGrace)
		_ = old.Close()
	}()
}

func (r *ReconnectingClient) client() workerpb.WorkerServiceClient { return *r.cur.Load() }

func (r *ReconnectingClient) ClaimJob(ctx context.Context, in *workerpb.ClaimJobRequest, opts ...grpc.CallOption) (*workerpb.ClaimJobResponse, error) {
	return r.client().ClaimJob(ctx, in, opts...)
}

func (r *ReconnectingClient) RunEvents(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[workerpb.RunEvent, workerpb.ManagerDirective], error) {
	return r.client().RunEvents(ctx, opts...)
}

func (r *ReconnectingClient) ReportResult(ctx context.Context, in *workerpb.ReportResultRequest, opts ...grpc.CallOption) (*workerpb.ReportResultResponse, error) {
	return r.client().ReportResult(ctx, in, opts...)
}

func (r *ReconnectingClient) Enroll(ctx context.Context, in *workerpb.EnrollRequest, opts ...grpc.CallOption) (*workerpb.EnrollResponse, error) {
	return r.client().Enroll(ctx, in, opts...)
}

func (r *ReconnectingClient) RenewCertificate(ctx context.Context, in *workerpb.RenewCertificateRequest, opts ...grpc.CallOption) (*workerpb.RenewCertificateResponse, error) {
	return r.client().RenewCertificate(ctx, in, opts...)
}
