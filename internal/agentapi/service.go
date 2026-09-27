// Package agentapi is the manager-side gRPC service for host agents
// (v0.3a, spec §8): Enroll exchanges a pxa1 enrollment token for the
// agent's first client certificate (binding to a pre-created host or
// creating one), Renew swaps the certificate at two-thirds lifetime, and
// AgentStream is the agent's persistent bidi connection (heartbeats
// refresh last_seen, facts flow to the FactsSink port, every processed
// message is acknowledged).
//
// The service is a transport, like internal/grpcsrv whose listener it
// rides: it composes the agents service (registry + tokens), the CA and
// the audit recorder through narrow constructor-injected dependencies and
// carries no business rules of its own. The auth interceptor in grpcsrv
// refuses agent certificates on the worker surface (and vice versa) and
// stashes the resolved agent identity for this package's authenticated
// methods.
package agentapi

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/clientip"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/grpcsrv"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// The agent Enroll rate limit mirrors the worker one (grpcsrv's
// enrollLimit/enrollWindow): at most 10 enrollment attempts per client
// address per minute, counted in process on the same clientip.Limiter
// mechanics (a shared bucket would couple the two surfaces' budgets).
const (
	agentEnrollLimit  = 10
	agentEnrollWindow = time.Minute
)

// AgentSigner is the slice of the CA the service needs: issue the agent
// certificate for a CSR (the agent keeps its key) and supply the CA PEM
// the responses carry. *ca.CA is the production implementation.
type AgentSigner interface {
	SignAgentCSR(csrPEM []byte, orgID uuid.UUID, agentName string) ([]byte, *x509.Certificate, error)
	CAPEM() []byte
}

// FactsSink is where agent-reported facts land (Task 8's facts service:
// hosts.facts merge + auto tags). It is deliberately narrow so Task 7 can
// ship without Task 8: a nil sink drops facts with a server log and still
// acknowledges them.
type FactsSink interface {
	RecordFacts(ctx context.Context, orgID, hostID uuid.UUID, facts map[string]string) error
}

// ChecksPort is the slice of the checks service the stream needs:
// resolving a host's configuration (to compare against what the agent
// reports and push on a mismatch) and recording the results an agent
// sends back. *checks.Service is the production implementation. Nil, the
// same posture as FactsSink, drops both halves with a server log: a
// heartbeat is still acknowledged with no CheckConfig, and CheckResults
// are still acknowledged without being stored — the checks domain can
// wire in independently of this package shipping.
type ChecksPort interface {
	ResolveHost(ctx context.Context, orgID, hostID uuid.UUID) (checks.ResolvedSet, error)
	// ResolveHosts computes several named hosts' configuration in one
	// pass, and ResolveOrg every monitored host's — both sharing a single
	// load of the definitions, the groups and each group's expansion
	// across all of them. PushHosts and PushOrg need them so a change
	// reaching many hosts costs one resolver pass rather than one per
	// host; ResolveHost stays for the single-host heartbeat comparison,
	// which is genuinely about one host.
	ResolveHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]checks.ResolvedSet, error)
	ResolveOrg(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]checks.ResolvedSet, error)
	RecordObservations(ctx context.Context, orgID uuid.UUID, obs []checks.Observation) (accepted, rejected int, err error)
}

// Deps wires the service's dependencies.
type Deps struct {
	// CA signs the agent's certificate requests and supplies the CA PEM
	// the responses carry.
	CA AgentSigner
	// Agents is the agent registry service (tokens, claims, registrations,
	// serial lookups, touches, revocations).
	Agents *agents.Service
	// Audit records the agent.enroll / agent.renew system events.
	Audit grpcsrv.SystemAuditor
	// Streams tracks open AgentStream connections so a revocation can end
	// them; nil builds a private registry. The same registry must be wired
	// as the agents service's revoke hook (SetRevokeHook) in app wiring.
	Streams *StreamRegistry
	// Facts receives agent-reported facts; nil drops them (Task 8 wires
	// the facts service here).
	Facts FactsSink
	// Checks resolves check configuration and records reported results;
	// nil drops both (see ChecksPort's own doc comment).
	Checks ChecksPort
	// Clock drives the enrollment rate limit; nil uses time.Now.
	Clock func() time.Time
	// ClientIP resolves the Enroll caller's address for the rate limit;
	// nil trusts no proxy.
	ClientIP *clientip.Resolver
}

