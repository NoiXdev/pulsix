// Renew and AgentStream: the certificate swap, the heartbeat-driven
// last_seen, the facts flow to the sink, and every way a revocation ends
// an agent's access — including an open stream.
package agentapi

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// enrollAnAgent runs the happy enroll and returns the response with the
// agent's key, ready to open authenticated connections.
func enrollAnAgent(t *testing.T, h *harness, name string) (*agentpb.EnrollResponse, *agentpb.AgentServiceClient) {
	t.Helper()
	tok := h.enrollToken(testOrg, tokenInput(name))
	key, csr := newKeyAndCSR(t)
	resp, err := h.enroll(tok.raw, csr, name)
	if err != nil {
		t.Fatal(err)
	}
	client := h.client([]byte(resp.GetCertPem()), keyPEMOf(t, key))
	return resp, &client
}

// TestRenewRoundTrip: the fresh certificate authenticates, the registry
// serial swapped (the old one revoked), the host id unchanged, one
// agent.renew audit event.
func TestRenewRoundTrip(t *testing.T) {
	h := newHarness(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	oldSerial := mustCert(t, resp.GetCertPem()).SerialNumber.String()

	key, csr := newKeyAndCSR(t)
	renewed, err := (*client).Renew(t.Context(), &agentpb.RenewRequest{CsrPem: csr})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.GetHostId() != resp.GetHostId() {
		t.Fatalf("renewal moved hosts: %s → %s", resp.GetHostId(), renewed.GetHostId())
	}
	newCrt := mustCert(t, renewed.GetCertPem())
	if newCrt.SerialNumber.String() == oldSerial {
		t.Fatal("the renewed certificate must carry a fresh serial")
	}
	renewedPub, ok := newCrt.PublicKey.(*ecdsa.PublicKey)
	if !ok || !renewedPub.Equal(&key.PublicKey) {
		t.Fatal("the renewed certificate must carry the renewed key")
	}
	if row, err := h.agentSvc.GetBySerial(t.Context(), newCrt.SerialNumber.String()); err != nil || row.Revoked {
		t.Fatalf("new serial live: %+v, %v", row, err)
	}
	if got := h.store.registeredKey(newCrt.SerialNumber.String()); got != agents.KeyFingerprint(newCrt) {
		t.Fatalf("registered key = %q, want the renewed leaf's %q", got, agents.KeyFingerprint(newCrt))
	}
	if row, err := h.agentSvc.GetBySerial(t.Context(), oldSerial); err != nil || !row.Revoked {
		t.Fatalf("old serial must be revoked after the swap: %+v, %v", row, err)
	}
	events := h.audit.recorded()
	if len(events) != 2 || events[1].Operation != "agent.renew" {
		t.Fatalf("audit = %+v, want enroll + renew", events)
	}
}

// TestRenewRacingARevocationIsRefused is backlog 7.18: the interceptor
// accepts the live serial, then the agent is revoked before the renewal's
// swap. The renewal is refused and the certificate it signed is never
// recorded, so it does not authenticate.
func TestRenewRacingARevocationIsRefused(t *testing.T) {
	h := newHarness(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	serial := mustCert(t, resp.GetCertPem()).SerialNumber.String()
	h.store.armRevokeAfterLookup(serial)

	_, csr := newKeyAndCSR(t)
	if _, err := (*client).Renew(t.Context(), &agentpb.RenewRequest{CsrPem: csr}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Renew after a revocation = %v, want PermissionDenied", err)
	}
	for _, ev := range h.audit.recorded() {
		if ev.Operation == "agent.renew" {
			t.Fatalf("a refused renewal was audited as done: %+v", ev)
		}
	}
}

// TestRenewRefusedWithoutCertificate: Renew is certificate-authenticated.
func TestRenewRefusedWithoutCertificate(t *testing.T) {
	h := newHarness(t)
	_, csr := newKeyAndCSR(t)
	if _, err := h.client(nil, nil).Renew(t.Context(), &agentpb.RenewRequest{CsrPem: csr}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous renew: %v, want Unauthenticated", err)
	}
}

// TestRenewBadCSR: a rejected request is the caller's fault.
func TestRenewBadCSR(t *testing.T) {
	h := newHarness(t)
	_, client := enrollAnAgent(t, h, "web-1")
	if _, err := (*client).Renew(t.Context(), &agentpb.RenewRequest{CsrPem: "garbage"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad csr: %v, want InvalidArgument", err)
	}
}

// TestHeartbeatUpdatesLastSeen: the open-touch plus every heartbeat move
// last_seen and the self-reported version; each message is acknowledged.
func TestHeartbeatUpdatesLastSeen(t *testing.T) {
	h := newHarness(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	serial := mustCert(t, resp.GetCertPem()).SerialNumber.String()

	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"v0.3a-1", "v0.3a-2"} {
		if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{Heartbeat: &agentpb.Heartbeat{Version: v}}}); err != nil {
			t.Fatal(err)
		}
		ack, err := recvWithin(t, stream, recvTimeout)
		if err != nil || !ack.GetAck() {
			t.Fatalf("heartbeat %q ack = %+v, %v", v, ack, err)
		}
	}
	touches := h.store.touches()
	if len(touches) != 3 { // open-touch (row's version) + two heartbeats
		t.Fatalf("touches = %v, want the open-touch plus two heartbeats", touches)
	}
	if touches[0] != serial+":v0.3a" || touches[1] != serial+":v0.3a-1" || touches[2] != serial+":v0.3a-2" {
		t.Fatalf("touches = %v", touches)
	}
	agents1, err := h.agentSvc.List(context.Background(), testOrg)
	if err != nil || len(agents1) != 1 || agents1[0].Version != "v0.3a-2" || agents1[0].LastSeen == nil {
		t.Fatalf("list after heartbeats = %+v, %v", agents1, err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
}

// TestFactsFlowToSink: facts land on the sink with the authenticated
// agent's org and host, and the message is acknowledged.
func TestFactsFlowToSink(t *testing.T) {
	h := newHarness(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Facts{Facts: &agentpb.Facts{
		Values: map[string]string{"os": "linux", "arch": "arm64"},
	}}}); err != nil {
		t.Fatal(err)
	}
	if ack, err := recvWithin(t, stream, recvTimeout); err != nil || !ack.GetAck() {
		t.Fatalf("facts ack = %+v, %v", ack, err)
	}
	calls := h.sink.recorded()
	if len(calls) != 1 || calls[0].OrgID != testOrg || calls[0].HostID.String() != resp.GetHostId() ||
		calls[0].Facts["os"] != "linux" || calls[0].Facts["arch"] != "arm64" {
		t.Fatalf("sink = %+v", calls)
	}
}

// TestFactsDroppedWithoutSink: with no sink wired (before Task 8), facts
// are dropped with a server log and still acknowledged.
func TestFactsDroppedWithoutSink(t *testing.T) {
	h := newHarnessWithoutFacts(t)
	_, client := enrollAnAgent(t, h, "web-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Facts{Facts: &agentpb.Facts{
		Values: map[string]string{"os": "linux"},
	}}}); err != nil {
		t.Fatal(err)
	}
	if ack, err := recvWithin(t, stream, recvTimeout); err != nil || !ack.GetAck() {
		t.Fatalf("dropped facts must still be acknowledged: %+v, %v", ack, err)
	}
	if calls := h.sink.recorded(); len(calls) != 0 {
		t.Fatalf("facts recorded without a sink: %+v", calls)
	}
}

