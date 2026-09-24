// Identity persistence tests: file layout, owner-only permissions, atomic
// writes, validity (parsed + unexpired + the org SAN that makes it an
// agent certificate) and the renewal window — against certificates the
// REAL instance CA issues, so "carries the org SAN" is pinned against the
// actual SAN format, not a local re-implementation.
package agent

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveEnrollmentAndLoadIdentityRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key, csrPEM := newTestKey(t)
	certPEM, caPEM := issueAgentCert(t, csrPEM)

	if err := SaveEnrollment(dir, key, certPEM, caPEM, "host-uuid-1"); err != nil {
		t.Fatalf("SaveEnrollment: %v", err)
	}

	id, err := LoadIdentity(dir)
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if id.HostID() != "host-uuid-1" {
		t.Errorf("HostID = %q, want host-uuid-1", id.HostID())
	}
	if id.Leaf() == nil {
		t.Fatal("Leaf = nil")
	}
	if got := id.Certificate(); got == nil {
		t.Fatal("Certificate = nil, want a usable client certificate")
	}
	signer := id.Signer()
	if signer == nil {
		t.Fatal("Signer = nil")
	}
	if pub, ok := id.Leaf().PublicKey.(interface{ Equal(crypto.PublicKey) bool }); !ok || !pub.Equal(key.Public()) {
		t.Error("the loaded identity's certificate does not match the persisted key")
	}

	// Owner-only permissions are part of the contract, pinned by test:
	// the data dir carries a long-lived private key and the CA chain.
	for _, name := range []string{certFileName, keyFileName, caFileName, metaFileName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, got)
		}
	}
}

func TestLoadIdentityEmptyDir(t *testing.T) {
	id, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if id != nil {
		t.Fatalf("LoadIdentity in an empty dir = %v, want nil, nil", id)
	}
}

func TestLoadIdentityKeyOnlyDir(t *testing.T) {
	// A key without a certificate (the first start crashed between key
	// generation and enrollment) is not a usable identity, but the key
	// must survive: the enrollment path reuses it (key generated ONCE).
	dir := t.TempDir()
	if _, err := LoadOrGenerateKey(filepath.Join(dir, keyFileName)); err != nil {
		t.Fatal(err)
	}
	id, err := LoadIdentity(dir)
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if id != nil {
		t.Fatalf("LoadIdentity with a key but no certificate = %v, want nil", id)
	}
}

