package probe

import (
	"context"
	"errors"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	tcpDefaultTimeout = 5 * time.Second
	// tcpBannerLimit is how much of the target's first output expect is
	// looked for in.
	tcpBannerLimit = 4 << 10
)

// probeTCP connects to address:port, optionally writes send and looks for
// expect in the first 4 KiB read within the timeout.
func probeTCP(ctx context.Context, g *Guard, params map[string]any, address string) []Observation {
	send := strParam(params, "send", "")
	expect := strParam(params, "expect", "")
	metrics := []string{"tcp.up", "tcp.connect_ms"}
	if expect != "" {
		metrics = append(metrics, "tcp.banner_match")
	}

	host, err := targetHost(address)
	if err != nil {
		return failAll(err.Error(), metrics...)
	}
	port := numParam(params, "port", 0)
	if port < 1 || port > 65535 || port != math.Trunc(port) {
		return failAll("invalid port", metrics...)
	}

	ctx, cancel := bound(ctx, params, tcpDefaultTimeout)
	defer cancel()

	start := time.Now()
	// ctx carries the timeout, so the dialer needs none of its own.
	conn, err := g.Dialer(0).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		if errors.Is(err, ErrNotAllowed) {
			return failAll(ErrNotAllowed.Error(), metrics...)
		}
		return down("tcp")
	}
	connect := time.Since(start)
	defer func() { _ = conn.Close() }()

	obs := []Observation{value("tcp.up", 1), value("tcp.connect_ms", millis(connect))}
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
		obs = append(obs, boolValue("tcp.banner_match", readContains(conn, expect, tcpBannerLimit)))
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