// TestFactsSinkFaultEndsStream: a sink fault is a server error — the
// stream ends with Internal and the agent reconnects (facts re-push at
// their next interval).
func TestFactsSinkFaultEndsStream(t *testing.T) {
	h := newHarness(t)
	h.sink.mu.Lock()
	h.sink.err = errors.New("db down")
	h.sink.mu.Unlock()
	_, client := enrollAnAgent(t, h, "web-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Facts{Facts: &agentpb.Facts{
		Values: map[string]string{"os": "linux"},
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := recvWithin(t, stream, recvTimeout); status.Code(err) != codes.Internal {
		t.Fatalf("sink fault: %v, want Internal", err)
	}
}

// TestUnknownBodyAcked: the message union is open — an unrecognized body
// is a no-op that is still acknowledged.
func TestUnknownBodyAcked(t *testing.T) {
	h := newHarness(t)
	_, client := enrollAnAgent(t, h, "web-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{}); err != nil {
		t.Fatal(err)
	}
	if ack, err := recvWithin(t, stream, recvTimeout); err != nil || !ack.GetAck() {
		t.Fatalf("empty-body ack = %+v, %v", ack, err)
	}
}

// TestStreamRequiresCertificate: AgentStream is not in the anonymous set.
func TestStreamRequiresCertificate(t *testing.T) {
	h := newHarness(t)
	stream, err := h.client(nil, nil).AgentStream(t.Context())
	if err == nil {
		_, err = recvWithin(t, stream, recvTimeout)
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous stream: %v, want Unauthenticated", err)
	}
}

// TestRevokedSerialRejected: after agents.Revoke the certificate's serial
// is refused on every authenticated method, on the same connection.
func TestRevokedSerialRejected(t *testing.T) {
	h := newHarness(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	if err := h.agentSvc.Revoke(context.Background(), testOrg, hostID); err != nil {
		t.Fatal(err)
	}
	_, csr := newKeyAndCSR(t)
	if _, err := (*client).Renew(t.Context(), &agentpb.RenewRequest{CsrPem: csr}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("renew after revoke: %v, want Unauthenticated", err)
	}
}

// TestReenrollCutsTheReplacedStream: re-enrolling a host replaces its
// active agent — the old serial is revoked by the swap, so the old
// stream is cut while the fresh serial's stream works.
func TestReenrollCutsTheReplacedStream(t *testing.T) {
	h := newHarness(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	oldStream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for trackedStreams(h.svc.Streams(), hostID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the old stream never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Re-enroll the same host under a fresh token and key.
	key2, csr2 := newKeyAndCSR(t)
	in := tokenInput("")
	in.HostID = &hostID
	tok := h.enrollToken(testOrg, in)
	resp2, err := h.enroll(tok.raw, csr2, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if resp2.GetHostId() != hostID.String() {
		t.Fatalf("re-enroll moved hosts: %s", resp2.GetHostId())
	}

	// The replaced serial's stream is cut.
	if _, err := recvWithin(t, oldStream, recvTimeout); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("old stream after re-enroll: %v, want Unauthenticated", err)
	}
	// The fresh serial's stream works.
	newClient := h.client([]byte(resp2.GetCertPem()), keyPEMOf(t, key2))
	stream2, err := newClient.AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream2.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{Heartbeat: &agentpb.Heartbeat{Version: "v2"}}}); err != nil {
		t.Fatal(err)
	}
	if ack, err := recvWithin(t, stream2, recvTimeout); err != nil || !ack.GetAck() {
		t.Fatalf("new stream heartbeat ack = %+v, %v", ack, err)
	}
}

// TestRenewalKeepsTheStream: renewal swaps the serial but pins the stream —
// the open connection keeps processing after the swap.
func TestRenewalKeepsTheStream(t *testing.T) {
	h := newHarness(t)
	_, client := enrollAnAgent(t, h, "web-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{Heartbeat: &agentpb.Heartbeat{Version: "v1"}}}); err != nil {
		t.Fatal(err)
	}
	if ack, err := recvWithin(t, stream, recvTimeout); err != nil || !ack.GetAck() {
		t.Fatalf("pre-renewal heartbeat = %+v, %v", ack, err)
	}
	_, csr := newKeyAndCSR(t)
	if _, err := (*client).Renew(t.Context(), &agentpb.RenewRequest{CsrPem: csr}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{Heartbeat: &agentpb.Heartbeat{Version: "v2"}}}); err != nil {
		t.Fatal(err)
	}
	if ack, err := recvWithin(t, stream, recvTimeout); err != nil || !ack.GetAck() {
		t.Fatalf("post-renewal heartbeat = %+v, %v — renewal must not cut the stream", ack, err)
	}
}

// TestRevocationEndsStreams: an open stream is cut when the agent is
// revoked — the handler ends it with Unauthenticated and the registry
// entry is gone.
func TestRevocationEndsStreams(t *testing.T) {
	h := newHarness(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	hostID := uuid.MustParse(resp.GetHostId())
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The stream is open: wait until the registry tracks it.
	deadline := time.Now().Add(5 * time.Second)
	for trackedStreams(h.svc.Streams(), hostID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stream never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := h.agentSvc.Revoke(context.Background(), testOrg, hostID); err != nil {
		t.Fatal(err)
	}
	if _, err := recvWithin(t, stream, recvTimeout); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("stream after revoke: %v, want Unauthenticated", err)
	}
	if trackedStreams(h.svc.Streams(), hostID) != 0 {
		t.Fatal("the cut stream was never released")
	}
}

// TestRevocationBetweenAuthAndTrackEndsTheStream: a revocation that
// commits after the interceptor resolved the serial but before the handler
// registered the stream is missed by CloseHost; the handler re-checks the
// serial once the stream is tracked and ends it with Unauthenticated.
func TestRevocationBetweenAuthAndTrackEndsTheStream(t *testing.T) {
	h := newHarness(t)
	resp, client := enrollAnAgent(t, h, "web-1")
	serial := mustCert(t, resp.GetCertPem()).SerialNumber.String()
	h.store.armRevokeAfterLookup(serial)
	stream, err := (*client).AgentStream(t.Context())
	if err == nil {
		_, err = recvWithin(t, stream, recvTimeout)
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("stream after the between-auth-and-track revoke: %v, want Unauthenticated", err)
	}
}

// TestStreamOpenTouchFaultIsLogged: a fault recording the open-touch is
// logged, not fatal — the stream opens and the heartbeats that follow
// carry the retry (here: failing while the fault is armed).
func TestStreamOpenTouchFaultIsLogged(t *testing.T) {
	h := newHarness(t)
	h.store.mu.Lock()
	h.store.touchErr = errors.New("db down")
	h.store.mu.Unlock()
	_, client := enrollAnAgent(t, h, "web-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Heartbeat{Heartbeat: &agentpb.Heartbeat{Version: "v"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := recvWithin(t, stream, recvTimeout); status.Code(err) != codes.Internal {
		t.Fatalf("heartbeat with the touch fault armed: %v, want Internal", err)
	}
}

// TestStreamRecheckFaultIsInternal: a registry fault in the post-track
// re-check is a server fault, not a revocation. The fault is armed to land
// between the interceptor's lookup (succeeds) and the handler's re-check.
func TestStreamRecheckFaultIsInternal(t *testing.T) {
	h := newHarness(t)
	_, client := enrollAnAgent(t, h, "web-1")
	h.store.armLookupFaultAfterNext()
	stream, err := (*client).AgentStream(t.Context())
	if err == nil {
		_, err = recvWithin(t, stream, recvTimeout)
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("re-check fault: %v, want Internal", err)
	}
}

// trackedStreams is the test's window into the registry.
func trackedStreams(r *StreamRegistry, hostID uuid.UUID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byHost[hostID])
}

// TestStreamFactsBoundsRejected: a facts push over the facts contract's
// bounds is the CALLER's fault — the sink's ErrInvalidInput surfaces as
// InvalidArgument (not Internal), so the agent's log says what it did
// wrong, not that the server broke.
func TestStreamFactsBoundsRejected(t *testing.T) {
	h := newHarness(t)
	h.sink.mu.Lock()
	h.sink.err = agents.ErrInvalidInput
	h.sink.mu.Unlock()
	_, client := enrollAnAgent(t, h, "web-1")
	stream, err := (*client).AgentStream(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpb.AgentMessage{Body: &agentpb.AgentMessage_Facts{Facts: &agentpb.Facts{
		Values: map[string]string{"os": "linux"},
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := recvWithin(t, stream, recvTimeout); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("over-bounds facts: %v, want InvalidArgument", err)
	}
}
