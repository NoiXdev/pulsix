package probetest_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func TestLoopbackGuard(t *testing.T) {
	g := probetest.LoopbackGuard()
	if !g.Allowed(netip.MustParseAddr("127.0.0.1")) || !g.Allowed(netip.MustParseAddr("::1")) {
		t.Fatal("the loopback guard refuses loopback")
	}
	if g.Allowed(netip.MustParseAddr("169.254.169.254")) {
		t.Fatal("the loopback guard allows the metadata address")
	}
}

func TestObservationAssertions(t *testing.T) {
	obs := []probe.Observation{
		probe.Value("x.up", 1),
		probe.Value("x.time_ms", 2.5),
		{Metric: "x.code", Err: "boom"},
	}
	m := probetest.ByMetric(t, obs)
	if len(m) != 3 || m["x.code"].Err != "boom" {
		t.Fatalf("ByMetric = %+v", m)
	}
	if got := probetest.WantValue(t, m, "x.time_ms"); got != 2.5 {
		t.Fatalf("WantValue = %v, want 2.5", got)
	}
	probetest.WantOnly(t, []probe.Observation{probe.Value("x.up", 0)}, "x.up", 0)
	probetest.WantDown(t, probe.Down("x", probe.FailureTimeout), "x", probe.FailureTimeout)
	up := probetest.WantUp(t, append(probe.Up("x"), probe.Text("x.issuer", "R11")), "x")
	if got := probetest.WantLabel(t, up, "x.issuer"); got != "R11" {
		t.Fatalf("WantLabel = %q", got)
	}
	probetest.WantErrors(t, probe.FailAll("boom", "x.up", "x.time_ms"), "boom", "x.up", "x.time_ms")
	probetest.WantErrors(t, nil, "boom")
}

func TestClosedPort(t *testing.T) {
	port := probetest.ClosedPort(t)
	if port < 1 || port > 65535 {
		t.Fatalf("ClosedPort = %d", port)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err == nil {
		_ = conn.Close()
		t.Skipf("something took port %d after it was released", port)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("dial the closed port: %v, want connection refused", err)
	}
}

// TestCAIssuesATrustedLeaf: a leaf from the CA, served by TLSServer,
// verifies against the CA's pool for both the name and the address it
// carries.
func TestCAIssuesATrustedLeaf(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	leaf := ca.Leaf(t, now.Add(-time.Minute), now.Add(time.Hour))
	srv, port := probetest.TLSServer(t, leaf)
	if port < 1 || srv.URL == "" {
		t.Fatalf("TLSServer = %s, %v", srv.URL, port)
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	for _, name := range []string{"probe.test", "127.0.0.1"} {
		conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: ca.Pool, ServerName: name, MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatalf("handshake as %s: %v", name, err)
		}
		certs := conn.ConnectionState().PeerCertificates
		_ = conn.Close()
		if len(certs) != 1 || !certs[0].NotAfter.Equal(now.Add(time.Hour).Truncate(time.Second)) {
			t.Fatalf("peer certificates %v, want the one leaf", certs)
		}
	}

	if _, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "probe.test", MinVersion: tls.VersionTLS12}); err == nil {
		t.Fatal("the leaf verified without its CA")
	}
}

func TestTCPServer(t *testing.T) {
	port, received := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "220 hello\r\n")
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	banner, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || banner != "220 hello\r\n" {
		t.Fatalf("banner %q, %v", banner, err)
	}
	if _, err := conn.Write([]byte("QUIT\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-received:
		if line != "QUIT\r\n" {
			t.Fatalf("received %q, want QUIT", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server delivered no line")
	}
}

// TestTCPServerWithoutBanner: a server without a banner writes nothing and
// still delivers what the client sends.
func TestTCPServerWithoutBanner(t *testing.T) {
	port, received := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "")
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-received:
		if line != "ping\n" {
			t.Fatalf("received %q, want ping", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server delivered no line")
	}
}

// TestTCPServerSkipsWhenItCannotListen: an address the test machine cannot
// listen on skips the calling test instead of failing it.
func TestTCPServerSkipsWhenItCannotListen(t *testing.T) {
	skipped := false
	t.Run("unlistenable", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		probetest.TCPServer(t, "tcp", "192.0.2.1:0", "")
	})
	if !skipped {
		t.Fatal("TCPServer on an address not on this machine did not skip")
	}
}
