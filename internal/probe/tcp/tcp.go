package tcp

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
	"github.com/noixdev/pulsix/internal/probe/judge"
)

const (
	tcpDefaultTimeout = 5 * time.Second
	// tcpBannerLimit is how much of the target's first output expect and
	// the response assertions look in.
	tcpBannerLimit = 4 << 10
)

// hostMetrics are the whole-host metrics a probe with an expect of
// expect reports on an up attempt.
func hostMetrics(expect string) []string {
	metrics := []string{"tcp.up", "tcp.failure", "tcp.connect_ms"}
	if expect != "" {
		metrics = append(metrics, "tcp.banner_match")
	}
	return metrics
}

// FailAll is the result of a tcp probe of params that could not be
// attempted: msg on its whole-host metrics and on both metrics of each
// assertion's item.
func FailAll(params map[string]any, msg string) []probe.Observation {
	return failAll(probe.StrParam(params, "expect", ""), judge.Entries(assert.TCP, params), msg)
}

func failAll(expect string, entries []judge.Entry, msg string) []probe.Observation {
	return append(probe.FailAll(msg, hostMetrics(expect)...), judge.FailAll("tcp", entries, msg)...)
}

// probeTCP connects to address:port, optionally writes send and reads the
// target's first output: until expect appears (when set) and the first
// line ended (when an assertion looks at the response), within 4 KiB and
// the timeout. Each assertion is judged on an up attempt (backlog 3.87).
func probeTCP(ctx context.Context, g *probe.Guard, params map[string]any, address string) []probe.Observation {
	send := probe.StrParam(params, "send", "")
	expect := probe.StrParam(params, "expect", "")
	entries := judge.Entries(assert.TCP, params)
	readsLine := judge.Uses(entries, assert.SubjectResponse, assert.SubjectResponseMS)

	host, err := probe.TargetHost(address)
	if err != nil {
		return failAll(expect, entries, err.Error())
	}
	port, ok := probe.PortParam(params, 0)
	if !ok {
		return failAll(expect, entries, probe.ErrBadPort.Error())
	}

	ctx, cancel := probe.Bound(ctx, params, tcpDefaultTimeout)
	defer cancel()

	start := time.Now()
	// ctx carries the timeout, so the dialer needs none of its own.
	conn, err := g.Dialer(0).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, probe.ErrNotAllowed) {
			return failAll(expect, entries, probe.ErrNotAllowed.Error())
		}
		return probe.Down("tcp", probe.Classify(err))
	}
	r := reading{connect: time.Since(start)}
	defer func() { _ = conn.Close() }()

	// A banner that does not match is no failure: the port was up.
	obs := append(probe.Up("tcp"), probe.Value("tcp.connect_ms", probe.Millis(r.connect)))
	if send != "" || expect != "" || readsLine {
		// The deadline is set before the cancel hook, so a cancel that has
		// already happened is not overwritten by it.
		if dl, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(dl)
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
		defer stop()

		if send != "" {
			// A failed write still leaves the port up; banner_match and the
			// assertions say the rest.
			_, _ = conn.Write([]byte(send))
		}
		if expect != "" || readsLine {
			// A reset or close during the read stays up: the port accepted
			// the connection, and banner_match = 0 says the rest.
			r.received = readResponse(conn, expect, readsLine, tcpBannerLimit)
			r.elapsed = time.Since(start)
		}
		if expect != "" {
			obs = append(obs, probe.BoolValue("tcp.banner_match", bytes.Contains(r.received, []byte(expect))))
		}
	}
	return append(obs, judge.Observe("tcp", entries, r.judge)...)
}

// readResponse reads from conn until want appears (or want is "") and,
// when line is set, a line break arrived — or until limit bytes were read
// or the read fails (EOF, deadline). Nothing read leaves the probe but
// through an assertion's actual.
func readResponse(conn net.Conn, want string, line bool, limit int) []byte {
	buf := make([]byte, 0, limit)
	for len(buf) < limit {
		n, err := conn.Read(buf[len(buf):limit])
		buf = buf[:len(buf)+n]
		if (want == "" || bytes.Contains(buf, []byte(want))) && (!line || bytes.IndexByte(buf, '\n') >= 0) {
			return buf
		}
		if err != nil {
			return buf
		}
	}
	return buf
}

// reading is what an up attempt measured: the connect time and, when the
// probe read, what arrived and when the read ended (from the dial's
// start).
type reading struct {
	connect  time.Duration
	received []byte
	elapsed  time.Duration
}

// judge evaluates one of tcp's subjects. response is the first line the
// target sent, without its line break; it and response_ms are absent when
// nothing arrived.
func (r reading) judge(a assert.Assertion, w *judge.Work) judge.Verdict {
	switch a.Subject() {
	case assert.SubjectConnectMS:
		return judge.Number(a, probe.Millis(r.connect))
	case assert.SubjectResponseMS:
		if len(r.received) == 0 {
			return judge.Absent(a)
		}
		return judge.Number(a, probe.Millis(r.elapsed))
	case assert.SubjectResponse:
		return w.Text(a, firstLine(r.received), len(r.received) > 0)
	}
	return judge.Failed(judge.ErrInvalid)
}

// firstLine is b up to its first line break, without a trailing CR.
func firstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	return string(bytes.TrimSuffix(b, []byte("\r")))
}

// Probe is the tcp probe, as the registry runs it.
func Probe(ctx context.Context, g *probe.Guard, params map[string]any, address string, _ map[string]string) []probe.Observation {
	return probeTCP(ctx, g, params, address)
}
