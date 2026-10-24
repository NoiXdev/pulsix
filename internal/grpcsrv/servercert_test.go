package grpcsrv

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"strings"
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
	src, err := newServerCertSource(h.ca, func() time.Time { return now }, nil)
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

// TestAFailedReissueBacksOffInsteadOfRetryingEveryHandshake is backlog
// 2.64. Once the leaf entered its last third, a failing re-issue was
// retried on EVERY handshake — each attempt a CA issue, which is a
// database write, and each failure a log line. So the moment the
// database is unwell, a fleet reconnecting turns a slow database into
// a hammered one and buries the reason under one identical line per
// connection. The certificate on hand is still valid for a third of
// its life; there is room to wait.
func TestAFailedReissueBacksOffInsteadOfRetryingEveryHandshake(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	var logs bytes.Buffer
	src, err := newServerCertSource(h.ca, func() time.Time { return now }, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	served, _ := src.get(nil)

	attempts := 0
	src.issue = func() error {
		attempts++
		return errors.New("the database is not answering")
	}
	now = now.Add(ca.LeafValidity - ca.LeafValidity/4) // less than a third left

	for range 50 {
		got, err := src.get(nil)
		if err != nil {
			t.Fatalf("get must keep serving the valid certificate: %v", err)
		}
		if got != served {
			t.Fatal("a failed re-issue must leave the served certificate alone")
		}
	}
	if attempts != 1 {
		t.Fatalf("50 handshakes made %d issue attempts, want 1 — each one is a database write", attempts)
	}
	if n := strings.Count(logs.String(), "re-issue gRPC server certificate"); n != 1 {
		t.Fatalf("logged %d times, want 1: a line per connection buries the reason", n)
	}

	// Once the window has passed it tries again...
	now = now.Add(certRetryAfter + time.Second)
	if _, _ = src.get(nil); attempts != 2 {
		t.Fatalf("attempts after the retry window = %d, want 2", attempts)
	}
	// ...and a success clears the backoff, so the next due check is
	// not held off by a failure that is over.
	src.issue = func() error { attempts++; return nil }
	now = now.Add(certRetryAfter + time.Second)
	if _, _ = src.get(nil); attempts != 3 {
		t.Fatalf("attempts after a success = %d, want 3", attempts)
	}
	if !src.retryAfter.IsZero() {
		t.Fatal("a successful re-issue must clear the backoff")
	}
}
