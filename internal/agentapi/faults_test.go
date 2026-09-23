// Fault paths and direct handler calls: the server-fault branches the
// bufconn round-trips cannot reach (injected store, signer and audit
// faults) and the fail-closed guards that the interceptor makes
// unreachable over the wire.
package agentapi

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// TestNewDefaults: a wiring without clock, registry or resolver builds —
// the constructor fills them in.
func TestNewDefaults(t *testing.T) {
	h := newHarness(t)
	svc, err := New(Deps{CA: h.signer, Agents: h.agentSvc, Audit: h.audit})
	if err != nil {
		t.Fatal(err)
	}
	if svc.streams == nil || svc.enrollLim == nil {
		t.Fatal("New must build the default registry and limiter")
	}
}

// TestEnrollOrgFaultIsInternal: a registry fault behind the anonymous
// Enroll is a generic Internal, its text never echoed.
func TestEnrollOrgFaultIsInternal(t *testing.T) {
	h := newHarness(t)
	h.store.mu.Lock()
	h.store.orgErr = errors.New(`relation "agent_enrollments" does not exist`)
	h.store.mu.Unlock()
	tok := h.enrollToken(testOrg, tokenInput("n"))
	_, csr := newKeyAndCSR(t)
	_, err := h.enroll(tok.raw, csr, "x")
	if status.Code(err) != codes.Internal {
		t.Fatalf("org fault: %v, want Internal", err)
	}
	if strings.Contains(status.Convert(err).Message(), "agent_enrollments") {
		t.Fatalf("the store's error text must not reach the caller: %v", err)
	}
}

// TestEnrollClaimFaultIsInternal: a claim fault after the closure ran is a
// server fault.
func TestEnrollClaimFaultIsInternal(t *testing.T) {
	h := newHarness(t)
	h.store.mu.Lock()
	h.store.claimErr = errors.New("db down")
	h.store.mu.Unlock()
	tok := h.enrollToken(testOrg, tokenInput("n"))
	_, csr := newKeyAndCSR(t)
	if _, err := h.enroll(tok.raw, csr, "x"); status.Code(err) != codes.Internal {
		t.Fatalf("claim fault: %v, want Internal", err)
	}
}

// TestEnrollSignFaultIsInternal: a signer fault (entropy, readiness) is a
// server fault.
func TestEnrollSignFaultIsInternal(t *testing.T) {
	h := newHarness(t)
	h.signer.err = errors.New("entropy exhausted")
	tok := h.enrollToken(testOrg, tokenInput("n"))
	_, csr := newKeyAndCSR(t)
	if _, err := h.enroll(tok.raw, csr, "x"); status.Code(err) != codes.Internal {
		t.Fatalf("sign fault: %v, want Internal", err)
	}
}

// TestEnrollRegisterFaultIsInternal: the registration's store fault is a
// server fault — the token use is spent, the enroll failed.
func TestEnrollRegisterFaultIsInternal(t *testing.T) {
	h := newHarness(t)
	h.store.mu.Lock()
	h.store.registerErr = errors.New("db down")
	h.store.mu.Unlock()
	tok := h.enrollToken(testOrg, tokenInput("n"))
	_, csr := newKeyAndCSR(t)
	if _, err := h.enroll(tok.raw, csr, "x"); status.Code(err) != codes.Internal {
		t.Fatalf("register fault: %v, want Internal", err)
	}
}

// TestEnrollAuditFaultIsLogged: an audit failure does not undo the
// enrollment — the agent's certificate exists and is returned.
func TestEnrollAuditFaultIsLogged(t *testing.T) {
	h := newHarness(t)
	h.audit.mu.Lock()
	h.audit.recordErr = errors.New("db down")
	h.audit.mu.Unlock()
	tok := h.enrollToken(testOrg, tokenInput("n"))
	_, csr := newKeyAndCSR(t)
	resp, err := h.enroll(tok.raw, csr, "x")
	if err != nil || resp.GetHostId() == "" {
		t.Fatalf("enroll with an audit fault = %v, %v", resp, err)
	}
}

