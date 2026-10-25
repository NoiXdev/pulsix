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
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"net"
	"net/url"
	"slices"
	"strings"
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
			if id.Kind != ca.KindWorker {
				t.Fatalf("kind = %v, want KindWorker", id.Kind)
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

// TestSignCSRIgnoresWhatTheRequestAsksFor: the CSR proves possession of a
// key and nothing else. Everything else in it — subject, every SAN form,
// and extensions requesting a CA certificate or another extended key
// usage — is the enrolling party's own text, and the issued certificate
// must carry none of it.
//
// The URI SANs are why this matters beyond tidiness: identityFrom reads
// the organization and zone out of the urn:pulsix: SANs and takes the
// FIRST of each, and the presence of a zone SAN is what makes a
// certificate a worker's rather than an agent's. A CSR whose SANs reached
// the certificate would therefore let an enrolling party name its own
// organization and zone — the two values every later authorization
// decision is made against.
func TestSignCSRIgnoresWhatTheRequestAsksFor(t *testing.T) {
	a := newCA(t)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	otherOrg, otherZone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mustURL := func(s string) *url.URL {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	// basicConstraints CA:TRUE and extKeyUsage serverAuth, asked for the
	// only way a CSR can ask: as extensions it carries itself.
	caTrue, err := asn1.Marshal(struct {
		IsCA bool `asn1:"optional"`
	}{IsCA: true})
	if err != nil {
		t.Fatal(err)
	}
	serverAuth, err := asn1.Marshal([]asn1.ObjectIdentifier{{1, 3, 6, 1, 5, 5, 7, 3, 1}})
	if err != nil {
		t.Fatal(err)
	}
	req := csrPEM(t, key, &x509.CertificateRequest{
		Subject:        pkix.Name{CommonName: "pulsix-worker impostor", Organization: []string{"evil"}},
		DNSNames:       []string{"manager", "localhost"},
		EmailAddresses: []string{"root@example.test"},
		IPAddresses:    []net.IP{net.IPv4(10, 0, 0, 1)},
		URIs: []*url.URL{
			mustURL("urn:pulsix:org:" + otherOrg.String()),
			mustURL("urn:pulsix:zone:" + otherZone.String()),
		},
		ExtraExtensions: []pkix.Extension{
			{Id: asn1.ObjectIdentifier{2, 5, 29, 19}, Critical: true, Value: caTrue},
			{Id: asn1.ObjectIdentifier{2, 5, 29, 37}, Value: serverAuth},
		},
	})

	_, crt, err := a.SignCSR(req, org, zone, "fra-1")
	if err != nil {
		t.Fatal(err)
	}
	if crt.Subject.CommonName != "pulsix-worker fra-1" || len(crt.Subject.Organization) != 0 {
		t.Errorf("subject = %+v, want only the CA's own common name", crt.Subject)
	}
	if len(crt.DNSNames) != 0 || len(crt.EmailAddresses) != 0 || len(crt.IPAddresses) != 0 {
		t.Errorf("dns = %v, email = %v, ip = %v; a worker certificate carries none of these", crt.DNSNames, crt.EmailAddresses, crt.IPAddresses)
	}
	wantURIs := []string{"urn:pulsix:org:" + org.String(), "urn:pulsix:zone:" + zone.String()}
	var gotURIs []string
	for _, u := range crt.URIs {
		gotURIs = append(gotURIs, u.String())
	}
	if !slices.Equal(gotURIs, wantURIs) {
		t.Errorf("uris = %v, want exactly %v — the CA names the worker, the request does not", gotURIs, wantURIs)
	}
	if crt.IsCA || !crt.BasicConstraintsValid {
		t.Errorf("IsCA = %v, BasicConstraintsValid = %v; a leaf may never sign", crt.IsCA, crt.BasicConstraintsValid)
	}
	if !slices.Equal(crt.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) {
		t.Errorf("ext key usage = %v, want client authentication alone", crt.ExtKeyUsage)
	}
	if crt.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("key usage = %v, want DigitalSignature alone", crt.KeyUsage)
	}

	// The decisive one: the identity the manager will authorize against is
	// the CA's, not the requester's.
	id, err := a.VerifyClient([][]byte{crt.Raw})
	if err != nil {
		t.Fatal(err)
	}
	if id.OrgID == nil || *id.OrgID != org || id.ZoneID != zone || id.WorkerName != "fra-1" || id.Kind != ca.KindWorker {
		t.Fatalf("identity = %+v; the request asked for org %s zone %s", id, otherOrg, otherZone)
	}
}

// TestSignAgentCSRCannotAskForAZone is the agent half of the SAN
// injection above, and the sharper case: an agent certificate is told
// apart from a worker's by the ABSENCE of a zone SAN. A zone SAN carried
// over from the request would turn an agent's certificate into a worker
// identity — and worker methods are exactly what the two certificate
// classes are segregated to keep apart.
func TestSignAgentCSRCannotAskForAZone(t *testing.T) {
	a := newCA(t)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	zoneURI, err := url.Parse("urn:pulsix:zone:" + zone.String())
	if err != nil {
		t.Fatal(err)
	}
	req := csrPEM(t, key, &x509.CertificateRequest{URIs: []*url.URL{zoneURI}})

	_, crt, err := a.SignAgentCSR(req, org, "collector-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range crt.URIs {
		if strings.HasPrefix(u.String(), "urn:pulsix:zone:") {
			t.Fatalf("agent certificate carries a zone SAN %q — that makes it read as a worker", u)
		}
	}
	id, err := a.VerifyClient([][]byte{crt.Raw})
	if err != nil {
		t.Fatal(err)
	}
	if id.Kind != ca.KindAgent || id.ZoneID != uuid.Nil {
		t.Fatalf("identity = %+v, want an agent with no zone", id)
	}
}
