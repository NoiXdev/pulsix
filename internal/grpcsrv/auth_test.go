// Auth: what the mTLS interceptor does to connections with no certificate,
// a foreign certificate, a certificate whose serial the registry does not
// know or has revoked, and a certificate whose zone differs from its row.
package grpcsrv

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

// orgID/zoneID are fixed so the harness's issued certificates are stable.
var (
	orgID  = uuid.MustParse("11111111-1111-7111-8111-111111111111")
	zoneID = uuid.MustParse("22222222-2222-7222-8222-222222222222")
)

// TestAuthUnauthenticated rejects a call that presents no client
// certificate. With tls.VerifyClientCertIfGiven the handshake succeeds —
// there is nothing to verify — so the refusal comes from the interceptor,
// for every method but Enroll. This answer is not a toss-up between the
// handshake and the interceptor: nothing in the TLS exchange can fail
// here, so the code and the message are both pinned.
func TestAuthUnauthenticated(t *testing.T) {
	h := newHarness(t)
	client := h.client(nil, nil)
	_, err := client.ClaimJob(t.Context(), claimRequest(zoneID))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %s (%v), want Unauthenticated from the interceptor", status.Code(err), err)
	}
	if msg := status.Convert(err).Message(); !strings.Contains(msg, "no client certificate presented") {
		t.Fatalf("message = %q, want the missing certificate named", msg)
	}
}

// TestAuthForeignCertificate: a client certificate chaining to some other
// root buys no service — and the two refusals it can run into are
// different mechanisms, so the test names both instead of accepting
// either.
//
// An honest client never gets the certificate onto the wire: crypto/tls
// matches tls.Config.Certificates against the authorities the server
// names in its certificate request, finds none that fit, and sends an
// empty certificate — so the call reads exactly like presenting nothing.
// That filter is the client's own courtesy, not a defence, which is why
// the second half forces the certificate out regardless and lands on the
// SERVER's verification. Only the code is pinned there: under TLS 1.3 the
// server's alert races the client's first write, so the message is
// sometimes the refused authority and sometimes the closed pipe.
func TestAuthForeignCertificate(t *testing.T) {
	h := newHarness(t)

	// A second CA and a worker certificate it signed.
	other, err := ca.New(&fakeCAStore{}, crypto.NewBox([32]byte{}), ca.Config{CommonName: "other-ca"})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	foreignCert, foreignKey, _, err := other.Issue(context.Background(), &orgID, zoneID, "impostor")
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.client(foreignCert, foreignKey).ClaimJob(t.Context(), claimRequest(zoneID))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("honest client: code = %s (%v), want Unauthenticated", status.Code(err), err)
	}
	if msg := status.Convert(err).Message(); !strings.Contains(msg, "no client certificate presented") {
		t.Fatalf("honest client: message = %q — the certificate must never reach the server", msg)
	}

	forced := workerpb.NewWorkerServiceClient(h.dial(h.forcedClientTLS(foreignCert, foreignKey)))
	if _, err := forced.ClaimJob(t.Context(), claimRequest(zoneID)); status.Code(err) != codes.Unavailable {
		t.Fatalf("forced onto the wire: code = %s (%v), want a refused handshake", status.Code(err), err)
	}
}

// TestAuthUnknownOrRevokedCertificate: the certificate chains to the
// instance CA, but its serial is unknown, or revoked — both Unauthenticated
// (spec 2026-09-18 §4), the same answer so neither can be told apart.
func TestAuthUnknownOrRevokedCertificate(t *testing.T) {
	h := newHarness(t)
	unknownCert, unknownKey, _ := h.issue("unregistered", orgID, zoneID, false) // never registered
	revokedCert, revokedKey, revokedSerial := h.issue("revoked", orgID, zoneID, true)
	h.store.mu.Lock()
	h.store.revoked[revokedSerial.String()] = true
	h.store.mu.Unlock()
	for name, pair := range map[string][2][]byte{"unknown": {unknownCert, unknownKey}, "revoked": {revokedCert, revokedKey}} {
		_, err := h.client(pair[0], pair[1]).ClaimJob(t.Context(), claimRequest(zoneID))
		if status.Code(err) != codes.Unauthenticated || !strings.Contains(status.Convert(err).Message(), "unknown or revoked certificate") {
			t.Fatalf("%s: %v, want Unauthenticated", name, err)
		}
	}
}