// TestRenewFaultsAreInternal: renewal's server faults are generic
// Internal — the signer's and the serial swap's.
func TestRenewFaultsAreInternal(t *testing.T) {
	h := newHarness(t)
	_, client := enrollAnAgent(t, h, "web-1")
	_, csr := newKeyAndCSR(t)

	h.signer.err = errors.New("entropy exhausted")
	if _, err := (*client).Renew(t.Context(), &agentpb.RenewRequest{CsrPem: csr}); status.Code(err) != codes.Internal {
		t.Fatalf("renew sign fault: %v, want Internal", err)
	}
	h.signer.err = nil

	h.store.mu.Lock()
	h.store.registerErr = errors.New("db down")
	h.store.mu.Unlock()
	if _, err := (*client).Renew(t.Context(), &agentpb.RenewRequest{CsrPem: csr}); status.Code(err) != codes.Internal {
		t.Fatalf("renew swap fault: %v, want Internal", err)
	}
}

// TestUnauthenticatedHandlersFailClosed: Renew and AgentStream trust the
// interceptor's stash; a handler called without it (a wiring bug) refuses
// instead of proceeding. Direct calls, no interceptor.
func TestUnauthenticatedHandlersFailClosed(t *testing.T) {
	h := newHarness(t)
	_, csr := newKeyAndCSR(t)
	if _, err := h.svc.Renew(context.Background(), &agentpb.RenewRequest{CsrPem: csr}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bare Renew: %v, want PermissionDenied", err)
	}
	if err := h.svc.AgentStream(&scriptedAgentStream{ctx: context.Background()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bare AgentStream: %v, want PermissionDenied", err)
	}
}

// scriptedAgentStream is an AgentStream server stream without a transport:
// Recv answers from a script, Send fails on demand.
type scriptedAgentStream struct {
	grpc.ServerStream
	ctx     context.Context
	recv    []func() (*agentpb.AgentMessage, error)
	sendErr error
	n       int
}

func (s *scriptedAgentStream) Context() context.Context { return s.ctx }

func (s *scriptedAgentStream) Recv() (*agentpb.AgentMessage, error) {
	s.n++
	if s.n > len(s.recv) {
		return nil, io.EOF
	}
	return s.recv[s.n-1]()
}

func (s *scriptedAgentStream) Send(*agentpb.ManagerMessage) error { return s.sendErr }

// testIdentity is the stashed-identity stand-in for direct receive-loop
// calls.
func testIdentity() agents.AgentIdentity {
	return agents.AgentIdentity{Serial: "serial-x", OrgID: testOrg, HostID: uuid.Must(uuid.NewV7()), Name: "web-1"}
}

// TestReceiveMessagesPassesTransportErrorsThrough: a Recv failure (the RPC
// ending) is returned as-is, not wrapped.
func TestReceiveMessagesPassesTransportErrorsThrough(t *testing.T) {
	h := newHarness(t)
	gone := status.Error(codes.Unavailable, "transport gone")
	stream := &scriptedAgentStream{ctx: t.Context(), recv: []func() (*agentpb.AgentMessage, error){
		func() (*agentpb.AgentMessage, error) { return nil, gone },
	}}
	err := h.svc.receiveMessages(t.Context(), testIdentity(), stream, &trackedStream{revoked: make(chan struct{})})
	if !errors.Is(err, gone) {
		t.Fatalf("transport error = %v, want the status passed through", err)
	}
}

// TestReceiveMessagesSendFaultEndsTheLoop: a failed acknowledgement ends
// the stream — the agent is unreachable.
func TestReceiveMessagesSendFaultEndsTheLoop(t *testing.T) {
	h := newHarness(t)
	sendErr := errors.New("send failed")
	stream := &scriptedAgentStream{ctx: t.Context(), sendErr: sendErr, recv: []func() (*agentpb.AgentMessage, error){
		func() (*agentpb.AgentMessage, error) {
			return &agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{Heartbeat: &agentpb.Heartbeat{Version: "v"}}}, nil
		},
	}}
	err := h.svc.receiveMessages(t.Context(), testIdentity(), stream, &trackedStream{revoked: make(chan struct{})})
	if !errors.Is(err, sendErr) {
		t.Fatalf("send fault = %v, want it to end the loop", err)
	}
}

