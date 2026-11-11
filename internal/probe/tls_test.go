package probe_test

import (
	"context"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

func TestTLSSelfSignedIsUpButNotValid(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	obs := probe.Run(context.Background(), loopbackGuard(), "tls", map[string]any{"port": float64(port), "timeout_s": float64(5)}, "127.0.0.1")
	m := byMetric(t, obs)
	if len(m) != 4 {
		t.Fatalf("want up, handshake_ms, cert_valid and cert_expiry_days, got %+v", obs)
	}
	if v := wantValue(t, m, "tls.up"); v != 1 {
		t.Fatalf("tls.up = %v, want 1", v)
	}
	if v := wantValue(t, m, "tls.handshake_ms"); v < 0 {
		t.Fatalf("tls.handshake_ms = %v, want >= 0", v)
	}
	if v := wantValue(t, m, "tls.cert_valid"); v != 0 {
		t.Fatalf("tls.cert_valid = %v, want 0 for a self-signed certificate", v)
	}
	want := time.Until(srv.Certificate().NotAfter).Hours() / 24
	if v := wantValue(t, m, "tls.cert_expiry_days"); math.Abs(v-want) > 1 {
		t.Fatalf("tls.cert_expiry_days = %v, want %v ± 1", v, want)
	}
}

func TestTLSConnectionRefusedIsDown(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "tls", map[string]any{"port": float64(closedPort(t)), "timeout_s": float64(2)}, "127.0.0.1")
	wantOnly(t, obs, "tls.up", 0)
}

func TestTLSHandshakeFailureIsDown(t *testing.T) {
	// A plain TCP listener that never speaks TLS.
	port, _ := tcpServer(t, "tcp", "127.0.0.1:0", "SSH-2.0-test\r\n")
	obs := probe.Run(context.Background(), loopbackGuard(), "tls", map[string]any{"port": float64(port), "timeout_s": float64(2)}, "127.0.0.1")
	wantOnly(t, obs, "tls.up", 0)
}

func TestTLSRefusedTargetIsAnError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	obs := probe.Run(context.Background(), probe.NewGuard(nil), "tls", map[string]any{"port": float64(port)}, "127.0.0.1")
	wantErrors(t, obs, "target address not allowed", "tls.up", "tls.handshake_ms", "tls.cert_valid", "tls.cert_expiry_days")
}

func TestTLSEmptyAddressIsAnError(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "tls", nil, "")
	wantErrors(t, obs, "host has no address", "tls.up", "tls.handshake_ms", "tls.cert_valid", "tls.cert_expiry_days")
}
