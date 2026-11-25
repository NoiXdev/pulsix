package probe_test

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// TestFailureVocabulary pins the labels of the spec's table: rules and
// the SPA's texts name them, so one changes only with both.
func TestFailureVocabulary(t *testing.T) {
	want := []string{"none", "dns", "refused", "timeout", "unreachable", "reset", "tls", "certificate", "protocol", "redirects", "nxdomain", "servfail", "auth", "auth_or_lost", "error"}
	if !slices.Equal(probe.FailureLabels(), want) {
		t.Fatalf("FailureLabels = %v, want %v", probe.FailureLabels(), want)
	}
	got := []string{
		probe.FailureNone, probe.FailureDNS, probe.FailureRefused, probe.FailureTimeout, probe.FailureUnreachable,
		probe.FailureReset, probe.FailureTLS, probe.FailureCertificate, probe.FailureProtocol, probe.FailureRedirects,
		probe.FailureNXDomain, probe.FailureServFail, probe.FailureAuth, probe.FailureAuthOrLost, probe.FailureError,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("constants = %v, want %v", got, want)
	}
}

// TestClassifyRealDials: the errors a real dial returns are classified
// the same on every platform the probes run on.
func TestClassifyRealDials(t *testing.T) {
	d := probetest.LoopbackGuard().Dialer(0)
	_, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(probetest.ClosedPort(t))))
	if got := probe.Classify(err); got != probe.FailureRefused {
		t.Fatalf("closed port: Classify(%v) = %q, want refused", err, got)
	}

	// A listener that resets every connection: the read fails with a reset.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.(*net.TCPConn).SetLinger(0)
			_ = c.Close()
		}
	}()
	// Under load the reset can arrive before the dial returns (seen on
	// macOS); that error is a reset too.
	conn, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err == nil {
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = conn.Read(make([]byte, 1))
	}
	if got := probe.Classify(err); got != probe.FailureReset {
		t.Fatalf("reset: Classify(%v) = %q, want reset", err, got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	_, err = d.DialContext(ctx, "tcp", ln.Addr().String())
	if got := probe.Classify(err); got != probe.FailureTimeout {
		t.Fatalf("expired: Classify(%v) = %q, want timeout", err, got)
	}
}

func TestIssuerLabel(t *testing.T) {
	cert := func(n pkix.Name) *x509.Certificate { return &x509.Certificate{Issuer: n} }
	long := strings.Repeat("ä", 100) // 200 bytes
	for _, tc := range []struct {
		name string
		cert *x509.Certificate
		want string
	}{
		{"common name", cert(pkix.Name{CommonName: "R11", Organization: []string{"Let's Encrypt"}}), "R11"},
		{"organization when no common name", cert(pkix.Name{Organization: []string{"Example Org", "Second"}}), "Example Org"},
		{"blank common name", cert(pkix.Name{CommonName: " \x00 ", Organization: []string{"Example Org"}}), "Example Org"},
		{"distinguished name otherwise", cert(pkix.Name{Country: []string{"DE"}, Locality: []string{"Berlin"}}), "L=Berlin,C=DE"},
		{"cleaned", cert(pkix.Name{CommonName: "Evil\x1b[31m\tCA\n"}), "Evil[31m CA"},
		{"nothing at all", cert(pkix.Name{}), probe.EmptyText},
		{"nil", nil, probe.EmptyText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := probe.IssuerLabel(tc.cert); got != tc.want {
				t.Fatalf("IssuerLabel = %q, want %q", got, tc.want)
			}
		})
	}
	got := probe.IssuerLabel(cert(pkix.Name{CommonName: long}))
	if len(got) > probe.ShortLabelLen || len(got) < probe.ShortLabelLen-1 || !utf8.ValidString(got) {
		t.Fatalf("a 200-byte issuer: %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

func TestShortLabel(t *testing.T) {
	if probe.ShortLabelLen != 128 {
		t.Fatalf("ShortLabelLen = %d, want 128", probe.ShortLabelLen)
	}
	if got := probe.ShortLabel(strings.Repeat("x", 300)); got != strings.Repeat("x", 128) {
		t.Fatalf("ShortLabel cut to %d bytes", len(got))
	}
	if got := probe.ShortLabel("a\x00"); got != "a" {
		t.Fatalf("ShortLabel = %q, want cleaned", got)
	}
	if got := probe.ShortLabel(" "); got != probe.EmptyText {
		t.Fatalf("ShortLabel of blank = %q", got)
	}
}
