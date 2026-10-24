// Authentication: the TLS layer verifies a presented client certificate
// against the instance CA, and the interceptor requires one for every
// method except Enroll (a worker's way to its first certificate). It turns
// the certificate into a verified worker identity that both call shapes
// carry into the handler through the context.
package grpcsrv

import (
	"context"
	"crypto"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

type (
	identityKey struct{}
	workerKey   struct{}
	agentKey    struct{}
)

// identityOf returns the verified client identity the interceptor stashed
// in ctx, if it ran.
// peerKeyKey carries the public key of the certificate the caller
// authenticated with.
type peerKeyKey struct{}

// peerPublicKey returns the key the caller's own certificate carries.
func peerPublicKey(ctx context.Context) (crypto.PublicKey, bool) {
	k := ctx.Value(peerKeyKey{})
	return k, k != nil
}

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

// AgentIdentityOf returns the agent registry row the interceptor resolved
// for an authenticated agent certificate (ca.KindAgent), if it ran.
// Exported for internal/agentapi's Renew and AgentStream handlers.
func AgentIdentityOf(ctx context.Context) (agents.AgentIdentity, bool) {
	a, ok := ctx.Value(agentKey{}).(agents.AgentIdentity)
	return a, ok
}

// unauthenticatedMethods may be called without a client certificate:
// token enrollment is how a worker or agent gets its first one.
// A switch, not a map var: a package-level map is writable by anything
// in the package, and this one decides which RPCs may be called with no
// client certificate at all — CLAUDE.md's "no package-level state"
// (backlog 2.68).
func unauthenticatedMethod(full string) bool {
	switch full {
	case workerpb.WorkerService_Enroll_FullMethodName, agentpb.AgentService_Enroll_FullMethodName:
		return true
	}
	return false
}

// The two service classes sharing the listener. A certificate class is
// segregated by method: an agent certificate is refused on the
// WorkerService and a worker certificate on the AgentService, so a
// compromise of either cannot reach the other's surface. The prefixes are
// pinned against the generated constants by TestMethodKindServicePrefixes.
const (
	workerServicePrefix = "/pulsix.worker.v1.WorkerService/"
	agentServicePrefix  = "/pulsix.agent.v1.AgentService/"
)

// authUnaryInterceptor authenticates a unary call before the handler runs.
func (s *Server) authUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if unauthenticatedMethod(info.FullMethod) {
		return handler(ctx, req)
	}
	ctx, err := s.authenticate(ctx, info.FullMethod)
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// authStreamInterceptor authenticates a stream before the handler runs.
// Interceptors cannot change a stream's context directly, so the handler is
// handed a wrapper that overrides Context.
func (s *Server) authStreamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx, err := s.authenticate(ss.Context(), info.FullMethod)
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
// identity and its registry row. A missing or untrustworthy certificate is
// Unauthenticated, and so are unknown and revoked serials (spec
// 2026-09-18 §4) — one answer, so neither can be told apart — and a
// certificate whose org or zone SAN differs from the registry row it
// resolves to. The certificate's kind decides which registry resolves the
// serial (worker or agent, v0.3a); a kind the method's service does not
// serve is PermissionDenied.
func (s *Server) authenticate(ctx context.Context, fullMethod string) (context.Context, error) {
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
	if err := s.checkMethodKind(identity, fullMethod); err != nil {
		return ctx, err
	}
	switch identity.Kind {
	case ca.KindWorker:
		worker, err := s.lookupWorker(ctx, identity)
		if err != nil {
			return ctx, err
		}
		// The presented leaf's public key travels with the identity:
		// RenewCertificate must check that the CSR it is handed asks
		// for THIS key and not another (backlog 2.64).
		ctx = context.WithValue(ctx, peerKeyKey{}, certs[0].PublicKey)
		return context.WithValue(context.WithValue(ctx, identityKey{}, identity), workerKey{}, worker), nil
	case ca.KindAgent:
		agent, err := s.lookupAgent(ctx, identity)
		if err != nil {
			return ctx, err
		}
		return context.WithValue(context.WithValue(ctx, identityKey{}, identity), agentKey{}, agent), nil
	default:
		return ctx, status.Error(codes.Unauthenticated, "bad client certificate: unknown certificate kind")
	}
}

// checkMethodKind refuses a certificate whose class does not match the
// service the method belongs to: WorkerService/* wants KindWorker,
// AgentService/* wants KindAgent. A method of neither service carries no
// class requirement. The check runs before any registry lookup, so a
// mis-classed certificate costs nothing and the refusal is unambiguous.
func (s *Server) checkMethodKind(identity *ca.ClientIdentity, fullMethod string) error {
	switch {
	case strings.HasPrefix(fullMethod, agentServicePrefix):
		if identity.Kind != ca.KindAgent {
			return status.Error(codes.PermissionDenied, "this method requires an agent certificate")
		}
	case strings.HasPrefix(fullMethod, workerServicePrefix):
		if identity.Kind != ca.KindWorker {
			return status.Error(codes.PermissionDenied, "this method requires a worker certificate")
		}
	}
	return nil
}

// lookupWorker resolves identity's serial to its registry row and checks
// that the certificate's org and zone SANs match that row. Unknown and
// revoked serials and a mismatch are all Unauthenticated; a registry fault
// is a generic Internal (logged, never echoed to the caller).
func (s *Server) lookupWorker(ctx context.Context, identity *ca.ClientIdentity) (workers.Worker, error) {
	worker, err := s.deps.Workers.GetBySerial(ctx, identity.Serial)
	if errors.Is(err, workers.ErrNotFound) || errors.Is(err, workers.ErrRevoked) {
		return workers.Worker{}, status.Error(codes.Unauthenticated, "unknown or revoked certificate")
	}
	if err != nil {
		s.log().ErrorContext(ctx, "look up worker certificate", "serial", identity.Serial.String(), "err", err)
		return workers.Worker{}, status.Error(codes.Internal, "worker lookup failed")
	}
	if identity.OrgID == nil || *identity.OrgID != worker.OrgID || identity.ZoneID != worker.ZoneID {
		return workers.Worker{}, status.Error(codes.Unauthenticated, "certificate does not match the worker's enrollment")
	}
	return worker, nil
}

// lookupAgent is lookupWorker's agent counterpart (v0.3a): the serial is
// resolved in the agent registry, the certificate's org SAN must match the
// row's organization (agent certificates carry no zone SAN). Revoked
// serials read exactly like unknown ones.
func (s *Server) lookupAgent(ctx context.Context, identity *ca.ClientIdentity) (agents.AgentIdentity, error) {
	if s.deps.Agents == nil {
		s.log().ErrorContext(ctx, "agent certificate presented but no agent registry is wired")
		return agents.AgentIdentity{}, status.Error(codes.Internal, "agent lookup failed")
	}
	agent, err := s.deps.Agents.GetBySerial(ctx, identity.Serial.String())
	if errors.Is(err, agents.ErrNotFound) {
		return agents.AgentIdentity{}, status.Error(codes.Unauthenticated, "unknown or revoked certificate")
	}
	if err != nil {
		s.log().ErrorContext(ctx, "look up agent certificate", "serial", identity.Serial.String(), "err", err)
		return agents.AgentIdentity{}, status.Error(codes.Internal, "agent lookup failed")
	}
	if agent.Revoked {
		return agents.AgentIdentity{}, status.Error(codes.Unauthenticated, "unknown or revoked certificate")
	}
	if identity.OrgID == nil || *identity.OrgID != agent.OrgID {
		return agents.AgentIdentity{}, status.Error(codes.Unauthenticated, "certificate does not match the agent's enrollment")
	}
	return agent, nil
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
