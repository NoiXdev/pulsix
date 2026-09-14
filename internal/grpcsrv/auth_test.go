// Auth: what the mTLS interceptor does to connections with no certificate,
// a foreign certificate, and a certificate whose serial the registry no
// longer knows.
package grpcsrv

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// orgID/zoneID are fixed so the harness's issued certificates are stable.
var (
	orgID  = uuid.MustParse("11111111-1111-7111-8111-111111111111")
	zoneID = uuid.MustParse("22222222-2222-7222-8222-222222222222")
)

// TestAuthUnauthenticated rejects a connection that presents no client
// certificate. With tls.RequireAndVerifyClientCert the TLS layer refuses
// during the handshake, so the caller sees a connection-level failure —
// typically codes.Unavailable with a handshake message — and never reaches
// the interceptor; the assert accepts either that or the interceptor's own
// codes.Unauthenticated, and the documentation point is: no certificate, no
// service.
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
// some other root: same layer as TestAuthUnauthenticated (the TLS
// verification fails the handshake), asserted identically.
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

// TestAuthRevokedWorker: the certificate chains to the instance CA, but its
// serial is not in the worker registry — the interceptor's own layer, so
// the specific PermissionDenied contract applies.
func TestAuthRevokedWorker(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM, _ := h.issue("unregistered", orgID, zoneID, false) // never registered
	client := h.client(certPEM, keyPEM)

	_, err := client.ClaimJob(t.Context(), claimRequest(zoneID))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %s (%v), want PermissionDenied", status.Code(err), err)
	}
	if !strings.Contains(status.Convert(err).Message(), "unknown or revoked worker") {
		t.Fatalf("message %q must name the revoked worker", status.Convert(err).Message())
	}
}

// claimRequest builds a ClaimJobRequest for one zone.
func claimRequest(zone uuid.UUID) *workerpb.ClaimJobRequest {
	return &workerpb.ClaimJobRequest{ZoneIds: []string{zone.String()}}
}