// TestAuthZoneMismatch: a certificate whose zone SAN differs from the
// registry row it resolves to is refused.
func TestAuthZoneMismatch(t *testing.T) {
	h := newHarness(t)
	otherZone := uuid.Must(uuid.NewV7())
	certPEM, keyPEM, serial := h.issue("drifted", orgID, otherZone, false)
	if _, err := workers.NewService(h.store).Register(t.Context(), orgID, workers.RegisterInput{ZoneID: zoneID, Name: "drifted", CertSerial: serial}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client(certPEM, keyPEM).ClaimJob(t.Context(), claimRequest(zoneID)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("zone mismatch: %v", err)
	}
}

// claimRequest builds a ClaimJobRequest for one zone.
func claimRequest(zone uuid.UUID) *workerpb.ClaimJobRequest {
	return &workerpb.ClaimJobRequest{ZoneIds: []string{zone.String()}}
}

// TestEveryMethodButEnrollNeedsACertificate: without a client certificate
// the handshake succeeds (VerifyClientCertIfGiven), and every method of
// both services except their Enroll answers Unauthenticated. The methods
// come from the service descriptors, so a future RPC is covered without
// editing this test and is callable anonymously only once it is
// allowlisted explicitly.
func TestEveryMethodButEnrollNeedsACertificate(t *testing.T) {
	h := newHarness(t)
	conn := h.conn(nil, nil)
	// Over every method and stream of both services, not over the
	// allowlist's own length: the list is a switch since backlog 2.68,
	// and enumerating the real surface is the stronger check anyway —
	// a new RPC that forgets its certificate is caught here whether or
	// not anyone remembered to count.
	for _, desc := range []grpc.ServiceDesc{workerpb.WorkerService_ServiceDesc, agentpb.AgentService_ServiceDesc} {
		for _, m := range desc.Methods {
			method := "/" + desc.ServiceName + "/" + m.MethodName
			wantOpen := method == workerpb.WorkerService_Enroll_FullMethodName || method == agentpb.AgentService_Enroll_FullMethodName
			if unauthenticatedMethod(method) != wantOpen {
				t.Fatalf("%s callable without a certificate = %v, want %v", method, !wantOpen, wantOpen)
			}
		}
		for _, sd := range desc.Streams {
			method := "/" + desc.ServiceName + "/" + sd.StreamName
			if unauthenticatedMethod(method) {
				t.Fatalf("%s is a stream and must never be callable without a certificate", method)
			}
		}
	}
	for _, desc := range []grpc.ServiceDesc{workerpb.WorkerService_ServiceDesc, agentpb.AgentService_ServiceDesc} {
		for _, m := range desc.Methods {
			method := "/" + desc.ServiceName + "/" + m.MethodName
			if method == workerpb.WorkerService_Enroll_FullMethodName || method == agentpb.AgentService_Enroll_FullMethodName {
				continue
			}
			if err := conn.Invoke(t.Context(), method, &emptypb.Empty{}, &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
				t.Errorf("%s without a certificate: %v, want Unauthenticated", method, err)
			}
		}
		for _, sd := range desc.Streams {
			method := "/" + desc.ServiceName + "/" + sd.StreamName
			stream, err := conn.NewStream(t.Context(), &grpc.StreamDesc{StreamName: sd.StreamName, ServerStreams: sd.ServerStreams, ClientStreams: sd.ClientStreams}, method)
			if err == nil {
				err = stream.RecvMsg(&emptypb.Empty{})
			}
			if status.Code(err) != codes.Unauthenticated {
				t.Errorf("%s without a certificate: %v, want Unauthenticated", method, err)
			}
		}
	}
}

// TestMethodKindServicePrefixes pins the hardcoded service prefixes
// checkMethodKind matches against the generated full-method constants: a
// proto package change must fail here rather than silently disable the
// class segregation.
func TestMethodKindServicePrefixes(t *testing.T) {
	if !strings.HasPrefix(workerpb.WorkerService_Enroll_FullMethodName, workerServicePrefix) {
		t.Fatalf("worker prefix %q does not match %q", workerServicePrefix, workerpb.WorkerService_Enroll_FullMethodName)
	}
	if !strings.HasPrefix(agentpb.AgentService_Enroll_FullMethodName, agentServicePrefix) {
		t.Fatalf("agent prefix %q does not match %q", agentServicePrefix, agentpb.AgentService_Enroll_FullMethodName)
	}
}

// TestMethodKindEnforcement: the two certificate classes are segregated by
// method — an agent certificate is refused on the WorkerService and a
// worker certificate on the AgentService (PermissionDenied, before any
// handler runs), while each class reaches its own service (the agent
// service's stub answers Unimplemented, proving the interceptor let the
// call through).
func TestMethodKindEnforcement(t *testing.T) {
	h := newHarness(t)
	agentCert, agentKey, _ := h.issueAgent("agt-1", orgID, true)
	workerCertPEM, workerKeyPEM := h.worker()
	agentConn := h.conn(agentCert, agentKey)
	workerConn := h.conn(workerCertPEM, workerKeyPEM)

	// An agent certificate is refused on the WorkerService...
	if _, err := workerpb.NewWorkerServiceClient(agentConn).ClaimJob(t.Context(), claimRequest(zoneID)); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("agent cert on ClaimJob: %v, want PermissionDenied", err)
	}
	// ...and a worker certificate on the AgentService.
	if _, err := agentpb.NewAgentServiceClient(workerConn).Renew(t.Context(), &agentpb.RenewRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("worker cert on Renew: %v, want PermissionDenied", err)
	}
	// The right class reaches its own service: the agent service's stub
	// answers Unimplemented, proving the interceptor let the call through.
	if _, err := agentpb.NewAgentServiceClient(agentConn).Renew(t.Context(), &agentpb.RenewRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("agent cert on Renew: %v, want the interceptor to let it through (Unimplemented stub)", err)
	}
}

