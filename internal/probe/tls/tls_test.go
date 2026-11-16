package tls_test

import (
	"context"
	"crypto/tls"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func TestTLSSelfSignedIsUpButNotValid(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	obs := run(context.Background(), probetest.LoopbackGuard(), map[string]any{"port": float64(port), "timeout_s": float64(5)}, "127.0.0.1")
	m := probetest.WantUp(t, obs, "tls")
	if len(m) != 6 {
		t.Fatalf("want up, failure, handshake_ms, cert_valid, cert_expiry_days and cert_issuer, got %+v", obs)
	}
	// httptest's certificate is issued by an organization without a
	// common name.
	if got := probetest.WantLabel(t, m, "tls.cert_issuer"); got != "Acme Co" {
		t.Fatalf("tls.cert_issuer = %q, want the organization", got)
	}
	if v := probetest.WantValue(t, m, "tls.handshake_ms"); v < 0 {
		t.Fatalf("tls.handshake_ms = %v, want >= 0", v)
	}
	if v := probetest.WantValue(t, m, "tls.cert_valid"); v != 0 {
		t.Fatalf("tls.cert_valid = %v, want 0 for a self-signed certificate", v)
	}
	want := time.Until(srv.Certificate().NotAfter).Hours() / 24
	if v := probetest.WantValue(t, m, "tls.cert_expiry_days"); math.Abs(v-want) > 1 {
		t.Fatalf("tls.cert_expiry_days = %v, want %v ± 1", v, want)
	}
}

func TestTLSConnectionRefusedIsDown(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(), map[string]any{"port": float64(probetest.ClosedPort(t)), "timeout_s": float64(2)}, "127.0.0.1")
	probetest.WantDown(t, obs, "tls", probe.FailureRefused)
}

func TestTLSHandshakeFailureIsDown(t *testing.T) {
	// A plain TCP listener that never speaks TLS.
	port, _ := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "SSH-2.0-test\r\n")
	obs := run(context.Background(), probetest.LoopbackGuard(), map[string]any{"port": float64(port), "timeout_s": float64(2)}, "127.0.0.1")
	probetest.WantDown(t, obs, "tls", probe.FailureTLS)
}

// TestTLSAlertIsDown: a server that refuses the handshake with an alert.
func TestTLSAlertIsDown(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return nil, errors.New("no configuration for this client")
	}}
	srv.StartTLS()
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	obs := run(context.Background(), probetest.LoopbackGuard(), map[string]any{"port": float64(port), "timeout_s": float64(2)}, "127.0.0.1")
	probetest.WantDown(t, obs, "tls", probe.FailureTLS)
}

// TestTLSClosedDuringHandshakeIsDown: a server that closes the
// connection instead of answering the hello reset it.
func TestTLSClosedDuringHandshakeIsDown(t *testing.T) {
	port := listen(t, func(c net.Conn) { _ = c.Close() })
	obs := run(context.Background(), probetest.LoopbackGuard(), map[string]any{"port": float64(port), "timeout_s": float64(2)}, "127.0.0.1")
	probetest.WantDown(t, obs, "tls", probe.FailureReset)
}

// TestTLSSilentServerTimesOut: a server that never answers the hello.
func TestTLSSilentServerTimesOut(t *testing.T) {
	port := listen(t, func(c net.Conn) {
		time.Sleep(3 * time.Second)
		_ = c.Close()
	})
	obs := run(context.Background(), probetest.LoopbackGuard(), map[string]any{"port": float64(port), "timeout_s": float64(1)}, "127.0.0.1")
	probetest.WantDown(t, obs, "tls", probe.FailureTimeout)
}

// listen accepts connections on loopback and hands each to serve.
func listen(t *testing.T, serve func(net.Conn)) int {
	t.Helper()
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
			go serve(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestTLSRefusedTargetIsAnError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	obs := run(context.Background(), probe.NewGuard(nil, nil), map[string]any{"port": float64(port)}, "127.0.0.1")
	probetest.WantErrors(t, obs, "target address not allowed", "tls.up", "tls.failure", "tls.handshake_ms", "tls.cert_valid", "tls.cert_expiry_days", "tls.cert_issuer")
}

func TestTLSEmptyAddressIsAnError(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(), nil, "")
	probetest.WantErrors(t, obs, "host has no address", "tls.up", "tls.failure", "tls.handshake_ms", "tls.cert_valid", "tls.cert_expiry_days", "tls.cert_issuer")
}
