// Authentication: the TLS layer verifies a presented client certificate
// against the instance CA, and the interceptor requires one for every
// method except Enroll (a worker's way to its first certificate). It turns
// the certificate into a verified worker identity that both call shapes
// carry into the handler through the context.
package grpcsrv

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

type (
	identityKey struct{}
	workerKey   struct{}
)

// identityOf returns the verified client identity the interceptor stashed
// in ctx, if it ran.
func identityOf(ctx context.Context) (*ca.ClientIdentity, bool) {
	id, ok := ctx.Value(identityKey{}).(*ca.ClientIdentity)
	return id, ok
}

// workerOf returns the registry row the interceptor resolved the identity
// to, if it ran.
func workerOf(ctx context.Context) (workers.Worker, bool) {
	w, ok := ctx.Value(workerKey{}).(workers.Worker)
	return w, ok
}

// unauthenticatedMethods may be called without a client certificate:
// token enrollment is how a worker gets its first one.
var unauthenticatedMethods = map[string]bool{workerpb.WorkerService_Enroll_FullMethodName: true}

// authUnaryInterceptor authenticates a unary call before the handler runs.
func (s *Server) authUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if unauthenticatedMethods[info.FullMethod] {
		return handler(ctx, req)
	}
	ctx, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// authStreamInterceptor authenticates a stream before the handler runs.
// Interceptors cannot change a stream's context directly, so the handler is
// handed a wrapper that overrides Context.
func (s *Server) authStreamInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx, err := s.authenticate(ss.Context())
	if err != nil {
		return err
	}
	return handler(srv, &ctxServerStream{ServerStream: ss, ctx: ctx})
}

// ctxServerStream is the ServerStream wrapper that substitutes the
// authenticated context for the raw one.
type ctxServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *ctxServerStream) Context() context.Context { return s.ctx }

// authenticate turns the presented client certificate into a stashed
// identity and worker row. A missing or untrustworthy certificate is
// Unauthenticated, and so are unknown and revoked serials (spec 2026-09-18
// §4) — one answer, so neither can be told apart — and a certificate whose
// zone SAN differs from the registry row it resolves to.
func (s *Server) authenticate(ctx context.Context) (context.Context, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return ctx, status.Error(codes.Unauthenticated, "no peer credentials")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "peer is not TLS authenticated")
	}
	certs := tlsInfo.State.PeerCertificates // leaf first
	if len(certs) == 0 {
		return ctx, status.Error(codes.Unauthenticated, "no client certificate presented")
	}
	rawCerts := make([][]byte, len(certs))
	for i, c := range certs {
		rawCerts[i] = c.Raw
	}
	identity, err := s.deps.CA.VerifyClient(rawCerts)
	if err != nil {
		// The TLS layer already refuses chains that do not verify, so this
		// path is for a certificate that passed mTLS but carries a
		// malformed identity — still untrustworthy, still Unauthenticated.
		return ctx, status.Errorf(codes.Unauthenticated, "bad client certificate: %v", err)
	}
	worker, err := s.deps.Workers.GetBySerial(ctx, identity.Serial)
	if errors.Is(err, workers.ErrNotFound) || errors.Is(err, workers.ErrRevoked) {
		return ctx, status.Error(codes.Unauthenticated, "unknown or revoked certificate")
	}
	if err != nil {
		return ctx, status.Errorf(codes.Internal, "look up worker: %v", err)
	}
	if worker.ZoneID != identity.ZoneID {
		return ctx, status.Error(codes.Unauthenticated, "certificate does not match the worker's enrollment")
	}
	return context.WithValue(context.WithValue(ctx, identityKey{}, identity), workerKey{}, worker), nil
}

// authenticated returns the handler-side pieces the interceptor stashed.
// A missing piece is a wiring bug rather than something a caller could
// send, but a handler that proceeded without one would be trusting an
// unauthenticated caller — so it fails closed.
func authenticated(ctx context.Context) (*ca.ClientIdentity, workers.Worker, error) {
	identity, okID := identityOf(ctx)
	w, okWorker := workerOf(ctx)
	if !okID || !okWorker {
		return nil, workers.Worker{}, status.Error(codes.PermissionDenied, "unauthenticated call")
	}
	return identity, w, nil
}
