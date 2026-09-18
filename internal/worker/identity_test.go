package worker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/ca"
)

func TestIdentityRenewalWindowAndReplace(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1", true)
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	leaf := id.Leaf()
	if id.NeedsRenewal(leaf.NotBefore.Add(time.Hour)) {
		t.Fatal("a fresh certificate needs no renewal")
	}
	if !id.NeedsRenewal(leaf.NotAfter.Add(-ca.LeafValidity / 4)) {
		t.Fatal("less than a third left must renew")
	}
	// A renewed certificate for the same key replaces the file atomically.
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, id.Signer())
	if err != nil {
		t.Fatal(err)
	}
	renewed, _, err := h.ca.SignCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), orgID, zoneID, "fra-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Replace(renewed); err != nil {
		t.Fatal(err)
	}
	onDisk, _ := os.ReadFile(cfg.CertPath)
	if string(onDisk) != string(renewed) || id.Leaf().SerialNumber.Cmp(leaf.SerialNumber) == 0 {
		t.Fatal("the renewed certificate must be on disk and in use")
	}
	entries, _ := os.ReadDir(filepath.Dir(cfg.CertPath))
	for _, e := range entries {
		if e.Name()[0] == '.' {
			t.Fatalf("temp file %s left behind", e.Name())
		}
	}
	// A certificate for another key is refused and changes nothing.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherCSR, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, other)
	foreign, _, _ := h.ca.SignCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: otherCSR}), orgID, zoneID, "fra-1")
	if err := id.Replace(foreign); err == nil {
		t.Fatal("a certificate for another key was accepted")
	}
	if err := id.Replace([]byte("garbage")); err == nil {
		t.Fatal("garbage was accepted")
	}
}