// TestEnrollOverlongHintRefusesPreClaim: a hint the CA cannot put behind
// the pulsix-agent common name (past 64 runes) is refused by the claim
// itself — before the use is consumed, before a host is created, before
// anything is flipped.
func TestEnrollOverlongHintRefusesPreClaim(t *testing.T) {
	h := newHarness(t)
	tok := h.enrollToken(testOrg, tokenInput(strings.Repeat("a", 65)))
	_, csr := newKeyAndCSR(t)
	if _, err := h.enroll(tok.raw, csr, "short"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("overlong hint: %v, want InvalidArgument", err)
	}
	if uses, used := h.store.useOf(tok.parsed.ID); uses != 0 || used {
		t.Fatalf("the refusal burned a use: uses=%d used=%v", uses, used)
	}
	if len(h.prov.hosts[testOrg]) != 0 || len(h.prov.monitored) != 0 {
		t.Fatalf("the refusal touched hosts: %v / %v", h.prov.hosts[testOrg], h.prov.monitored)
	}
}

// TestEnrollBindFlowLongNameRefused: a bound token with an over-long
// presented name is refused before the claim — the use is not burned and
// the bound host is untouched.
func TestEnrollBindFlowLongNameRefused(t *testing.T) {
	h := newHarness(t)
	bound := h.prov.seedHost(testOrg, "web-1")
	in := tokenInput("")
	in.HostID = &bound
	tok := h.enrollToken(testOrg, in)
	_, csr := newKeyAndCSR(t)
	if _, err := h.enroll(tok.raw, csr, strings.Repeat("b", 65)); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("overlong presented name: %v, want InvalidArgument", err)
	}
	if uses, used := h.store.useOf(tok.parsed.ID); uses != 0 || used {
		t.Fatalf("the refusal burned a use: uses=%d used=%v", uses, used)
	}
	if h.prov.monitoredOf(bound) {
		t.Fatal("the refusal flipped the bound host's monitored flag")
	}
}

// TestEnrollBindFlowWithoutName: a bound token, no hint, no presented
// name — the certificate cannot be named, the signing step refuses with
// InvalidArgument (the claim has committed at that point: this is the
// documented residual of the bind flow's empty-name case).
func TestEnrollBindFlowWithoutName(t *testing.T) {
	h := newHarness(t)
	bound := h.prov.seedHost(testOrg, "web-1")
	in := tokenInput("")
	in.HostID = &bound
	tok := h.enrollToken(testOrg, in)
	_, csr := newKeyAndCSR(t)
	if _, err := h.enroll(tok.raw, csr, ""); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bind flow without a name: %v, want InvalidArgument", err)
	}
}

// TestReceiveMessagesDropsAfterTheCut: messages read after the stream was
// cut are dropped without acknowledgement, and the loop ends when the
// ended RPC's Recv fails.
func TestReceiveMessagesDropsAfterTheCut(t *testing.T) {
	h := newHarness(t)
	tracked := &trackedStream{revoked: make(chan struct{})}
	tracked.shut()
	stream := &scriptedAgentStream{ctx: t.Context(), recv: []func() (*agentpb.AgentMessage, error){
		func() (*agentpb.AgentMessage, error) {
			return &agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{Heartbeat: &agentpb.Heartbeat{Version: "v"}}}, nil
		},
	}}
	if err := h.svc.receiveMessages(t.Context(), testIdentity(), stream, tracked); err != nil {
		t.Fatalf("receive after the cut = %v, want a clean end", err)
	}
	if got := len(h.store.touches()); got != 0 {
		t.Fatalf("%d touches recorded after the cut, want none", got)
	}
}