// TestAuthAgentUnknownOrRevokedCertificate: an agent certificate whose
// serial the agent registry does not know or has revoked is refused with
// the same indistinguishable Unauthenticated the worker path answers.
func TestAuthAgentUnknownOrRevokedCertificate(t *testing.T) {
	h := newHarness(t)
	unknownCert, unknownKey, _ := h.issueAgent("unregistered-agent", orgID, false)
	revokedCert, revokedKey, revokedSerial := h.issueAgent("revoked-agent", orgID, true)
	h.agents.mu.Lock()
	h.agents.revoked[revokedSerial.String()] = true
	h.agents.mu.Unlock()
	for name, pair := range map[string][2][]byte{"unknown": {unknownCert, unknownKey}, "revoked": {revokedCert, revokedKey}} {
		conn := h.conn(pair[0], pair[1])
		stream, err := agentpb.NewAgentServiceClient(conn).AgentStream(t.Context())
		if err == nil {
			_, err = stream.Recv()
		}
		if status.Code(err) != codes.Unauthenticated || !strings.Contains(status.Convert(err).Message(), "unknown or revoked certificate") {
			t.Fatalf("%s agent certificate: %v, want Unauthenticated", name, err)
		}
	}
}

// TestAuthAgentOrgMismatch: an agent certificate whose org SAN differs from
// the registry row it resolves to is refused, like the worker org check.
func TestAuthAgentOrgMismatch(t *testing.T) {
	h := newHarness(t)
	otherOrg := uuid.Must(uuid.NewV7())
	certPEM, keyPEM, _ := h.issueAgent("foreign-agent", otherOrg, false)
	// Register it under orgID's registry — the certificate's org SAN does
	// not match the row it resolves to.
	serial := serialOf(t, h, certPEM)
	h.agents.mu.Lock()
	h.agents.bySerial[serial.String()] = agents.AgentIdentity{OrgID: orgID, HostID: uuid.Must(uuid.NewV7()), Name: "foreign-agent"}
	h.agents.mu.Unlock()
	stream, err := agentpb.NewAgentServiceClient(h.conn(certPEM, keyPEM)).AgentStream(t.Context())
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.Unauthenticated || !strings.Contains(status.Convert(err).Message(), "does not match") {
		t.Fatalf("agent org mismatch: %v, want Unauthenticated", err)
	}
}

// serialOf parses the leaf certificate's serial out of certPEM (test
// helper for the org-mismatch case where the cert was issued but never
// registered through the service).
func serialOf(t *testing.T, h *harness, certPEM []byte) *big.Int {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block")
	}
	crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return crt.SerialNumber
}

