package grpcsrv

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

type testZones struct{}

func (testZones) ListZones(_ context.Context, org uuid.UUID) ([]hosts.Zone, error) {
	return []hosts.Zone{{ID: zoneID, OrgID: org, Slug: "default"}}, nil
}

// newKeyAndCSR is what a worker does before Enroll or RenewCertificate.
func newKeyAndCSR(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// csrForKey builds a CSR for a key already on hand in PEM form — what
// a real renewal sends, since the renewer signs with the identity's
// own signer (backlog 2.64).
func csrForKey(t *testing.T, keyPEMBytes []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(keyPEMBytes)
	if block == nil {
		t.Fatal("key is not PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func keyPEM(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func (h *harness) enrollmentToken(name string) enrolltoken.Token {
	h.t.Helper()
	svc := workers.NewService(h.store, workers.WithSigner(h.ca), workers.WithZones(testZones{}))
	_, token, err := svc.CreateEnrollment(context.Background(), orgID, nil, zoneID, name)
	if err != nil {
		h.t.Fatal(err)
	}
	tok, err := enrolltoken.Parse(token)
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// TestEnrollWithoutAClientCertificate: a certificate-less connection may
// call Enroll (and only Enroll); the returned certificate then
// authenticates every other method.
func TestEnrollWithoutAClientCertificate(t *testing.T) {
	h := newHarness(t)
	tok := h.enrollmentToken("fra-9")
	anonymous := h.client(nil, nil)
	if _, err := anonymous.ClaimJob(t.Context(), claimRequest(zoneID)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("ClaimJob without a certificate: %v", err)
	}
	key, csr := newKeyAndCSR(t)
	resp, err := anonymous.Enroll(t.Context(), &workerpb.EnrollRequest{TokenId: tok.ID.String(), Secret: tok.Secret, Csr: csr, Version: "v0.2c"})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(resp.GetCaChain())
	if block == nil || enrolltoken.Pin(block.Bytes) != tok.CAPin {
		t.Fatal("the returned CA must be the pinned one")
	}
	if _, err := h.client(resp.GetCertificate(), keyPEM(t, key)).ClaimJob(t.Context(), claimRequest(zoneID)); err != nil {
		t.Fatalf("the enrolled certificate must authenticate: %v", err)
	}
	if got := h.audit.Events(); len(got) == 0 || got[0].Operation != "worker.enroll" {
		t.Fatalf("audit = %+v", got)
	}
	// Single use, and every refusal looks the same.
	for name, req := range map[string]*workerpb.EnrollRequest{
		"reused token":  {TokenId: tok.ID.String(), Secret: tok.Secret, Csr: csr},
		"unknown token": {TokenId: uuid.NewString(), Secret: tok.Secret, Csr: csr},
		"garbage id":    {TokenId: "x", Secret: tok.Secret, Csr: csr},
	} {
		if _, err := anonymous.Enroll(t.Context(), req); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("%s: %v", name, err)
		}
	}
	bad := h.enrollmentToken("fra-10")
	if _, err := anonymous.Enroll(t.Context(), &workerpb.EnrollRequest{TokenId: bad.ID.String(), Secret: bad.Secret, Csr: []byte("x")}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad csr: %v", err)
	}
}

func TestEnrollIsRateLimitedPerAddress(t *testing.T) {
	h := newHarness(t)
	anonymous := h.client(nil, nil)
	for i := 0; i < enrollLimit; i++ {
		if _, err := anonymous.Enroll(t.Context(), &workerpb.EnrollRequest{TokenId: uuid.NewString()}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := anonymous.Enroll(t.Context(), &workerpb.EnrollRequest{TokenId: uuid.NewString()}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("over the limit: %v", err)
	}
	h.advance(enrollWindow)
	if _, err := anonymous.Enroll(t.Context(), &workerpb.EnrollRequest{TokenId: uuid.NewString()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("after the window: %v", err)
	}
}

func TestRenewCertificate(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEMBytes := h.worker()
	client := h.client(certPEM, keyPEMBytes)
	// The CSR asks for the key this connection authenticated with,
	// which is what the real renewer sends — it signs with the
	// identity's own signer. A CSR for any OTHER key is refused since
	// backlog 2.64; TestRenewRefusesACSRForADifferentKey covers that.
	csr := csrForKey(t, keyPEMBytes)
	resp, err := client.RenewCertificate(t.Context(), &workerpb.RenewCertificateRequest{Csr: csr})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.ParseLeaf(resp.GetCertificate())
	if err != nil || leaf.Subject.CommonName != "pulsix-worker fra-1-a" {
		t.Fatalf("renewed = %v, %v", leaf, err)
	}
	if _, err := h.client(resp.GetCertificate(), keyPEMBytes).ClaimJob(t.Context(), claimRequest(zoneID)); err != nil {
		t.Fatalf("the renewed certificate must authenticate: %v", err)
	}
	if _, err := client.ClaimJob(t.Context(), claimRequest(zoneID)); err != nil {
		t.Fatalf("the old certificate stays valid: %v", err)
	}
	if _, err := client.RenewCertificate(t.Context(), &workerpb.RenewCertificateRequest{Csr: []byte("x")}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad csr: %v", err)
	}
	if _, err := h.client(nil, nil).RenewCertificate(t.Context(), &workerpb.RenewCertificateRequest{Csr: csr}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("renew without a certificate: %v", err)
	}
}

// TestEnrollWithAClientCertificate: Enroll skips authentication by method,
// so a caller that presents a certificate (a worker re-enrolling with a new
// token) is served like an anonymous one — the token is the credential.
func TestEnrollWithAClientCertificate(t *testing.T) {
	h := newHarness(t)
	tok := h.enrollmentToken("fra-11")
	certPEM, keyPEMBytes := h.worker()
	_, csr := newKeyAndCSR(t)
	resp, err := h.client(certPEM, keyPEMBytes).Enroll(t.Context(), &workerpb.EnrollRequest{TokenId: tok.ID.String(), Secret: tok.Secret, Csr: csr})
	if err != nil || resp.GetWorkerId() == "" {
		t.Fatalf("enroll with a client certificate: %v, %v", resp, err)
	}
}

// TestEnrollNameTaken: a worker that took the token's name after the token
// was created makes the redemption AlreadyExists.
func TestEnrollNameTaken(t *testing.T) {
	h := newHarness(t)
	tok := h.enrollmentToken("fra-12")
	h.issue("fra-12", orgID, zoneID, true)
	_, csr := newKeyAndCSR(t)
	_, err := h.client(nil, nil).Enroll(t.Context(), &workerpb.EnrollRequest{TokenId: tok.ID.String(), Secret: tok.Secret, Csr: csr})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("name clash: %v, want AlreadyExists", err)
	}
}

// TestEnrollRateLimitIsPerClientAddress: the limit keys on the PEER
// address and nothing else — the port does not matter, and no caller
// can pick its own bucket by sending forwarded-for metadata, trusted
// peer or not (backlog 2.67).
//
// This used to honour x-forwarded-for when the peer fell inside
// PULSIX_TRUSTED_PROXIES, mirroring the HTTP side. On HTTP that is
// sound: Traefik overwrites the header, so the value really is the
// proxy speaking. Nothing overwrites gRPC METADATA — Docker's userland
// proxy forwards TCP and leaves it untouched — so behind a gateway
// inside a trusted range, an unauthenticated enrollment caller was
// choosing which bucket to spend. Which is the one thing a rate limit
// on an unauthenticated endpoint cannot allow.
func TestEnrollRateLimitIsPerClientAddress(t *testing.T) {
	h := newHarness(t)
	srv, err := newServer(Deps{CA: h.ca, Workers: workers.NewService(h.store, workers.WithSigner(h.ca)),
		Audit: h.audit, Clock: h.clock})
	if err != nil {
		t.Fatal(err)
	}
	call := func(remote string, forwardedFor ...string) codes.Code {
		ctx := peer.NewContext(t.Context(), &peer.Peer{Addr: net.TCPAddrFromAddrPort(netip.MustParseAddrPort(remote))})
		if len(forwardedFor) > 0 {
			ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-forwarded-for", strings.Join(forwardedFor, ", ")))
		}
		_, err := srv.Enroll(ctx, &workerpb.EnrollRequest{TokenId: uuid.NewString()})
		return status.Code(err)
	}
	for i := 0; i < enrollLimit; i++ {
		if code := call("192.0.2.1:5000"); code != codes.Unauthenticated {
			t.Fatalf("call %d: %s", i, code)
		}
	}
	for _, tc := range []struct {
		name   string
		remote string
		xff    []string
		want   codes.Code
	}{
		{"same address, another port", "192.0.2.1:6000", nil, codes.ResourceExhausted},
		{"forwarded-for from an untrusted peer is ignored", "192.0.2.1:5000", []string{"198.51.100.7"}, codes.ResourceExhausted},
		{"forwarded-for from a TRUSTED peer is ignored too", "192.0.2.1:5000", []string{"203.0.113.9"}, codes.ResourceExhausted},
		{"another address", "192.0.2.2:5000", nil, codes.Unauthenticated},
	} {
		if code := call(tc.remote, tc.xff...); code != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, code, tc.want)
		}
	}

	// Two callers arriving from the same gateway share its bucket,
	// whatever they claim about themselves. That IS the cost of
	// keying on the peer, and it is the lesser one: a shared bucket
	// makes several workers starting together back off and retry —
	// ResourceExhausted is transient since backlog 2.64 — while a
	// pickable bucket makes the limit decorative.
	for i := 0; i < enrollLimit; i++ {
		if code := call("10.0.0.1:443", "198.51.100."+string(rune('1'+i))); code != codes.Unauthenticated {
			t.Fatalf("gateway call %d: %s", i, code)
		}
	}
	if code := call("10.0.0.1:443", "198.51.100.200"); code != codes.ResourceExhausted {
		t.Fatalf("a fresh forwarded-for bought a fresh bucket: %s", code)
	}
}

// TestRenewCertificateIsAudited: a renewal records a worker.renew system
// event for the authenticated worker.
func TestRenewCertificateIsAudited(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEMBytes := h.worker()
	csr := csrForKey(t, keyPEMBytes)
	if _, err := h.client(certPEM, keyPEMBytes).RenewCertificate(t.Context(), &workerpb.RenewCertificateRequest{Csr: csr}); err != nil {
		t.Fatal(err)
	}
	got := h.audit.Events()
	if len(got) != 1 || got[0].Operation != "worker.renew" || got[0].OrgID != orgID.String() || got[0].ObjectType != "worker" {
		t.Fatalf("audit = %+v", got)
	}
}

// TestEnrollAndRenewFaultsAreGeneric: Enroll is anonymous, so a store
// fault behind it must not reach the caller as text (table names, driver
// messages); RenewCertificate answers the same way. Both are logged.
func TestEnrollAndRenewFaultsAreGeneric(t *testing.T) {
	h := newHarness(t)
	tok := h.enrollmentToken("fra-11")
	certPEM, keyPEMBytes := h.worker()
	fault := errors.New(`insert worker: ERROR: duplicate key value violates unique constraint "workers_cert_serial_key"`)
	h.store.mu.Lock()
	h.store.enrollErr, h.store.addCertErr = fault, fault
	h.store.mu.Unlock()

	_, csr := newKeyAndCSR(t)
	_, err := h.client(nil, nil).Enroll(t.Context(), &workerpb.EnrollRequest{TokenId: tok.ID.String(), Secret: tok.Secret, Csr: csr})
	if status.Code(err) != codes.Internal || status.Convert(err).Message() != "enrollment failed" {
		t.Fatalf("enroll fault: %v, want Internal \"enrollment failed\"", err)
	}
	_, err = h.client(certPEM, keyPEMBytes).RenewCertificate(t.Context(), &workerpb.RenewCertificateRequest{Csr: csrForKey(t, keyPEMBytes)})
	if status.Code(err) != codes.Internal || strings.Contains(status.Convert(err).Message(), "workers_cert_serial_key") {
		t.Fatalf("renew fault: %v, want a generic Internal", err)
	}
}

// TestRenewRefusesACSRForADifferentKey is backlog 2.64. Renew signed
// whatever CSR the authenticated worker sent, for that worker's
// identity, without checking that the key inside it is the key the
// caller just authenticated with.
//
// That is not an access escalation — the caller is already
// authenticated — it is PERSISTENCE. A worker compromised once could
// mint a full-lifetime certificate for the attacker's own key, and
// cleaning the original key off the host would not take it back: the
// attacker keeps a valid worker identity until someone revokes a
// serial nobody knows to look for. Binding the renewal to the current
// key makes it prove continuity, so recovering the host ends the
// access.
func TestRenewRefusesACSRForADifferentKey(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEMBytes := h.worker()
	client := h.client(certPEM, keyPEMBytes)

	// A CSR for a key the caller does not hold.
	_, foreignCSR := newKeyAndCSR(t)
	_, err := client.RenewCertificate(t.Context(), &workerpb.RenewCertificateRequest{Csr: foreignCSR})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a CSR for another key = %v, want InvalidArgument", err)
	}

	// The same key the connection authenticated with is accepted —
	// which is what the real renewer sends (it signs the CSR with
	// id.Signer()).
	csr := csrForKey(t, keyPEMBytes)
	resp, err := client.RenewCertificate(t.Context(), &workerpb.RenewCertificateRequest{Csr: csr})
	if err != nil {
		t.Fatalf("a renewal for the caller's own key was refused: %v", err)
	}
	if _, err := h.client(resp.GetCertificate(), keyPEMBytes).ClaimJob(t.Context(), claimRequest(zoneID)); err != nil {
		t.Fatalf("the renewed certificate must authenticate: %v", err)
	}
}
