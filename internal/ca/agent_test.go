package ca_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
)

func TestIssueAgentCertificateRoundTrip(t *testing.T) {
	a := newCA(t)
	org := uuid.Must(uuid.NewV7())
	certPEM, keyPEM, serial, err := a.IssueAgentCert(t.Context(), org, "agt-1")
	if err != nil {
		t.Fatal(err)
	}
	if serial == nil || serial.Sign() <= 0 {
		t.Fatalf("serial = %v", serial)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("certPEM has no CERTIFICATE block")
	}
	crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if crt.Subject.CommonName != "pulsix-agent agt-1" {
		t.Errorf("cn = %q", crt.Subject.CommonName)
	}
	if got, ok := uriSAN(crt, "urn:pulsix:org:"); !ok || got != "urn:pulsix:org:"+org.String() {
		t.Errorf("org SAN = %q, %v", got, ok)
	}
	if _, ok := uriSAN(crt, "urn:pulsix:zone:"); ok {
		t.Error("an agent cert must have no zone SAN")
	}
	if crt.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Error("missing DigitalSignature key usage")
	}
	if len(crt.ExtKeyUsage) != 1 || crt.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Errorf("ext key usage = %v", crt.ExtKeyUsage)
	}
	if got := crt.NotAfter.Sub(crt.NotBefore); got != ca.AgentCertValidity {
		t.Errorf("validity = %v, want AgentCertValidity", got)
	}
	if got := crt.NotAfter.Sub(crt.NotBefore); got != 90*24*time.Hour {
		t.Errorf("validity = %v, want 90 days", got)
	}
	// The key matches the certificate.
	k, _ := pem.Decode(keyPEM)
	priv, err := x509.ParseECPrivateKey(k.Bytes)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if !priv.PublicKey.Equal(crt.PublicKey) {
		t.Error("key does not match certificate")
	}
	// VerifyClient accepts it as an agent identity.
	id, err := a.VerifyClient([][]byte{crt.Raw})
	if err != nil {
		t.Fatalf("VerifyClient: %v", err)
	}
	if id.Kind != ca.KindAgent {
		t.Errorf("kind = %v, want KindAgent", id.Kind)
	}
	if id.Serial.Cmp(serial) != 0 || id.OrgID == nil || *id.OrgID != org || id.WorkerName != "agt-1" {
		t.Errorf("identity = %+v", id)
	}
	if id.ZoneID != uuid.Nil {
		t.Errorf("agent identity carries a zone: %+v", id)
	}
}

func TestIssueAgentCertValidatesName(t *testing.T) {
	a := newCA(t)
	org := uuid.Must(uuid.NewV7())
	if _, _, _, err := a.IssueAgentCert(t.Context(), org, ""); !errors.Is(err, ca.ErrInvalidInput) {
		t.Errorf("empty name: %v, want ErrInvalidInput", err)
	}
	if _, _, _, err := a.IssueAgentCert(t.Context(), org, strings.Repeat("a", 65)); !errors.Is(err, ca.ErrInvalidInput) {
		t.Errorf("65-char name: %v, want ErrInvalidInput", err)
	}
	if _, _, _, err := a.IssueAgentCert(t.Context(), org, strings.Repeat("a", 64)); err != nil {
		t.Errorf("64-char name: %v, want accepted", err)
	}
}

func TestIssueAgentCertBeforeEnsureFails(t *testing.T) {
	a, err := ca.New(&fakeStore{}, testBox(), ca.Config{CommonName: "pulsix-manager"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.IssueAgentCert(t.Context(), uuid.Must(uuid.NewV7()), "w"); !errors.Is(err, ca.ErrNotReady) {
		t.Fatalf("IssueAgentCert before Ensure: %v, want ErrNotReady", err)
	}
}

// failAfterReader yields n successful reads and then fails, so a test can
// starve a later step of IssueAgentCert while the earlier ones (serial
// draw, key generation) already got their entropy.
type failAfterReader struct {
	n int
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, errors.New("no entropy")
	}
	r.n--
	for i := range p {
		p[i] = 0x42
	}
	return len(p), nil
}

func TestIssueAgentCertFailsWithoutEntropy(t *testing.T) {
	// Seed a valid sealed CA so Ensure takes the found path, which consumes
	// no entropy; only IssueAgentCert then runs into the failing reader.
	store := &fakeStore{saved: sealForTest(t, mustJSON(t, sealedCA{KeyPEM: testKeyPEM(t), CertPEM: testCertPEM(t)}))}
	org := uuid.Must(uuid.NewV7())
	for n := 0; n <= 4; n++ {
		a, err := ca.New(store, testBox(), ca.Config{CommonName: "pulsix-manager"},
			ca.WithRand(&failAfterReader{n: n}))
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure with a valid stored CA: %v", err)
		}
		if _, _, _, err := a.IssueAgentCert(t.Context(), org, "agt-1"); err == nil {
			t.Fatalf("IssueAgentCert without entropy (after %d reads) must fail", n)
		}
	}
}

// testKeyPEM mints a throwaway P-384 key as the PEM the sealed blob needs.
func testKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
}

// testCertPEM self-signs a throwaway root as the PEM the sealed blob
// needs; Ensure only parses it, the pool it feeds is never consulted here.
func testCertPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          ca.NewSerial(rand.Reader),
		Subject:               pkix.Name{CommonName: "pulsix-manager"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
