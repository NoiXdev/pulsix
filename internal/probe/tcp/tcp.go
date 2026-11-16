package tcp

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

const (
	tcpDefaultTimeout = 5 * time.Second
	// tcpBannerLimit is how much of the target's first output expect is
	// looked for in.
	tcpBannerLimit = 4 << 10
)

// probeTCP connects to address:port, optionally writes send and looks for
// expect in the first 4 KiB read within the timeout.
func probeTCP(ctx context.Context, g *probe.Guard, params map[string]any, address string) []probe.Observation {
	send := probe.StrParam(params, "send", "")
	expect := probe.StrParam(params, "expect", "")
	metrics := []string{"tcp.up", "tcp.failure", "tcp.connect_ms"}
	if expect != "" {
		metrics = append(metrics, "tcp.banner_match")
	}

	host, err := probe.TargetHost(address)
	if err != nil {
		return probe.FailAll(err.Error(), metrics...)
	}
	port, ok := probe.PortParam(params, 0)
	if !ok {
		return probe.FailAll(probe.ErrBadPort.Error(), metrics...)
	}

	ctx, cancel := probe.Bound(ctx, params, tcpDefaultTimeout)
	defer cancel()

	start := time.Now()
	// ctx carries the timeout, so the dialer needs none of its own.
	conn, err := g.Dialer(0).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, probe.ErrNotAllowed) {
			return probe.FailAll(probe.ErrNotAllowed.Error(), metrics...)
		}
		return probe.Down("tcp", probe.Classify(err))
	}
	connect := time.Since(start)
	defer func() { _ = conn.Close() }()

	// A banner that does not match is no failure: the port was up.
	obs := []probe.Observation{probe.Value("tcp.up", 1), probe.Up("tcp"), probe.Value("tcp.connect_ms", probe.Millis(connect))}
	if send == "" && expect == "" {
		return obs
	}

	// The deadline is set before the cancel hook, so a cancel that has
	// already happened is not overwritten by it.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	if send != "" {
		// A failed write still leaves the port up; banner_match says the rest.
		_, _ = conn.Write([]byte(send))
	}
	if expect != "" {
		obs = append(obs, probe.BoolValue("tcp.banner_match", readContains(conn, expect, tcpBannerLimit)))
	}
	return obs
}

// readContains reads from conn until want appears, limit bytes were read, or
// the read fails (EOF, deadline). Nothing read leaves the probe.
func readContains(conn net.Conn, want string, limit int) bool {
	buf := make([]byte, 0, limit)
	for len(buf) < limit {
		n, err := conn.Read(buf[len(buf):limit])
		buf = buf[:len(buf)+n]
		if strings.Contains(string(buf), want) {
			return true
		}
		if err != nil {
			return false
		}
	}
	return false
}

// Probe is the tcp probe, as the registry runs it.
func Probe(ctx context.Context, g *probe.Guard, params map[string]any, address string, _ map[string]string) []probe.Observation {
	return probeTCP(ctx, g, params, address)
}
