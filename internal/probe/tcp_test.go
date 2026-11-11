package probe_test

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

// tcpServer listens on network/addr, writes banner to every connection and
// delivers the first line each client sends on the returned channel.
func tcpServer(t *testing.T, network, addr, banner string) (port int, received <-chan string) {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Skipf("listen %s %s: %v", network, addr, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan string, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				if banner != "" {
					_, _ = conn.Write([]byte(banner))
				}
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err == nil {
					got <- line
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, got
}

// closedPort returns a loopback port that nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func tcpParams(port int, extra map[string]any) map[string]any {
	p := map[string]any{"port": float64(port), "timeout_s": float64(2)}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func TestTCPBannerMatch(t *testing.T) {
	port, _ := tcpServer(t, "tcp", "127.0.0.1:0", "SSH-2.0-test\r\n")
	obs := probe.Run(context.Background(), loopbackGuard(), "tcp", tcpParams(port, map[string]any{"expect": "SSH-2.0"}), "127.0.0.1")
	m := byMetric(t, obs)
	if len(m) != 3 {
		t.Fatalf("want up, connect_ms and banner_match, got %+v", obs)
	}
	if v := wantValue(t, m, "tcp.up"); v != 1 {
		t.Fatalf("tcp.up = %v, want 1", v)
	}
	if v := wantValue(t, m, "tcp.connect_ms"); v < 0 {
		t.Fatalf("tcp.connect_ms = %v, want >= 0", v)
	}
	if v := wantValue(t, m, "tcp.banner_match"); v != 1 {
		t.Fatalf("tcp.banner_match = %v, want 1", v)
	}
}

func TestTCPBannerMismatch(t *testing.T) {
	port, _ := tcpServer(t, "tcp", "127.0.0.1:0", "SSH-2.0-test\r\n")
	start := time.Now()
	obs := probe.Run(context.Background(), loopbackGuard(), "tcp", tcpParams(port, map[string]any{"expect": "nope", "timeout_s": float64(1)}), "127.0.0.1")
	m := byMetric(t, obs)
	if v := wantValue(t, m, "tcp.up"); v != 1 {
		t.Fatalf("tcp.up = %v, want 1", v)
	}
	if v := wantValue(t, m, "tcp.banner_match"); v != 0 {
		t.Fatalf("tcp.banner_match = %v, want 0", v)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("probe took %v, beyond its 1 s timeout", d)
	}
}

func TestTCPWithoutExpectReportsNoBannerMatch(t *testing.T) {
	port, _ := tcpServer(t, "tcp", "127.0.0.1:0", "")
	obs := probe.Run(context.Background(), loopbackGuard(), "tcp", tcpParams(port, nil), "127.0.0.1")
	m := byMetric(t, obs)
	if len(m) != 2 {
		t.Fatalf("want up and connect_ms only, got %+v", obs)
	}
	if v := wantValue(t, m, "tcp.up"); v != 1 {
		t.Fatalf("tcp.up = %v, want 1", v)
	}
}

func TestTCPClosedPortIsDown(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "tcp", tcpParams(closedPort(t), map[string]any{"expect": "x"}), "127.0.0.1")
	wantOnly(t, obs, "tcp.up", 0)
}

func TestTCPUnresolvableHostIsDown(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "tcp", tcpParams(22, nil), "no-such-host.invalid")
	wantOnly(t, obs, "tcp.up", 0)
}

func TestTCPSendReachesListener(t *testing.T) {
	port, received := tcpServer(t, "tcp", "127.0.0.1:0", "")
	obs := probe.Run(context.Background(), loopbackGuard(), "tcp", tcpParams(port, map[string]any{"send": "PING\r\n"}), "127.0.0.1")
	if v := wantValue(t, byMetric(t, obs), "tcp.up"); v != 1 {
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
	port, _ := tcpServer(t, "tcp", "127.0.0.1:0", "SSH-2.0-test\r\n")
	obs := probe.Run(context.Background(), probe.NewGuard(nil), "tcp", tcpParams(port, map[string]any{"expect": "SSH-2.0"}), "127.0.0.1")
	wantErrors(t, obs, "target address not allowed", "tcp.up", "tcp.connect_ms", "tcp.banner_match")
}

func TestTCPRefusedHostnameIsAnError(t *testing.T) {
	// localhost resolves to loopback only, so every dial is refused.
	port, _ := tcpServer(t, "tcp", "127.0.0.1:0", "")
	obs := probe.Run(context.Background(), probe.NewGuard(nil), "tcp", tcpParams(port, nil), "localhost")
	wantErrors(t, obs, "target address not allowed", "tcp.up", "tcp.connect_ms")
}

func TestTCPEmptyAddressIsAnError(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "tcp", tcpParams(22, map[string]any{"expect": "x"}), "")
	wantErrors(t, obs, "host has no address", "tcp.up", "tcp.connect_ms", "tcp.banner_match")
}

func TestTCPUnparsableAddressIsAnError(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "tcp", tcpParams(22, nil), "host:22")
	m := byMetric(t, obs)
	if len(m) != 2 || m["tcp.up"].Err == "" || m["tcp.connect_ms"].Err == "" {
		t.Fatalf("want one error per metric, got %+v", obs)
	}
}

func TestTCPIPv6(t *testing.T) {
	port, _ := tcpServer(t, "tcp6", "[::1]:0", "SSH-2.0-test\r\n")
	for _, addr := range []string{"::1", "[::1]"} {
		t.Run(addr, func(t *testing.T) {
			obs := probe.Run(context.Background(), loopbackGuard(), "tcp", tcpParams(port, map[string]any{"expect": "SSH-2.0"}), addr)
			m := byMetric(t, obs)
			if v := wantValue(t, m, "tcp.up"); v != 1 {
				t.Fatalf("tcp.up = %v, want 1", v)
			}
			if v := wantValue(t, m, "tcp.banner_match"); v != 1 {
				t.Fatalf("tcp.banner_match = %v, want 1", v)
			}
		})
	}
}

func TestTCPHonoursContextDeadline(t *testing.T) {
	// The listener never writes, so the banner read waits for the deadline:
	// the context's 300 ms, not the params' 30 s.
	port, _ := tcpServer(t, "tcp", "127.0.0.1:0", "")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	obs := probe.Run(ctx, loopbackGuard(), "tcp", tcpParams(port, map[string]any{"expect": "x", "timeout_s": float64(30)}), "127.0.0.1")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("probe took %v, beyond the context deadline", d)
	}
	if v := wantValue(t, byMetric(t, obs), "tcp.banner_match"); v != 0 {
		t.Fatalf("tcp.banner_match = %v, want 0", v)
	}
}

func TestRunUnknownPluginIsAnError(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "snmp", nil, "127.0.0.1")
	m := byMetric(t, obs)
	if len(m) != 1 || m["snmp.up"].Err == "" {
		t.Fatalf("want one error on snmp.up, got %+v", obs)
	}
}
