package tcp_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/judge"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// serve listens on loopback and hands every connection to handle, which
// owns it.
func serve(t *testing.T, handle func(net.Conn)) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// writeAndHold writes banner and keeps the connection open for hold.
func writeAndHold(banner string, hold time.Duration) func(net.Conn) {
	return func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte(banner))
		time.Sleep(hold)
	}
}

func assertions(entries ...string) []any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = e
	}
	return out
}

// byItem indexes the assertion observations: item → metric → observation.
func byItem(obs []probe.Observation) map[string]map[string]probe.Observation {
	out := map[string]map[string]probe.Observation{}
	for _, o := range obs {
		if o.Item == "" {
			continue
		}
		if out[o.Item] == nil {
			out[o.Item] = map[string]probe.Observation{}
		}
		out[o.Item][o.Metric] = o
	}
	return out
}

// wantVerdict fails unless item reports result and actual.
func wantVerdict(t *testing.T, items map[string]map[string]probe.Observation, item, result, actual string) {
	t.Helper()
	got := items[item]
	r, a := got["tcp.assertion"], got["tcp.assertion_actual"]
	if r.Label == nil || a.Label == nil || *r.Label != result || *a.Label != actual {
		t.Errorf("%s = %+v / %+v, want %s seeing %s", item, r, a, result, actual)
	}
}

// TestTCPAssertions (backlog 3.87): connect_ms, response and response_ms
// judged on the first line the target sent; the probe stops reading at
// the line break although the server keeps the connection open.
func TestTCPAssertions(t *testing.T) {
	port := serve(t, writeAndHold("SSH-2.0-OpenSSH_9.6\r\nsecond line\r\n", 5*time.Second))
	params := tcpParams(port, map[string]any{"timeout_s": float64(4), "assertions": assertions(
		"c=connect_ms < 5000",
		`r=response matches "^SSH-2\\.0"`,
		`e=response == "SSH-2.0-OpenSSH_9.6"`,
		`w=response contains "second" @warn`,
		"t=response_ms < 5000",
		"x=connect_ms > 5000",
	)})
	start := time.Now()
	obs := run(context.Background(), probetest.LoopbackGuard(), params, "127.0.0.1")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("probe took %v: it must stop at the first line break, not wait for the timeout", d)
	}
	items := byItem(obs)
	if len(items) != 6 {
		t.Fatalf("assertion items = %v", items)
	}
	wantVerdict(t, items, "r", "pass", `"SSH-2.0-OpenSSH_9.6"`)
	wantVerdict(t, items, "e", "pass", `"SSH-2.0-OpenSSH_9.6"`)
	wantVerdict(t, items, "w", "warn", `"SSH-2.0-OpenSSH_9.6"`)
	if *items["c"]["tcp.assertion"].Label != "pass" || *items["t"]["tcp.assertion"].Label != "pass" {
		t.Errorf("c, t = %+v, %+v", items["c"], items["t"])
	}
	if *items["x"]["tcp.assertion"].Label != "crit" {
		t.Errorf("x = %+v, want crit", items["x"])
	}
}

// TestTCPResponseAssertions: expect still looks past the first line when
// a response subject asks for it; a reply without a line break before the
// close is the whole reply; send goes out first and is never an actual.
func TestTCPResponseAssertions(t *testing.T) {
	t.Run("expect on line two", func(t *testing.T) {
		port := serve(t, writeAndHold("220 mail\r\n250 ready\r\n", 5*time.Second))
		obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(port, map[string]any{
			"timeout_s": float64(4), "expect": "250 ready", "assertions": assertions(`r=response == "220 mail"`),
		}), "127.0.0.1")
		m := probetest.WantUp(t, obs[:4], "tcp")
		if v := probetest.WantValue(t, m, "tcp.banner_match"); v != 1 {
			t.Fatalf("tcp.banner_match = %v, want 1", v)
		}
		wantVerdict(t, byItem(obs), "r", "pass", `"220 mail"`)
	})
	t.Run("reply without a line break", func(t *testing.T) {
		port := serve(t, func(conn net.Conn) {
			buf := make([]byte, 64)
			_, _ = conn.Read(buf)
			_, _ = conn.Write([]byte("+PONG"))
			_ = conn.Close()
		})
		obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(port, map[string]any{
			"send": "PING secret-token\r\n", "assertions": assertions(`r=response == "+PONG"`, "t=response_ms < 5000"),
		}), "127.0.0.1")
		items := byItem(obs)
		wantVerdict(t, items, "r", "pass", `"+PONG"`)
		if *items["t"]["tcp.assertion"].Label != "pass" {
			t.Errorf("t = %+v", items["t"])
		}
	})
}

// TestTCPResponseMsAbsent: nothing arrives before the timeout, so a
// response_ms limit fails seeing absent (backlog 3.87, R4), while a
// response text keeps 3.13b's absent rule.
func TestTCPResponseMsAbsent(t *testing.T) {
	port := serve(t, writeAndHold("", 3*time.Second))
	obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(port, map[string]any{
		"timeout_s": float64(1), "assertions": assertions("t=response_ms < 5000", `n=response != "x"`, `e=response contains "x"`, "c=connect_ms < 5000"),
	}), "127.0.0.1")
	probetest.WantUp(t, obs[:3], "tcp")
	items := byItem(obs)
	wantVerdict(t, items, "t", "crit", judge.ActualAbsent)
	wantVerdict(t, items, "n", "pass", judge.ActualAbsent)
	wantVerdict(t, items, "e", "crit", judge.ActualAbsent)
	if *items["c"]["tcp.assertion"].Label != "pass" {
		t.Errorf("c = %+v", items["c"])
	}
}

// TestTCPAssertionsOnFailures: a down attempt reports no assertion item;
// one that could not be made puts its error on both metrics of each; an
// entry that does not parse in tcp's dialect is an error on its item.
func TestTCPAssertionsOnFailures(t *testing.T) {
	list := assertions("c=connect_ms < 5000", "s=status == 200")
	obs := run(context.Background(), probetest.LoopbackGuard(), tcpParams(probetest.ClosedPort(t), map[string]any{"assertions": list}), "127.0.0.1")
	probetest.WantDown(t, obs, "tcp", probe.FailureRefused)
	if items := byItem(obs); len(items) != 0 {
		t.Errorf("down attempt reported %v", items)
	}

	port, _ := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "")
	obs = run(context.Background(), probe.NewGuard(nil, nil), tcpParams(port, map[string]any{"assertions": list}), "127.0.0.1")
	items := byItem(obs)
	for _, name := range []string{"c", "s"} {
		for _, metric := range []string{"tcp.assertion", "tcp.assertion_actual"} {
			if items[name][metric].Err != "target address not allowed" {
				t.Errorf("%s %s = %+v, want the guard's error", name, metric, items[name][metric])
			}
		}
	}

	obs = run(context.Background(), probetest.LoopbackGuard(), tcpParams(port, map[string]any{"assertions": list}), "127.0.0.1")
	items = byItem(obs)
	if items["s"]["tcp.assertion"].Err != judge.ErrInvalid || *items["c"]["tcp.assertion"].Label != "pass" {
		t.Errorf("items = %+v", items)
	}
}