// TestRevocationRefusesTheNextCall: the interceptor resolves the serial on
// every call, so a worker that was just served is refused on its very next
// call, on the same connection, once its certificate is revoked.
func TestRevocationRefusesTheNextCall(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, serial := h.issue("fra-1-a", orgID, zoneID, true)
	client := h.client(certPEM, keyPEM)
	if _, err := client.ClaimJob(t.Context(), claimRequest(zoneID)); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	h.store.revoked[serial.String()] = true
	h.store.mu.Unlock()
	if _, err := client.ClaimJob(t.Context(), claimRequest(zoneID)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("call after revocation: %v, want Unauthenticated", err)
	}
}

// TestExpiredCertificateRefused: a registered certificate is usable only
// until it expires (a renewal keeps the old one valid until then, no
// longer); the TLS layer refuses it afterwards.
func TestExpiredCertificateRefused(t *testing.T) {
	h := newHarness(t)
	past, err := ca.New(h.caStore, h.caBox, ca.Config{CommonName: "pulsix-manager", ListenHost: "manager"},
		ca.WithClock(func() time.Time { return time.Now().Add(-ca.LeafValidity - time.Hour) }))
	if err != nil {
		t.Fatal(err)
	}
	if err := past.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, serial, err := past.Issue(t.Context(), &orgID, zoneID, "expired")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workers.NewService(h.store).Register(t.Context(), orgID, workers.RegisterInput{ZoneID: zoneID, Name: "expired", CertSerial: serial}); err != nil {
		t.Fatal(err)
	}
	// An expired certificate still chains to the instance CA, so the
	// client does offer it and the server's own verification is what
	// refuses it — at the handshake, before any interceptor runs. Only
	// the code is pinned: like every TLS 1.3 refusal here the alert races
	// the client's first write, so the message is sometimes the expiry
	// and sometimes the closed pipe.
	_, err = h.client(certPEM, keyPEM).ClaimJob(t.Context(), claimRequest(zoneID))
	if code := status.Code(err); code != codes.Unavailable {
		t.Fatalf("expired certificate: %s (%v), want a refused handshake", code, err)
	}
}

// TestAuthOrgMismatch: a certificate whose org SAN differs from the
// registry row it resolves to is refused, like a zone mismatch.
func TestAuthOrgMismatch(t *testing.T) {
	h := newHarness(t)
	otherOrg := uuid.Must(uuid.NewV7())
	certPEM, keyPEM, serial := h.issue("foreign-org", otherOrg, zoneID, false)
	if _, err := workers.NewService(h.store).Register(t.Context(), orgID, workers.RegisterInput{ZoneID: zoneID, Name: "foreign-org", CertSerial: serial}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client(certPEM, keyPEM).ClaimJob(t.Context(), claimRequest(zoneID)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("org mismatch: %v", err)
	}
}

// TestAuthLookupFailureIsGeneric: a registry fault behind the interceptor
// answers Internal without echoing the store's error text.
func TestAuthLookupFailureIsGeneric(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM := h.worker()
	h.store.mu.Lock()
	h.store.lookupErr = errors.New(`relation "worker_certificates" does not exist`)
	h.store.mu.Unlock()
	_, err := h.client(certPEM, keyPEM).ClaimJob(t.Context(), claimRequest(zoneID))
	if status.Code(err) != codes.Internal || strings.Contains(status.Convert(err).Message(), "worker_certificates") {
		t.Fatalf("lookup fault: %v, want a generic Internal", err)
	}
}

// adoptionEvents returns the worker.certificate_adopted audit events.
func (h *harness) adoptionEvents() []SystemEvent {
	var out []SystemEvent
	for _, ev := range h.audit.Events() {
		if ev.Operation == "worker.certificate_adopted" {
			out = append(out, ev)
		}
	}
	return out
}

// restoredAround sets the restore marker the fake registry's adoption
// reads: a backup an hour old, restored an hour from now, so a
// certificate issued now lies inside the window.
func (h *harness) restoredAround(now time.Time) {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	h.store.restoreWindow = &workers.RestoreWindow{BackupCreatedAt: now.Add(-time.Hour), RestoredAt: now.Add(time.Hour)}
}

// TestAuthAdoptsAWorkerCertificateAfterARestore (backlog 7.47): a
// certificate the restored registry does not know is adopted when the
// registry's rules allow it, the call goes through, and the adoption is
// audited and logged once; the next call finds the serial registered.
func TestAuthAdoptsAWorkerCertificateAfterARestore(t *testing.T) {
	h := newHarness(t)
	_, _, firstSerial := h.issue("fra-1", orgID, zoneID, true)
	worker, err := h.store.GetBySerial(t.Context(), firstSerial)
	if err != nil {
		t.Fatal(err)
	}
	h.restoredAround(time.Now())
	certPEM, keyPEM, serial := h.issue("fra-1", orgID, zoneID, false)
	client := h.client(certPEM, keyPEM)

	if _, err := client.ClaimJob(t.Context(), claimRequest(zoneID)); err != nil {
		t.Fatalf("ClaimJob with a certificate issued after the backup: %v", err)
	}
	if got, err := h.store.GetBySerial(t.Context(), serial); err != nil || got.ID != worker.ID {
		t.Fatalf("adopted serial resolves to %+v, %v; want worker %s", got, err, worker.ID)
	}
	events := h.adoptionEvents()
	if len(events) != 1 {
		t.Fatalf("adoption events = %+v, want one", events)
	}
	ev := events[0]
	if ev.ObjectType != "worker" || ev.ObjectID != worker.ID.String() || ev.OrgID != orgID.String() ||
		!strings.Contains(ev.Summary, serial.String()) || !strings.Contains(ev.Summary, "fra-1") {
		t.Fatalf("adoption event = %+v, want worker %s and serial %s named", ev, worker.ID, serial)
	}
	logs := h.logs.String()
	adopted := ""
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "adopted") {
			if adopted != "" {
				t.Fatalf("logs = %q, want one adoption line", logs)
			}
			adopted = line
		}
	}
	if !strings.Contains(adopted, "level=INFO") || !strings.Contains(adopted, "serial="+serial.String()) {
		t.Fatalf("logs = %q, want one info line naming serial %s", logs, serial)
	}

	if _, err := client.ClaimJob(t.Context(), claimRequest(zoneID)); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if n := len(h.adoptionEvents()); n != 1 {
		t.Fatalf("adoption events after the second call = %d, want still one", n)
	}
}

