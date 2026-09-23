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

// TestCheckAgentName: the 1-64-rune constraint the enrollment paths apply
// pre-claim and the signers apply before issuing.
func TestCheckAgentName(t *testing.T) {
	for name, want := range map[string]bool{
		"":                      false,
		"agt-1":                 true,
		strings.Repeat("a", 64): true,
		strings.Repeat("a", 65): false,
	} {
		err := ca.CheckAgentName(name)
		if got := err == nil; got != want {
			t.Errorf("CheckAgentName(%q...) = %v (%v)", name, got, err)
		}
	}
}

// failAfterReader yields n successful reads and then fails, so a test can
// starve a later step of SignAgentCSR while the earlier ones (the serial
// draw) already got their entropy.
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

func TestSignAgentCSRFailsWithoutEntropy(t *testing.T) {
	// Seed a valid sealed CA so Ensure takes the found path, which consumes
	// no entropy; only SignAgentCSR then runs into the failing reader (the
	// serial draw first, the signature second).
	store := &fakeStore{saved: sealForTest(t, mustJSON(t, sealedCA{KeyPEM: testKeyPEM(t), CertPEM: testCertPEM(t)}))}
	org := uuid.Must(uuid.NewV7())
	for n := 0; n <= 1; n++ {
		a, err := ca.New(store, testBox(), ca.Config{CommonName: "pulsix-manager"},
			ca.WithRand(&failAfterReader{n: n}))
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure with a valid stored CA: %v", err)
		}
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		csr := csrPEM(t, key, &x509.CertificateRequest{})
		if _, _, err := a.SignAgentCSR(csr, org, "agt-1"); err == nil {
			t.Fatalf("SignAgentCSR without entropy (after %d reads) must fail", n)
		}
	}
}
