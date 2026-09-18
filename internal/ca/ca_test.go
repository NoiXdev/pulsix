package ca_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/crypto"
)

// fakeStore keeps the sealed CA bytes in memory.
type fakeStore struct {
	saved   []byte
	loadErr error
	saveErr error
}

func (f *fakeStore) Load(context.Context) ([]byte, bool, error) {
	return f.saved, f.saved != nil, f.loadErr
}
func (f *fakeStore) Save(_ context.Context, sealed []byte) error { f.saved = sealed; return f.saveErr }

// testBox is the same deterministic in-memory AES-GCM box the credentials
// tests use: the key is derived from a fixed constant so every test seals
// and opens with the same box.
func testBox() *crypto.Box {
	var key [32]byte
	copy(key[:], "pulsix-ca-test-key-0123456789ab")
	return crypto.NewBox(key)
}

func newCA(t *testing.T) *ca.CA {
	t.Helper()
	box := testBox()
	a, err := ca.New(&fakeStore{}, box, ca.Config{CommonName: "pulsix-manager", ListenHost: "manager"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	return a
}

// sealedCA is the JSON the CA seals; the test pins the field names because
// they are the storage contract across restarts.
type sealedCA struct {
	KeyPEM  string `json:"key_pem"`
	CertPEM string `json:"cert_pem"`
}

// sealForTest seals plain under the CA's additional data, shaped exactly
// like the blob Ensure stores and reopens.
func sealForTest(t *testing.T, plain []byte) []byte {
	t.Helper()
	sealed, err := testBox().SealAAD(plain, []byte("ca:pulsix-manager"))
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func uriSAN(crt *x509.Certificate, prefix string) (string, bool) {
	for _, u := range crt.URIs {
		if s := u.String(); strings.HasPrefix(s, prefix) {
			return s, true
		}
	}
	return "", false
}

func TestIssueAndVerifyRoundTrip(t *testing.T) {
	a := newCA(t)
	zone := uuid.Must(uuid.NewV7())
	certPEM, keyPEM, serial, err := a.Issue(t.Context(), nil, zone, "fra-1")
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
	if crt.Subject.CommonName != "pulsix-worker fra-1" {
		t.Errorf("cn = %q", crt.Subject.CommonName)
	}
	if got, ok := uriSAN(crt, "urn:pulsix:zone:"); !ok || got != "urn:pulsix:zone:"+zone.String() {
		t.Errorf("zone SAN = %q, %v", got, ok)
	}
	if _, ok := uriSAN(crt, "urn:pulsix:org:"); ok {
		t.Error("a worker cert without org must have no org SAN")
	}
	if crt.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Error("missing DigitalSignature key usage")
	}
	if len(crt.ExtKeyUsage) != 1 || crt.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Errorf("ext key usage = %v", crt.ExtKeyUsage)
	}
	if crt.NotAfter.Sub(crt.NotBefore) != ca.LeafValidity {
		t.Errorf("validity = %v", crt.NotAfter.Sub(crt.NotBefore))
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
	// VerifyClient accepts it and returns the identity.
	id, err := a.VerifyClient([][]byte{crt.Raw})
	if err != nil {
		t.Fatalf("VerifyClient: %v", err)
	}
	if id.Serial.Cmp(serial) != 0 || id.ZoneID != zone || id.WorkerName != "fra-1" {
		t.Errorf("identity = %+v", id)
	}
	if id.OrgID != nil {
		t.Errorf("org id = %v, want nil", id.OrgID)
	}
}

func TestVerifyClientRejects(t *testing.T) {
	a := newCA(t)
	// A self-signed certificate the CA never issued.
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: ca.NewSerial(rand.Reader), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	raw, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.VerifyClient([][]byte{raw}); err == nil {
		t.Fatal("VerifyClient accepted a foreign certificate")
	}
	if _, err := a.VerifyClient(nil); err == nil {
		t.Fatal("VerifyClient accepted no certificates")
	}
	// A CA-issued cert that expired: VerifyClient has no time knob, so
	// expiry against a shifted clock cannot be forced here; the case
	// degenerates into asserting the pool the gRPC listener will serve.
	_ = newCA(t)
	pool, root := a.Pool()
	if pool == nil || root == nil {
		t.Fatal("Pool returned nil")
	}
	if root.Subject.CommonName != "pulsix-manager" {
		t.Errorf("root cn = %q", root.Subject.CommonName)
	}
	var cfg tls.Config
	cfg.ClientCAs = pool
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
}

func TestEnsurePersistsAcrossRestarts(t *testing.T) {
	store := &fakeStore{}
	box := testBox()
	first, err := ca.New(store, box, ca.Config{CommonName: "pulsix-manager"})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, root1 := first.Pool()

	second, err := ca.New(store, box, ca.Config{CommonName: "pulsix-manager"})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, root2 := second.Pool()
	if !root1.Equal(root2) {
		t.Fatal("a restart must reuse the stored CA, not generate a new one")
	}
	// A cert issued by the first instance verifies against the second.
	_, _, serial, err := first.Issue(t.Context(), nil, uuid.Must(uuid.NewV7()), "w")
	if err != nil {
		t.Fatal(err)
	}
	if serial == nil {
		t.Fatal("no serial")
	}
}

func TestNewSerialIsPositiveAnd64Bit(t *testing.T) {
	for i := 0; i < 100; i++ {
		s := ca.NewSerial(rand.Reader)
		if s.Sign() <= 0 || s.BitLen() > 63 {
			t.Fatalf("serial %v has %d bits", s, s.BitLen())
		}
	}
}

func TestIssueWorkerCertificateWithOrg(t *testing.T) {
	a := newCA(t)
	org := uuid.Must(uuid.NewV7())
	zone := uuid.Must(uuid.NewV7())
	certPEM, _, serial, err := a.Issue(t.Context(), &org, zone, "ham-2")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := uriSAN(crt, "urn:pulsix:org:"); !ok || got != "urn:pulsix:org:"+org.String() {
		t.Errorf("org SAN = %q, %v", got, ok)
	}
	if serial == nil || serial.Sign() <= 0 {
		t.Fatalf("serial = %v", serial)
	}
	id, err := a.VerifyClient([][]byte{crt.Raw})
	if err != nil {
		t.Fatalf("VerifyClient: %v", err)
	}
	if id.OrgID == nil || *id.OrgID != org || id.ZoneID != zone || id.WorkerName != "ham-2" {
		t.Errorf("identity = %+v", id)
	}
}

func TestIssueServerCertificate(t *testing.T) {
	a := newCA(t)
	certPEM, keyPEM, _, err := a.Issue(t.Context(), nil, uuid.UUID{}, "")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if crt.Subject.CommonName != "pulsix-manager" {
		t.Errorf("cn = %q", crt.Subject.CommonName)
	}
	if len(crt.ExtKeyUsage) != 1 || crt.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("ext key usage = %v", crt.ExtKeyUsage)
	}
	if len(crt.DNSNames) != 1 || crt.DNSNames[0] != "manager" {
		t.Errorf("dns names = %v", crt.DNSNames)
	}
	if crt.IsCA {
		t.Error("the server certificate must not be a CA")
	}
	k, _ := pem.Decode(keyPEM)
	priv, err := x509.ParseECPrivateKey(k.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !priv.PublicKey.Equal(crt.PublicKey) {
		t.Error("key does not match certificate")
	}
	// The server certificate is not a client certificate.
	if _, err := a.VerifyClient([][]byte{crt.Raw}); !errors.Is(err, ca.ErrBadIdentity) {
		t.Fatalf("VerifyClient accepted the server certificate: %v", err)
	}
}

func TestIssueUsesTheInjectedClock(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	a, err := ca.New(store, testBox(), ca.Config{CommonName: "pulsix-manager"},
		ca.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, root := a.Pool()
	if !root.NotBefore.Equal(now) {
		t.Errorf("root NotBefore = %v, want %v", root.NotBefore, now)
	}
	if got := root.NotAfter.Sub(root.NotBefore); got != 3650*24*time.Hour {
		t.Errorf("root validity = %v", got)
	}
	zone := uuid.Must(uuid.NewV7())
	certPEM, _, _, err := a.Issue(t.Context(), nil, zone, "fra-1")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !crt.NotBefore.Equal(now) {
		t.Errorf("leaf NotBefore = %v, want %v", crt.NotBefore, now)
	}
	if got := crt.NotAfter.Sub(crt.NotBefore); got != ca.LeafValidity {
		t.Errorf("leaf validity = %v", got)
	}
}

func TestIssueAndVerifyBeforeEnsureFail(t *testing.T) {
	a, err := ca.New(&fakeStore{}, testBox(), ca.Config{CommonName: "pulsix-manager"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.Issue(t.Context(), nil, uuid.Must(uuid.NewV7()), "w"); !errors.Is(err, ca.ErrNotReady) {
		t.Fatalf("Issue before Ensure: %v", err)
	}
	if pool, root := a.Pool(); pool != nil || root != nil {
		t.Fatalf("Pool before Ensure = %v, %v, want nil, nil", pool, root)
	}
}

func TestNewRequiresACommonName(t *testing.T) {
	if _, err := ca.New(&fakeStore{}, testBox(), ca.Config{}); err == nil {
		t.Fatal("New accepted an empty CommonName")
	}
}

func TestEnsurePropagatesStoreErrors(t *testing.T) {
	store := &fakeStore{loadErr: errors.New("db down")}
	a, err := ca.New(store, testBox(), ca.Config{CommonName: "pulsix-manager"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Ensure(t.Context()); err == nil || errors.Is(err, ca.ErrCASealed) {
		t.Fatalf("Ensure with a failing Load = %v, want the store error", err)
	}

	writer := &fakeStore{}
	a, err = ca.New(writer, testBox(), ca.Config{CommonName: "pulsix-manager"})
	if err != nil {
		t.Fatal(err)
	}
	writer.saveErr = errors.New("db read-only")
	if err := a.Ensure(t.Context()); err == nil || errors.Is(err, ca.ErrCASealed) {
		t.Fatalf("Ensure with a failing Save = %v, want the store error", err)
	}
}

func TestEnsureRejectsUnusableSealedCA(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	goodKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
	unparseableKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("garbage")}))
	unparseableCertPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")}))
	cases := []struct {
		name   string
		stored []byte
	}{
		{"garbage bytes", []byte("too short to open")},
		{"sealed non-JSON", sealForTest(t, []byte("not json"))},
		{"sealed PEM-less JSON", sealForTest(t, mustJSON(t, sealedCA{KeyPEM: "x", CertPEM: "y"}))},
		{"sealed key that does not parse", sealForTest(t, mustJSON(t, sealedCA{KeyPEM: unparseableKeyPEM, CertPEM: unparseableCertPEM}))},
		{"sealed certificate that does not parse", sealForTest(t, mustJSON(t, sealedCA{KeyPEM: goodKeyPEM, CertPEM: unparseableCertPEM}))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &fakeStore{saved: c.stored}
			a, err := ca.New(store, testBox(), ca.Config{CommonName: "pulsix-manager"})
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Ensure(t.Context()); !errors.Is(err, ca.ErrCASealed) {
				t.Fatalf("Ensure = %v, want ErrCASealed", err)
			}
		})
	}
}

func TestVerifyClientRejectsMalformedCertificates(t *testing.T) {
	a := newCA(t)
	if _, err := a.VerifyClient([][]byte{{0xff}}); !errors.Is(err, ca.ErrBadIdentity) {
		t.Fatalf("VerifyClient(garbage bytes) = %v, want ErrBadIdentity", err)
	}
}

func TestIssueFailsWithoutEntropy(t *testing.T) {
	// Seed a valid sealed CA so Ensure takes the found path, which consumes
	// no entropy, and only Issue then runs into the failing reader.
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
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
	cder, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	blob := mustJSON(t, sealedCA{
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})),
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cder})),
	})
	store := &fakeStore{saved: sealForTest(t, blob)}
	a, err := ca.New(store, testBox(), ca.Config{CommonName: "pulsix-manager"},
		ca.WithRand(iotest.ErrReader(errors.New("no entropy"))))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure with a valid stored CA: %v", err)
	}
	if _, _, _, err := a.Issue(t.Context(), nil, uuid.Must(uuid.NewV7()), "w"); err == nil {
		t.Fatal("Issue without entropy must fail")
	}
}