// Service implements agentpb.AgentServiceServer on top of Deps.
type Service struct {
	agentpb.UnimplementedAgentServiceServer
	deps      Deps
	streams   *StreamRegistry
	enrollLim *clientip.Limiter
}

// New validates the wiring and builds the Service.
func New(deps Deps) (*Service, error) {
	if deps.CA == nil || deps.Agents == nil {
		return nil, fmt.Errorf("agentapi: CA and Agents must be set")
	}
	if deps.Audit == nil {
		return nil, fmt.Errorf("agentapi: Audit must be set")
	}
	if deps.Clock == nil {
		deps.Clock = time.Now
	}
	if deps.Streams == nil {
		deps.Streams = NewStreamRegistry()
	}
	if deps.ClientIP == nil {
		deps.ClientIP = clientip.New(nil)
	}
	return &Service{deps: deps, streams: deps.Streams,
		enrollLim: clientip.NewLimiter(agentEnrollLimit, agentEnrollWindow, deps.Clock)}, nil
}

// Streams exposes the stream registry for the app wiring (it is the
// agents service's StreamCutter).
func (s *Service) Streams() *StreamRegistry { return s.streams }

// Enroll implements agentpb.AgentServiceServer.Enroll. It is the one
// agent method callable without a client certificate; the token is the
// credential, and the rate limit bounds how hard one address can lean on
// it. Enroll is anonymous, so the store's error text is logged, never sent
// back (the same posture as the worker Enroll).
func (s *Service) Enroll(ctx context.Context, req *agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
	ip := s.peerAddr(ctx)
	if !s.enrollLim.Allow(ip) {
		return nil, status.Error(codes.ResourceExhausted, "too many enrollment attempts from this address; retry in a minute")
	}
	tok, err := enrolltoken.ParseAgent(req.GetToken())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "enrollment refused")
	}
	// The certificate request is checked before anything is consumed: a
	// rejected request must not burn a token use (the claim below commits
	// its use before the signing step runs). The same holds for the
	// presented name — it names the certificate in the bind flow and in a
	// hintless create; a token hint's length is enforced inside the claim,
	// which rolls back.
	if _, err := ca.CheckCSR([]byte(req.GetCsrPem())); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if name := req.GetName(); name != "" {
		if err := ca.CheckAgentName(name); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
	}
	// The token's organization scopes everything that follows (the claim
	// and the audit record); an unknown token is indistinguishable from a
	// wrong secret or an expired one — one refusal.
	orgID, err := s.deps.Agents.EnrollmentOrg(ctx, tok.ID)
	if errors.Is(err, agents.ErrTokenInvalid) {
		return nil, status.Error(codes.Unauthenticated, "enrollment refused")
	}
	if err != nil {
		slog.ErrorContext(ctx, "resolve agent enrollment org", "token_id", tok.ID.String(), "err", err)
		return nil, status.Error(codes.Internal, "enrollment failed")
	}
	res, err := s.deps.Agents.ClaimToken(ctx, orgID, tok, req.GetName())
	switch {
	case errors.Is(err, agents.ErrTokenInvalid):
		return nil, status.Error(codes.Unauthenticated, "enrollment refused")
	case errors.Is(err, agents.ErrTokenConflict):
		return nil, status.Error(codes.AlreadyExists, "a host with this name already exists in the organization")
	case errors.Is(err, agents.ErrNotFound):
		return nil, status.Error(codes.NotFound, "the token's bound host does not exist")
	case errors.Is(err, agents.ErrInvalidInput):
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	case err != nil:
		slog.ErrorContext(ctx, "claim agent enrollment", "token_id", tok.ID.String(), "err", err)
		return nil, status.Error(codes.Internal, "enrollment failed")
	}
	// The certificate carries the name the enrollment resolved: a created
	// host is named by the token's hint (which therefore names the agent
	// too); a bound host keeps its own name, so the agent's presented name
	// stands in. Register refuses an empty name (the certificate needs
	// one), and the CA refuses names past 64 runes.
	name := res.Name
	if name == "" {
		name = req.GetName()
	}
	certPEM, crt, err := s.deps.CA.SignAgentCSR([]byte(req.GetCsrPem()), orgID, name)
	switch {
	case errors.Is(err, ca.ErrBadCSR), errors.Is(err, ca.ErrInvalidInput):
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	case err != nil:
		slog.ErrorContext(ctx, "sign agent certificate", "token_id", tok.ID.String(), "err", err)
		return nil, status.Error(codes.Internal, "enrollment failed")
	}
	if err := s.deps.Agents.Register(ctx, agents.Registration{
		OrgID: orgID, HostID: res.HostID, AgentName: name, Version: req.GetVersion(),
		Serial: crt.SerialNumber.String(), NotBefore: crt.NotBefore, NotAfter: crt.NotAfter,
	}); err != nil {
		slog.ErrorContext(ctx, "register agent", "token_id", tok.ID.String(), "host_id", res.HostID, "err", err)
		return nil, status.Error(codes.Internal, "enrollment failed")
	}
	// Re-enrollment replaces the host's active agent: the swap revoked the
	// replaced serial, so its open streams end here. The fresh serial has
	// no streams yet, and a renewal (the same Register) is not given this
	// cut — the renewing agent keeps its stream.
	s.streams.CloseHostExcept(res.HostID, crt.SerialNumber.String())
	s.audit(ctx, grpcsrv.SystemEvent{Operation: "agent.enroll", ObjectType: "agent", ObjectID: res.HostID.String(),
		OrgID: orgID.String(), Summary: "agent " + name + " enrolled on host " + res.HostID.String() + " from " + ip.String()})
	return &agentpb.EnrollResponse{CertPem: string(certPEM), CaPem: string(s.deps.CA.CAPEM()), HostId: res.HostID.String()}, nil
}

