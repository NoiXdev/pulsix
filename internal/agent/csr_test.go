// CSR builder tests: P-256 keys, the "pulsix-agent <name>" common name,
// PEM round-trip through the manager's own CSR checker, and the
// load-or-generate key path that reuses a persisted key.
package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/noixdev/pulsix/internal/ca"
)

func TestMakeCSR(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := MakeCSR(key, "staging-node-1")
	if err != nil {
		t.Fatalf("MakeCSR: %v", err)
	}
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("csr = %q, want a PEM CERTIFICATE REQUEST", csrPEM)
	}
	req, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}
	if err := req.CheckSignature(); err != nil {
		t.Fatalf("CSR signature: %v", err)
	}
	if got := req.Subject.CommonName; got != "pulsix-agent staging-node-1" {
		t.Errorf("CN = %q, want %q", got, "pulsix-agent staging-node-1")
	}
	if _, ok := req.PublicKey.(*ecdsa.PublicKey); !ok {
		t.Errorf("CSR key = %T, want *ecdsa.PublicKey", req.PublicKey)
	}
	// The manager-side checker (the exact code the Enroll handler runs)
	// must accept the CSR the agent builds.
	if _, err := ca.CheckCSR([]byte(csrPEM)); err != nil {
		t.Errorf("ca.CheckCSR: %v", err)
	}
}

func TestGenerateKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if key.Curve != elliptic.P256() {
		t.Errorf("curve = %v, want P-256", key.Curve)
	}
}

func TestLoadOrGenerateKeyGeneratesOnce(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, keyFileName)

	first, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrGenerateKey: %v", err)
	}
	// The freshly generated key is persisted owner-only BEFORE the caller
	// spends the enrollment round trip, so a crash can never orphan a
	// certificate whose key is gone.
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %o, want 600", info.Mode().Perm())
	}

	second, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrGenerateKey again: %v", err)
	}
	if !first.Equal(second) {
		t.Error("the persisted key must be reused, not regenerated")
	}

	// A pre-existing PEM file loads as-is (the renewal path re-signs CSRs
	// with the SAME key the certificate carries).
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrGenerateKey pre-existing: %v", err)
	}
	if !loaded.Equal(other) {
		t.Error("the pre-existing key must load unchanged")
	}
}

func TestLoadOrGenerateKeyRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, keyFileName)
	if err := os.WriteFile(keyPath, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrGenerateKey(keyPath); err == nil {
		t.Fatal("LoadOrGenerateKey on a garbage key file = nil error, want an error")
	}
}
