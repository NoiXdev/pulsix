// Auth: what the mTLS interceptor does to connections with no certificate,
// a foreign certificate, a certificate whose serial the registry does not
// know or has revoked, and a certificate whose zone differs from its row.
package grpcsrv

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

// orgID/zoneID are fixed so the harness's issued certificates are stable.
var (
	orgID  = uuid.MustParse("11111111-1111-7111-8111-111111111111")
	zoneID = uuid.MustParse("22222222-2222-7222-8222-222222222222")
)

// TestAuthUnauthenticated rejects a call that presents no client
// certificate. With tls.VerifyClientCertIfGiven the handshake succeeds, and
// the interceptor answers codes.Unauthenticated for every method but
// Enroll; the assert also accepts a handshake-failure codes.Unavailable,
// and the documentation point is: no certificate, no service.
func TestAuthUnauthenticated(t *testing.T) {
	h := newHarness(t)
	client := h.client(nil, nil)
	_, err := client.ClaimJob(t.Context(), claimRequest(zoneID))
	if err == nil {
		t.Fatal("claim without a client certificate must fail")
	}
	code := status.Code(err)
	if code != codes.Unavailable && code != codes.Unauthenticated {
		t.Fatalf("code = %s (%v), want Unauthenticated or a handshake-failure Unavailable", code, err)
	}
}

// TestAuthForeignCertificate rejects a client certificate that chains to
// some other root: VerifyClientCertIfGiven still verifies every presented
// certificate, so the handshake fails (Unavailable), or the interceptor
// answers Unauthenticated.
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

	client := h.client(foreignCert, foreignKey)
	_, err = client.ClaimJob(t.Context(), claimRequest(zoneID))
	if err == nil {
		t.Fatal("claim with a foreign certificate must fail")
	}
	code := status.Code(err)
	if code != codes.Unavailable && code != codes.Unauthenticated {
		t.Fatalf("code = %s (%v), want Unauthenticated or a handshake-failure Unavailable", code, err)
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
// the handshake succeeds (VerifyClientCertIfGiven), and every method of the
// service except Enroll answers Unauthenticated. The methods come from the
// service descriptor, so a future RPC is covered without editing this test
// and is callable anonymously only once it is allowlisted explicitly.
func TestEveryMethodButEnrollNeedsACertificate(t *testing.T) {
	h := newHarness(t)
	conn := h.conn(nil, nil)
	desc := workerpb.WorkerService_ServiceDesc
	if len(unauthenticatedMethods) != 1 || !unauthenticatedMethods[workerpb.WorkerService_Enroll_FullMethodName] {
		t.Fatalf("only Enroll may skip authentication: %v", unauthenticatedMethods)
	}
	for _, m := range desc.Methods {
		method := "/" + desc.ServiceName + "/" + m.MethodName
		if method == workerpb.WorkerService_Enroll_FullMethodName {
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
	_, err = h.client(certPEM, keyPEM).ClaimJob(t.Context(), claimRequest(zoneID))
	if code := status.Code(err); code != codes.Unavailable && code != codes.Unauthenticated {
		t.Fatalf("expired certificate: %v, want a refused handshake", err)
	}
}
