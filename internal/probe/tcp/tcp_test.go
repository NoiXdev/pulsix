package tcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func tcpParams(port int, extra map[string]any) map[string]any {
	p := map[string]any{"port": float64(port), "timeout_s": float64(2)}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func TestTCPBannerMatch(t *testing.T) {
	port, _ := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "SSH-2.0-test\r\n")
	obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(port, map[string]any{"expect": "SSH-2.0"}), "127.0.0.1")
	m := probetest.WantUp(t, obs, "tcp")
	if len(m) != 4 {
		t.Fatalf("want up, failure, connect_ms and banner_match, got %+v", obs)
	}
	if v := probetest.WantValue(t, m, "tcp.connect_ms"); v < 0 {
		t.Fatalf("tcp.connect_ms = %v, want >= 0", v)
	}
	if v := probetest.WantValue(t, m, "tcp.banner_match"); v != 1 {
		t.Fatalf("tcp.banner_match = %v, want 1", v)
	}
}

func TestTCPBannerMismatch(t *testing.T) {
	port, _ := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "SSH-2.0-test\r\n")
	start := time.Now()
	obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(port, map[string]any{"expect": "nope", "timeout_s": float64(1)}), "127.0.0.1")
	// A banner that does not match is no failure: the port was up.
	m := probetest.WantUp(t, obs, "tcp")
	if v := probetest.WantValue(t, m, "tcp.banner_match"); v != 0 {
		t.Fatalf("tcp.banner_match = %v, want 0", v)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("probe took %v, beyond its 1 s timeout", d)
	}
}

func TestTCPWithoutExpectReportsNoBannerMatch(t *testing.T) {
	port, _ := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "")
	obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(port, nil), "127.0.0.1")
	m := probetest.WantUp(t, obs, "tcp")
	if len(m) != 3 {
		t.Fatalf("want up, failure and connect_ms only, got %+v", obs)
	}
}

func TestTCPClosedPortIsDown(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(probetest.ClosedPort(t), map[string]any{"expect": "x"}), "127.0.0.1")
	probetest.WantDown(t, obs, "tcp", probe.FailureRefused)
}

func TestTCPUnresolvableHostIsDown(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(22, nil), "no-such-host.invalid")
	probetest.WantDown(t, obs, "tcp", probe.FailureDNS)
}

// TestTCPConnectTimeoutIsDown: a connect still pending at the deadline
// timed out (here the deadline has passed before the dial starts).
func TestTCPConnectTimeoutIsDown(t *testing.T) {
	port, _ := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	obs := run(ctx, probetest.LoopbackGuard(), tcpParams(port, nil), "127.0.0.1")
	probetest.WantDown(t, obs, "tcp", probe.FailureTimeout)
}

func TestTCPSendReachesListener(t *testing.T) {
	port, received := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "")
	obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(port, map[string]any{"send": "PING\r\n"}), "127.0.0.1")
	if v := probetest.WantValue(t, probetest.ByMetric(t, obs), "tcp.up"); v != 1 {
		t.Fatalf("tcp.up = %v, want 1", v)
	}
	select {
	case line := <-received:
		if line != "PING\r\n" {
			t.Fatalf("listener got %q, want %q", line, "PING\r\n")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listener received nothing")
	}
}

func TestTCPRefusedTargetIsAnError(t *testing.T) {
	port, _ := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "SSH-2.0-test\r\n")
	obs := run(context.Background(), probe.NewGuard(nil, nil), tcpParams(port, map[string]any{"expect": "SSH-2.0"}), "127.0.0.1")
	probetest.WantErrors(t, obs, "target address not allowed", "tcp.up", "tcp.failure", "tcp.connect_ms", "tcp.banner_match")
}

func TestTCPRefusedHostnameIsAnError(t *testing.T) {
	// localhost resolves to loopback only, so every dial is refused.
	port, _ := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "")
	obs := run(context.Background(), probe.NewGuard(nil, nil), tcpParams(port, nil), "localhost")
	probetest.WantErrors(t, obs, "target address not allowed", "tcp.up", "tcp.failure", "tcp.connect_ms")
}

func TestTCPEmptyAddressIsAnError(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(22, map[string]any{"expect": "x"}), "")
	probetest.WantErrors(t, obs, "host has no address", "tcp.up", "tcp.failure", "tcp.connect_ms", "tcp.banner_match")
}

func TestTCPUnparsableAddressIsAnError(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(22, nil), "host:22")
	m := probetest.ByMetric(t, obs)
	if len(m) != 3 || m["tcp.up"].Err == "" || m["tcp.failure"].Err == "" || m["tcp.connect_ms"].Err == "" {
		t.Fatalf("want one error per metric, got %+v", obs)
	}
}

func TestTCPIPv6(t *testing.T) {
	port, _ := probetest.TCPServer(t, "tcp6", "[::1]:0", "SSH-2.0-test\r\n")
	for _, addr := range []string{"::1", "[::1]"} {
		t.Run(addr, func(t *testing.T) {
			obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(port, map[string]any{"expect": "SSH-2.0"}), addr)
			m := probetest.WantUp(t, obs, "tcp")
			if v := probetest.WantValue(t, m, "tcp.banner_match"); v != 1 {
				t.Fatalf("tcp.banner_match = %v, want 1", v)
			}
		})
	}
}

func TestTCPHonoursContextDeadline(t *testing.T) {
	// The listener never writes, so the banner read waits for the deadline:
	// the context's 300 ms, not the params' 30 s.
	port, _ := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	obs := run(ctx, probetest.LoopbackGuard(), tcpParams(port, map[string]any{"expect": "x", "timeout_s": float64(30)}), "127.0.0.1")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("probe took %v, beyond the context deadline", d)
	}
	if v := probetest.WantValue(t, probetest.ByMetric(t, obs), "tcp.banner_match"); v != 0 {
		t.Fatalf("tcp.banner_match = %v, want 0", v)
	}
}