func TestLoadIdentityGarbageReadsAsAbsent(t *testing.T) {
	// Garbage identity files are not an error at load time: they read as
	// "no identity", and the enrollment path (which refuses to overwrite
	// an unparseable key) decides what is recoverable.
	dir := t.TempDir()
	for _, f := range []string{certFileName, keyFileName} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("garbage"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id, err := LoadIdentity(dir)
	if err != nil {
		t.Fatalf("LoadIdentity on garbage: %v", err)
	}
	if id != nil {
		t.Fatalf("LoadIdentity on garbage = %v, want nil (reads as absent)", id)
	}
}

func TestIdentityValidity(t *testing.T) {
	id, _ := enrollDataDir(t)
	leaf := id.Leaf()

	if !id.Valid(leaf.NotBefore.Add(time.Hour)) {
		t.Error("a freshly issued identity must be valid")
	}
	if id.Valid(leaf.NotAfter.Add(time.Minute)) {
		t.Error("an expired identity must not be valid")
	}
	if id.Valid(leaf.NotBefore.Add(-time.Minute)) {
		t.Error("an identity from the future (NotBefore not reached) must not be valid")
	}
}

func TestIdentityValidityRequiresOrgSAN(t *testing.T) {
	// A certificate with no pulsix URI SAN (a plain server-shaped
	// certificate) is never a valid agent identity: skipping enrollment
	// with one would hand the manager a certificate the interceptor
	// refuses.
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    now,
		NotAfter:     now.Add(time.Hour),
		DNSNames:     []string{"plain-server.example"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	id := &Identity{leaf: leaf}
	if id.Valid(now.Add(time.Minute)) {
		t.Error("a certificate without the org SAN must not be valid")
	}
}

func TestIdentityNeedsRenewal(t *testing.T) {
	id, _ := enrollDataDir(t)
	leaf := id.Leaf()
	lifetime := leaf.NotAfter.Sub(leaf.NotBefore)

	// Fresh: two full thirds of the lifetime left — no renewal.
	if id.NeedsRenewal(leaf.NotBefore.Add(lifetime / 3)) {
		t.Error("renewal must not trigger while two thirds of the lifetime remain")
	}
	// The renewal window: less than a third left.
	if !id.NeedsRenewal(leaf.NotBefore.Add(2*lifetime/3 + time.Minute)) {
		t.Error("renewal must trigger once a third of the lifetime is left")
	}
}

func TestIdentityReplaceRenews(t *testing.T) {
	id, dir := enrollDataDir(t)
	before := id.Leaf()

	// A renewal swaps the certificate for the SAME key's new one: the CSR
	// is signed with the persisted key.
	signer, ok := id.Signer().(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("Signer = %T, want *ecdsa.PrivateKey", id.Signer())
	}
	renewedCSR, err := MakeCSR(signer, testAgentName)
	if err != nil {
		t.Fatal(err)
	}
	caInst := newTestCA(t)
	renewedPEM, _, err := caInst.SignAgentCSR(renewedCSR, testOrgID, testAgentName)
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Replace(renewedPEM); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	// The in-memory leaf is the renewed one; a reload sees it too (the
	// write is atomic — what is readable is what was persisted). The
	// serial is the discriminating check: both certificates may share the
	// same one-second validity window, but the CA draws fresh serials.
	reloaded, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Leaf().SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Error("the renewed certificate must replace the old one on disk")
	}
	if got := reloaded.Certificate(); got == nil {
		t.Fatal("reloaded certificate = nil, want usable")
	}
	if reloaded.HostID() != id.HostID() {
		t.Errorf("HostID after renewal = %q, want %q", reloaded.HostID(), id.HostID())
	}
}

func TestIdentityReplaceRejectsWrongKey(t *testing.T) {
	id, _ := enrollDataDir(t)
	before := id.Leaf()

	// A certificate minted for a DIFFERENT key must change nothing.
	_, foreignCSR := newTestKey(t)
	caInst := newTestCA(t)
	foreignPEM, _, err := caInst.SignAgentCSR(foreignCSR, testOrgID, testAgentName)
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Replace(foreignPEM); err == nil {
		t.Fatal("Replace with a foreign key's certificate = nil error, want an error")
	}
	if !id.Leaf().Equal(before) {
		t.Error("a refused Replace must leave the identity unchanged")
	}
}

func TestIdentityReplaceRejectsGarbage(t *testing.T) {
	id, _ := enrollDataDir(t)
	before := id.Leaf()
	if err := id.Replace([]byte("garbage")); err == nil {
		t.Fatal("Replace with garbage = nil error, want an error")
	}
	if !id.Leaf().Equal(before) {
		t.Error("a refused Replace must leave the identity unchanged")
	}
}

// A renewal swaps the identity's certificate while the TLS stack may be
// reading it from another goroutine: DialAgent hands every handshake
// i.Certificate() through GetClientCertificate, and the connection the
// renewal RPC itself rode is still open when Replace lands (grpc can
// re-handshake it). Reads and the swap must therefore be safe against
// each other — the race detector is the assertion here.
func TestIdentityReplaceIsSafeAgainstConcurrentHandshakeReads(t *testing.T) {
	id, _ := enrollDataDir(t)
	signer, ok := id.Signer().(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("Signer = %T, want *ecdsa.PrivateKey", id.Signer())
	}
	renewedCSR, err := MakeCSR(signer, testAgentName)
	if err != nil {
		t.Fatal(err)
	}
	renewedPEM, _, err := newTestCA(t).SignAgentCSR(renewedCSR, testOrgID, testAgentName)
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				// Exactly what a handshake and the loop do to a live
				// identity: read the certificate, the leaf and the
				// validity windows.
				if c := id.Certificate(); c == nil {
					panic("Certificate() = nil during renewal")
				}
				_ = id.Leaf()
				_ = id.Valid(time.Now())
				_ = id.NeedsRenewal(time.Now())
				_ = id.HostID()
			}
		}
	}()
	if err := id.Replace(renewedPEM); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	close(stop)
	<-done
}