// TestAuthRefusesACertificateItCannotAdopt: without a restore marker the
// unknown serial is refused exactly as before; a revoked serial is never
// offered for adoption, restore or not; and a registry fault during the
// adoption is a generic Internal.
func TestAuthRefusesACertificateItCannotAdopt(t *testing.T) {
	refused := func(t *testing.T, h *harness, certPEM, keyPEM []byte) {
		t.Helper()
		_, err := h.client(certPEM, keyPEM).ClaimJob(t.Context(), claimRequest(zoneID))
		if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != "unknown or revoked certificate" {
			t.Fatalf("%v, want Unauthenticated: unknown or revoked certificate", err)
		}
		if events := h.adoptionEvents(); len(events) != 0 {
			t.Fatalf("adoption events = %+v, want none", events)
		}
	}
	t.Run("no restore", func(t *testing.T) {
		h := newHarness(t)
		h.issue("fra-1", orgID, zoneID, true)
		certPEM, keyPEM, _ := h.issue("fra-1", orgID, zoneID, false)
		refused(t, h, certPEM, keyPEM)
	})
	t.Run("revoked serial", func(t *testing.T) {
		h := newHarness(t)
		certPEM, keyPEM, serial := h.issue("fra-1", orgID, zoneID, true)
		h.store.mu.Lock()
		h.store.revoked[serial.String()] = true
		h.store.mu.Unlock()
		h.restoredAround(time.Now())
		refused(t, h, certPEM, keyPEM)
	})
	t.Run("registry fault", func(t *testing.T) {
		h := newHarness(t)
		h.issue("fra-1", orgID, zoneID, true)
		h.restoredAround(time.Now())
		h.store.mu.Lock()
		h.store.adoptErr = errors.New(`relation "instance_settings" does not exist`)
		h.store.mu.Unlock()
		certPEM, keyPEM, _ := h.issue("fra-1", orgID, zoneID, false)
		_, err := h.client(certPEM, keyPEM).ClaimJob(t.Context(), claimRequest(zoneID))
		if status.Code(err) != codes.Internal || strings.Contains(status.Convert(err).Message(), "instance_settings") {
			t.Fatalf("adoption fault: %v, want a generic Internal", err)
		}
	})
}