// Renew implements agentpb.AgentServiceServer.Renew. The interceptor has
// already verified the calling certificate and resolved its live serial
// (unknown and revoked serials never reach the handler); the fresh
// certificate is issued for the agent's renewed key and the registry serial
// swaps in one transaction (agents.Register revokes the previous serials) —
// the old certificate is refused from then on, which is why the agent
// reconnects with the new one immediately.
func (s *Service) Renew(ctx context.Context, req *agentpb.RenewRequest) (*agentpb.EnrollResponse, error) {
	agent, ok := grpcsrv.AgentIdentityOf(ctx)
	if !ok {
		return nil, status.Error(codes.PermissionDenied, "unauthenticated call")
	}
	certPEM, crt, err := s.deps.CA.SignAgentCSR([]byte(req.GetCsrPem()), agent.OrgID, agent.Name)
	switch {
	case errors.Is(err, ca.ErrBadCSR), errors.Is(err, ca.ErrInvalidInput):
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	case err != nil:
		slog.ErrorContext(ctx, "sign renewed agent certificate", "host_id", agent.HostID, "err", err)
		return nil, status.Error(codes.Internal, "certificate renewal failed")
	}
	if err := s.deps.Agents.Register(ctx, agents.Registration{
		OrgID: agent.OrgID, HostID: agent.HostID, AgentName: agent.Name, Version: agent.Version,
		Serial: crt.SerialNumber.String(), NotBefore: crt.NotBefore, NotAfter: crt.NotAfter,
	}); err != nil {
		slog.ErrorContext(ctx, "swap agent certificate serial", "host_id", agent.HostID, "err", err)
		return nil, status.Error(codes.Internal, "certificate renewal failed")
	}
	s.audit(ctx, grpcsrv.SystemEvent{Operation: "agent.renew", ObjectType: "agent", ObjectID: agent.HostID.String(),
		OrgID: agent.OrgID.String(), Summary: "agent " + agent.Name + " renewed its certificate"})
	return &agentpb.EnrollResponse{CertPem: string(certPEM), CaPem: string(s.deps.CA.CAPEM()), HostId: agent.HostID.String()}, nil
}

// audit records a system event; a failure is logged rather than undoing
// the enrollment or renewal it documents.
func (s *Service) audit(ctx context.Context, ev grpcsrv.SystemEvent) {
	if err := s.deps.Audit.Record(ctx, ev); err != nil {
		slog.ErrorContext(ctx, "audit agent certificate event", "operation", ev.Operation, "err", err)
	}
}

// peerAddr is the caller's address through the trusted-proxy rules (the
// same resolution the worker Enroll limits by).
func (s *Service) peerAddr(ctx context.Context) netip.Addr {
	remote := ""
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		remote = p.Addr.String()
	}
	var xff []string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		xff = md.Get("x-forwarded-for")
	}
	return s.deps.ClientIP.Resolve(remote, xff)
}