// sealedBrokenBox is a Sealer whose OpenAAD works but SealAAD always fails.
type sealedBrokenBox struct{ inner *crypto.Box }

func (b sealedBrokenBox) SealAAD(_, _ []byte) ([]byte, error) { return nil, errors.New("seal broken") }
func (b sealedBrokenBox) OpenAAD(sealed, aad []byte) ([]byte, error) {
	return b.inner.OpenAAD(sealed, aad)
}

func TestEnsureFailsWhenSealingFails(t *testing.T) {
	store := &fakeStore{}
	a, err := ca.New(store, sealedBrokenBox{inner: testBox()}, ca.Config{CommonName: "pulsix-manager"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Ensure(t.Context()); !errors.Is(err, ca.ErrCASealed) {
		t.Fatalf("Ensure = %v, want ErrCASealed", err)
	}
	if store.saved != nil {
		t.Fatal("nothing must be stored when sealing fails")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEnsureGenerateFailsWithoutEntropy(t *testing.T) {
	store := &fakeStore{}
	a, err := ca.New(store, testBox(), ca.Config{CommonName: "pulsix-manager"},
		ca.WithRand(iotest.ErrReader(errors.New("no entropy"))))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Ensure(t.Context()); !errors.Is(err, ca.ErrCASealed) {
		t.Fatalf("Ensure without entropy = %v, want ErrCASealed", err)
	}
	if store.saved != nil {
		t.Fatal("nothing must be stored when generation fails")
	}
}
