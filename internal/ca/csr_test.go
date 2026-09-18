package ca_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
)

func csrPEM(t *testing.T, key crypto.Signer, tpl *x509.CertificateRequest) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, tpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestSignCSRAcceptedKeysAndIdentity(t *testing.T) {
	a := newCA(t)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	for name, key := range map[string]crypto.Signer{"p256": p256, "p384 (legacy bundles)": p384, "ed25519": edKey} {
		t.Run(name, func(t *testing.T) {
			// The CSR's own subject and SANs are ignored: the CA names the worker.
			req := csrPEM(t, key, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "evil"}, DNSNames: []string{"manager"}})
			certPEM, crt, err := a.SignCSR(req, org, zone, "fra-1")
			if err != nil {
				t.Fatal(err)
			}
			if crt.Subject.CommonName != "pulsix-worker fra-1" || len(crt.DNSNames) != 0 {
				t.Fatalf("subject = %q, dns = %v", crt.Subject.CommonName, crt.DNSNames)
			}
			if got := crt.NotAfter.Sub(crt.NotBefore); got != ca.LeafValidity {
				t.Fatalf("validity = %v", got)
			}
			id, err := a.VerifyClient([][]byte{crt.Raw})
			if err != nil || id.ZoneID != zone || id.OrgID == nil || *id.OrgID != org || id.WorkerName != "fra-1" {
				t.Fatalf("identity = %+v, %v", id, err)
			}
			parsed, err := ca.ParseLeaf(certPEM)
			if err != nil || parsed.SerialNumber.Cmp(crt.SerialNumber) != 0 {
				t.Fatalf("ParseLeaf = %v, %v", parsed, err)
			}
		})
	}
}

func TestSignCSRRejects(t *testing.T) {
	a := newCA(t)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	p224, _ := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	good, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tampered := csrPEM(t, good, &x509.CertificateRequest{})
	block, _ := pem.Decode(tampered)
	block.Bytes[len(block.Bytes)-1] ^= 0xff
	cases := map[string][]byte{
		"not pem":        []byte("garbage"),
		"wrong pem type": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}}),
		"rsa key":        csrPEM(t, rsaKey, &x509.CertificateRequest{}),
		"p224 key":       csrPEM(t, p224, &x509.CertificateRequest{}),
		"bad signature":  pem.EncodeToMemory(block),
	}
	for name, in := range cases {
		if _, _, err := a.SignCSR(in, org, zone, "w"); !errors.Is(err, ca.ErrBadCSR) {
			t.Errorf("%s: %v, want ErrBadCSR", name, err)
		}
	}
}

func TestCAPEMAndPin(t *testing.T) {
	a := newCA(t)
	_, root := a.Pool()
	block, _ := pem.Decode(a.CAPEM())
	if block == nil || string(block.Bytes) != string(root.Raw) {
		t.Fatal("CAPEM is the root certificate")
	}
	if a.CAPin() != sha256.Sum256(root.Raw) {
		t.Fatal("CAPin is SHA-256 of the root DER")
	}
	if _, err := ca.ParseLeaf([]byte("x")); err == nil {
		t.Fatal("ParseLeaf accepted garbage")
	}
}
