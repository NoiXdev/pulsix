package mail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestToHeaderFoldsBelowTheLineLimit is backlog 2.79. The To: header was
// one unfolded line, so a channel with enough recipients produced a line
// past RFC 5322 §2.1.1's 998-character limit — which a strict server may
// reject and a lenient one may truncate, silently dropping recipients
// from a notification. The folded header must carry every address, keep
// each line inside the limit, and still unfold to the same list.
func TestToHeaderFoldsBelowTheLineLimit(t *testing.T) {
	var to []string
	for i := 0; i < 60; i++ {
		to = append(to, fmt.Sprintf("ops-team-member-%02d@a-fairly-long-example-domain.test", i))
	}
	msg := string(buildMessage("pulsix@example.test", to, "Subject", "body"))

	head, _, ok := strings.Cut(msg, "\r\n\r\n")
	if !ok {
		t.Fatal("message has no header/body separator")
	}
	for _, line := range strings.Split(head, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("header line is %d characters, over RFC 5322's 998: %.80s…", len(line), line)
		}
	}
	// Exactly one To: field — the one the From: line precedes. Folding
	// uses continuation lines, never a repeated field.
	if n := strings.Count(head, "\r\nTo:"); n != 1 {
		t.Fatalf("To: appears as a field %d times, want 1", n)
	}

	// Unfolding (RFC 5322 §2.2.3) must give back exactly the input list.
	start := strings.Index(head, "To:")
	if start < 0 {
		t.Fatal("no To: field in the header")
	}
	field := head[start:]
	if end := strings.Index(field, "\r\nSubject:"); end >= 0 {
		field = field[:end]
	}
	unfolded := strings.ReplaceAll(field, "\r\n", "")
	got := strings.Split(strings.TrimPrefix(unfolded, "To:"), ",")
	if len(got) != len(to) {
		t.Fatalf("unfolded To: has %d addresses, want %d", len(got), len(to))
	}
	for i := range got {
		if strings.TrimSpace(got[i]) != to[i] {
			t.Fatalf("address %d = %q, want %q", i, strings.TrimSpace(got[i]), to[i])
		}
	}
}

// TestToHeaderKeepsAnOverlongSingleAddressIntact pins the deliberate
// exception: an address longer than the line limit is emitted whole on
// its own line rather than split, because a fold inside an address
// corrupts it. Too long and intact beats short and wrong.
func TestToHeaderKeepsAnOverlongSingleAddressIntact(t *testing.T) {
	long := strings.Repeat("x", 1100) + "@example.test"
	msg := string(buildMessage("pulsix@example.test", []string{"short@example.test", long}, "Subject", "body"))
	if !strings.Contains(msg, long) {
		t.Fatal("the over-long address was altered; folding must never split inside an address")
	}
}

// TestCancelledSendWrapsTheContextError is backlog 2.79. deliver arms a
// context.AfterFunc that pushes the connection's deadline to now the
// moment the caller's context ends, so the blocked read returns promptly
// — but it returns a plain "i/o timeout", with nothing left to say the
// cause was a cancellation. A dispatcher shutting down could therefore
// not tell "we are stopping" apart from "the mail server failed", and
// treated an interrupted send as a server fault worth retrying. The error
// now carries the context's own error alongside the transport one, so
// errors.Is finds it.
func TestCancelledSendWrapsTheContextError(t *testing.T) {
	// A server that accepts the connection and never sends a greeting:
	// deliver gets past the dial and then blocks reading.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close() //nolint:errcheck // test
	accepted := make(chan struct{})
	go func() {
		conn, aerr := l.Accept()
		if aerr != nil {
			return
		}
		close(accepted)
		<-time.After(30 * time.Second)
		_ = conn.Close()
	}()

	host, portStr, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(portStr)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-accepted
		cancel()
	}()

	err = deliver(ctx, SMTPSettings{Host: host, Port: port, From: "a@example.test"},
		[]string{"b@example.test"}, "Subject", "body")
	if err == nil {
		t.Fatal("want an error from the cancelled conversation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error does not report the cancellation: %v", err)
	}
}

// TestSessionDeadlinePicksTheNearerBound covers both branches of the
// session bound, including the one no test could reach before without
// waiting out a real 30-second conversation (backlog 2.79).
func TestSessionDeadlinePicksTheNearerBound(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		ctx  func(t *testing.T) context.Context
		want time.Time
	}{
		{
			name: "no caller deadline falls back to sessionTimeout",
			ctx:  func(*testing.T) context.Context { return context.Background() },
			want: now.Add(sessionTimeout),
		},
		{
			name: "a nearer caller deadline wins",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), now.Add(5*time.Second))
				t.Cleanup(cancel)
				return ctx
			},
			want: now.Add(5 * time.Second),
		},
		{
			name: "a farther caller deadline does not extend the session",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Hour))
				t.Cleanup(cancel)
				return ctx
			},
			want: now.Add(sessionTimeout),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sessionDeadline(c.ctx(t), now); !got.Equal(c.want) {
				t.Fatalf("deadline = %s, want %s", got, c.want)
			}
		})
	}
}
