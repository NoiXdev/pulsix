package grpcsrv

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/enrolltoken"
)

func parseCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	return x509.ParseCertificate(block.Bytes)
}

func TestServerCertificateCarriesTheCAAndRenews(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	src, err := newServerCertSource(h.ca, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	first, _ := src.get(nil)
	_, root := h.ca.Pool()
	if len(first.Certificate) != 2 || string(first.Certificate[1]) != string(root.Raw) {
		t.Fatal("the served chain must be [leaf, CA] so a worker can pin the CA")
	}
	if same, _ := src.get(nil); same != first {
		t.Fatal("a fresh certificate must not be re-issued")
	}
	now = now.Add(ca.LeafValidity - ca.LeafValidity/4) // less than a third left
	renewed, _ := src.get(nil)
	if renewed == first {
		t.Fatal("the server certificate was not re-issued")
	}
}

// TestServedChainOverTheWire: the TLS handshake itself carries [leaf, CA],
// the CA being byte-for-byte the certificate a token pins.
func TestServedChainOverTheWire(t *testing.T) {
	h := newHarness(t)
	raw, err := h.lis.DialContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pool, root := h.ca.Pool()
	conn := tls.Client(raw, &tls.Config{ServerName: "manager", RootCAs: pool, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}})
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.HandshakeContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	chain := conn.ConnectionState().PeerCertificates
	if len(chain) != 2 || enrolltoken.Pin(chain[1].Raw) != h.ca.CAPin() || string(chain[1].Raw) != string(root.Raw) {
		t.Fatalf("served chain has %d certificates, want [leaf, CA]", len(chain))
	}
}
