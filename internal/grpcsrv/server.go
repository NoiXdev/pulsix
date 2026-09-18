// Server assembly: the Deps wiring, the mTLS grpc.Server construction
// (a self-renewing server certificate from the internal CA, served with
// the CA; client certificates verified whenever presented and required by
// the interceptor for every method but Enroll) and the in-process
// job→token registry RunEvents binds a stream to the claim that created it.
package grpcsrv

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/clientip"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

// DefaultCancelPollInterval is how often RunEvents asks the
// EventRecorder whether its job should cancel.
const DefaultCancelPollInterval = 500 * time.Millisecond

// tokenTTL bounds how long after a claim its RunEvents stream may still be
// bound: a worker has thirty seconds to open the stream before the token
// expires and the job has to be re-claimed.
const tokenTTL = 30 * time.Second

// Deps wires the server's dependencies. CA and Workers are the concrete
// services the transport needs by value; the rest are the narrow ports
// Task 5 implements. Clock drives the binding-token TTL.
type Deps struct {
	CA      *ca.CA
	Workers *workers.Service
	Claims  Claimer
	Events  EventRecorder
	Results ResultRecorder
	Audit   SystemAuditor
	Clock   func() time.Time

	// CancelPollInterval overrides DefaultCancelPollInterval; zero keeps
	// the default. Injected so tests can poll without waiting half a
	// second per tick.
	CancelPollInterval time.Duration

	// NewTicker builds the cancel-poll ticker; zero uses time.NewTicker.
	// Injected so tests can tick deterministically.
	NewTicker func(time.Duration) Ticker

	// Streams tracks open event streams per worker so a revocation can end
	// them; nil builds a private registry.
	Streams *StreamRegistry

	// ClientIP resolves the Enroll caller's address for the rate limit;
	// nil trusts no proxy.
	ClientIP *clientip.Resolver

	// tls is the transport credentials newServer builds from the CA; not
	// set by callers.
	tls credentials.TransportCredentials
}

// Server implements workerpb.WorkerServiceServer on top of Deps.
type Server struct {
	workerpb.UnimplementedWorkerServiceServer
	deps        Deps
	registry    *tokenRegistry
	streams     *StreamRegistry
	enrollLimit *clientip.Limiter
}

// newServer builds the Server: the gRPC listener's server certificate
// source (orgID nil and workerName empty make Issue take the server-cert
// path: ServerAuth, DNS SAN from the CA config's listen host; re-issued in
// memory as it ages, see servercert.go) and the TLS configuration that
// verifies a client certificate against the instance CA whenever one is
// presented. Whether a method needs one is the interceptor's decision.
func newServer(deps Deps) (*Server, error) {
	if deps.Clock == nil {
		return nil, fmt.Errorf("grpcsrv: Clock must be set")
	}
	if deps.CA == nil || deps.Workers == nil {
		return nil, fmt.Errorf("grpcsrv: CA and Workers must be set")
	}
	caPool, _ := deps.CA.Pool()
	certs, err := newServerCertSource(deps.CA, deps.Clock)
	if err != nil {
		return nil, err
	}
	// A client certificate is verified when presented and required by the
	// interceptor for every method except Enroll (auth.go).
	tlsCfg := &tls.Config{
		GetCertificate: certs.get,
		ClientAuth:     tls.VerifyClientCertIfGiven,
		ClientCAs:      caPool,
		MinVersion:     tls.VersionTLS13,
	}
	deps.tls = credentials.NewTLS(tlsCfg)
	if deps.CancelPollInterval == 0 {
		deps.CancelPollInterval = DefaultCancelPollInterval
	}
	if deps.Streams == nil {
		deps.Streams = NewStreamRegistry()
	}
	if deps.ClientIP == nil {
		deps.ClientIP = clientip.New(nil)
	}
	return &Server{deps: deps, registry: newTokenRegistry(deps.Clock), streams: deps.Streams,
		enrollLimit: clientip.NewLimiter(enrollLimit, enrollWindow, deps.Clock)}, nil
}

// New builds the Server and returns it wrapped in a ready-to-serve
// *grpc.Server with the service registered. The listener is left to the
// caller (internal/app serves on PULSIX_GRPC_LISTEN).
func New(ctx context.Context, deps Deps) (*grpc.Server, error) {
	if err := deps.CA.Ensure(ctx); err != nil {
		return nil, fmt.Errorf("ensure CA for gRPC server: %w", err)
	}
	srv, err := newServer(deps)
	if err != nil {
		return nil, err
	}
	return srv.newGRPCServer(), nil
}

// newGRPCServer builds the *grpc.Server: mTLS credentials, the
// authenticate interceptor on both call shapes, and the WorkerService.
func (s *Server) newGRPCServer() *grpc.Server {
	gs := grpc.NewServer(
		grpc.Creds(s.deps.tls),
		// Explicit, documented message limits (workerpb/limits.go): claims
		// carry whole inventory snapshots, far past gRPC's 4 MiB default.
		grpc.MaxSendMsgSize(workerpb.MaxManagerToWorkerBytes),
		grpc.MaxRecvMsgSize(workerpb.MaxWorkerToManagerBytes),
		grpc.ChainUnaryInterceptor(s.authUnaryInterceptor),
		grpc.ChainStreamInterceptor(s.authStreamInterceptor),
	)
	workerpb.RegisterWorkerServiceServer(gs, s)
	return gs
}

// tokenRegistry maps a claimed job to the binding token its worker must
// present when opening the RunEvents stream. The registry lives in
// process: tokens are single-use, short-lived (tokenTTL) and reissued by
// the next claim, so a restart simply unbinds any stream a worker might
// still want to open.
type tokenRegistry struct {
	mu      sync.Mutex
	entries map[uuid.UUID]boundToken
	now     func() time.Time
}

// boundToken is the claim's token and the instant it expires.
type boundToken struct {
	token     string
	expiresAt time.Time
}

func newTokenRegistry(now func() time.Time) *tokenRegistry {
	return &tokenRegistry{entries: map[uuid.UUID]boundToken{}, now: now}
}

// register records a fresh claim binding, dropping expired ones first so
// the map stays bounded by live claims.
func (r *tokenRegistry) register(jobID uuid.UUID, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for id, b := range r.entries {
		if now.After(b.expiresAt) {
			delete(r.entries, id)
		}
	}
	r.entries[jobID] = boundToken{token: token, expiresAt: now.Add(tokenTTL)}
}

// consume validates the binding (known job, matching token, not expired)
// and removes it — the stream is single-use.
func (r *tokenRegistry) consume(jobID uuid.UUID, token string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.entries[jobID]
	if !ok || b.token != token || r.now().After(b.expiresAt) {
		return false
	}
	delete(r.entries, jobID)
	return true
}

// lookup exposes a binding to tests.
func (r *tokenRegistry) lookup(jobID uuid.UUID) (boundToken, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.entries[jobID]
	return b, ok
}
