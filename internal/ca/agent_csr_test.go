package ca_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
)

// TestSignAgentCSRRoundTrip: the agent sends a CSR (its own P-256 key) and
// the manager names the certificate — the CSR's subject and SANs are
// ignored, the leaf carries the pulsix-agent common name and the org URI
// SAN only, and VerifyClient reads it back as a KindAgent identity.
func TestSignAgentCSRRoundTrip(t *testing.T) {
	a := newCA(t)
	org := uuid.Must(uuid.NewV7())
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// The CSR's own subject and SANs are ignored: the CA names the agent.
	req := csrPEM(t, key, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "evil"}, DNSNames: []string{"manager"}})
	certPEM, crt, err := a.SignAgentCSR(req, org, "agt-1")
	if err != nil {
		t.Fatal(err)
	}
	if crt.Subject.CommonName != "pulsix-agent agt-1" || len(crt.DNSNames) != 0 {
		t.Fatalf("subject = %q, dns = %v", crt.Subject.CommonName, crt.DNSNames)
	}
	if got, ok := uriSAN(crt, "urn:pulsix:org:"); !ok || got != "urn:pulsix:org:"+org.String() {
		t.Fatalf("org SAN = %q, %v", got, ok)
	}
	if _, ok := uriSAN(crt, "urn:pulsix:zone:"); ok {
		t.Fatal("an agent cert must have no zone SAN")
	}
	if got := crt.NotAfter.Sub(crt.NotBefore); got != ca.AgentCertValidity {
		t.Fatalf("validity = %v, want AgentCertValidity", got)
	}
	if !crt.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey) {
		t.Fatal("the certificate must carry the CSR's public key")
	}
	id, err := a.VerifyClient([][]byte{crt.Raw})
	if err != nil || id.Kind != ca.KindAgent || id.OrgID == nil || *id.OrgID != org ||
		id.WorkerName != "agt-1" || id.ZoneID != (uuid.UUID{}) {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	parsed, err := ca.ParseLeaf(certPEM)
	if err != nil || parsed.SerialNumber.Cmp(crt.SerialNumber) != 0 {
		t.Fatalf("ParseLeaf = %v, %v", parsed, err)
	}
}

func TestSignAgentCSRRejects(t *testing.T) {
	a := newCA(t)
	org := uuid.Must(uuid.NewV7())
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	good, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tampered := csrPEM(t, good, &x509.CertificateRequest{})
	block, _ := pem.Decode(tampered)
	block.Bytes[len(block.Bytes)-1] ^= 0xff
	valid := csrPEM(t, good, &x509.CertificateRequest{})
	cases := map[string]struct {
		csr       []byte
		name      string
		wantFault error
	}{
		"not pem":        {csr: []byte("garbage"), name: "agt", wantFault: ca.ErrBadCSR},
		"rsa key":        {csr: csrPEM(t, rsaKey, &x509.CertificateRequest{}), name: "agt", wantFault: ca.ErrBadCSR},
		"bad signature":  {csr: pem.EncodeToMemory(block), name: "agt", wantFault: ca.ErrBadCSR},
		"empty name":     {csr: valid, name: "", wantFault: ca.ErrInvalidInput},
		"overlong name":  {csr: valid, name: strings.Repeat("a", 65), wantFault: ca.ErrInvalidInput},
		"64-rune name":   {csr: valid, name: strings.Repeat("a", 64), wantFault: nil},
		"common invalid": {csr: []byte("garbage"), name: "", wantFault: nil},
	}
	for name, tc := range cases {
		_, _, err := a.SignAgentCSR(tc.csr, org, tc.name)
		switch {
		case tc.wantFault != nil && !errors.Is(err, tc.wantFault):
			t.Errorf("%s: %v, want %v", name, err, tc.wantFault)
		case tc.wantFault == nil && name == "common invalid":
			if !errors.Is(err, ca.ErrBadCSR) && !errors.Is(err, ca.ErrInvalidInput) {
				t.Errorf("%s: %v, want ErrBadCSR or ErrInvalidInput", name, err)
			}
		case tc.wantFault == nil && err != nil:
			t.Errorf("%s: %v, want success", name, err)
		}
	}
}

func TestSignAgentCSRBeforeEnsureFails(t *testing.T) {
	box := testBox()
	a, err := ca.New(&fakeStore{}, box, ca.Config{CommonName: "pulsix-manager"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.SignAgentCSR([]byte("x"), uuid.Must(uuid.NewV7()), "agt"); !errors.Is(err, ca.ErrNotReady) {
		t.Fatalf("SignAgentCSR before Ensure: %v, want ErrNotReady", err)
	}
}
