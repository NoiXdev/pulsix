package redis

import (
	"bufio"
	"context"
	"crypto/tls"
	"net"
	"strings"
)

// client is one open connection to a server. Its deadline is the round's,
// and the caller's context ending closes it, so no read or write outlives
// the round.
type client struct {
	conn net.Conn
	r    *bufio.Reader
	stop func() bool
}

// dial connects under ctx, wraps the connection in TLS when asked, and puts
// ctx's deadline on the connection. TLS is verified against the host name in
// the secret ("true") or not at all ("skip-verify"); no custom CA is read.
func dial(ctx context.Context, s settings) (*client, error) {
	var d net.Dialer
	raw, err := d.DialContext(ctx, s.network, s.addr)
	if err != nil {
		return nil, err
	}
	conn := raw
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	}
	// A cancelled context closes the raw connection, which fails whatever
	// read, write or handshake is in progress.
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	if s.tls != "false" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if s.tls == "skip-verify" {
			cfg.InsecureSkipVerify = true //nolint:gosec // the operator chose skip-verify in the secret
		} else {
			cfg.ServerName = s.host
		}
		tc := tls.Client(raw, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			stop()
			_ = raw.Close()
			return nil, err
		}
		conn = tc
	}
	return &client{conn: conn, r: bufio.NewReaderSize(conn, maxLine), stop: stop}, nil
}

// Close ends the connection. Its writes are bounded by the deadline set in
// dial, so it cannot hold the round.
func (c *client) Close() {
	c.stop()
	_ = c.conn.Close()
}

// do sends one command and reads its reply.
func (c *client) do(args ...string) (reply, error) {
	if err := writeCommand(c.conn, args...); err != nil {
		return reply{}, err
	}
	return readReply(c.r)
}

// auth logs in with AUTH [user] password.
func (c *client) auth(user, password string) error {
	args := []string{"AUTH"}
	if user != "" {
		args = append(args, user)
	}
	rep, err := c.do(append(args, password)...)
	if err != nil {
		return err
	}
	if rep.kind != '+' {
		return errProtocol
	}
	return nil
}

// info runs INFO without sections, the form every version from 5 on takes
// (the multi-section form is Redis 7), and parses its key:value lines.
func (c *client) info() (map[string]string, error) {
	rep, err := c.do("INFO")
	if err != nil {
		return nil, err
	}
	if rep.kind != '$' || rep.null {
		return nil, errProtocol
	}
	return parseInfo(rep.bulk), nil
}

// parseInfo reads INFO's text: key:value lines, with # section headers and
// blank lines skipped. A repeated key keeps its last value.
func parseInfo(text string) map[string]string {
	out := make(map[string]string)
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok && k != "" {
			out[k] = v
		}
	}
	return out
}
