package mail_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/mail"
)

func TestSendManyDeliversOneMessageToEveryRecipient(t *testing.T) {
	backend := &testBackend{username: "pulsix", password: "correct horse"}
	host, port := startTestServer(t, backend, true)
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: host, Port: port, Username: "pulsix", Password: "correct horse", From: "pulsix@example.test"}, nil
	})
	if err := m.SendMany(context.Background(), []string{"ops@example.test", "dba@example.test"}, "Run failed", "body"); err != nil {
		t.Fatal(err)
	}
	// One message per recipient since backlog 2.74 — this test used to
	// assert the opposite (one message carrying both addresses in its
	// To: header), which is the disclosure that row exists to remove.
	// Its real point survives unchanged: SendMany reaches everyone, and
	// each message is addressed to the person reading it.
	got := backend.recorded()
	if len(got) != 2 {
		t.Fatalf("recorded %d messages, want one per recipient: %+v", len(got), got)
	}
	for _, want := range []string{"ops@example.test", "dba@example.test"} {
		var found bool
		for _, msg := range got {
			if len(msg.to) == 1 && msg.to[0] == want {
				found = true
				if !strings.Contains(string(msg.data), "To: "+want+"\r\n") {
					t.Fatalf("the message to %s has no matching To header: %s", want, msg.data)
				}
			}
		}
		if !found {
			t.Fatalf("%s got no message of their own: %+v", want, got)
		}
	}
}

func TestSendManyRefusesNoRecipientsAndLineBreaks(t *testing.T) {
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: "127.0.0.1", Port: 1, From: "pulsix@example.test"}, nil
	})
	if err := m.SendMany(context.Background(), nil, "s", "b"); err == nil {
		t.Fatal("no recipients accepted")
	}
	if err := m.SendMany(context.Background(), []string{"a@example.test", "b@example.test\r\nBcc: x@example.test"}, "s", "b"); err == nil || !strings.Contains(err.Error(), "line break") {
		t.Fatalf("err = %v, want the line-break refusal", err)
	}
	failing := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) { return mail.SMTPSettings{}, errors.New("db down") })
	if err := failing.SendMany(context.Background(), []string{"a@example.test"}, "s", "b"); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("loader error = %v", err)
	}
}

func TestConfigured(t *testing.T) {
	cases := []struct {
		name    string
		load    func(context.Context) (mail.SMTPSettings, error)
		want    bool
		wantErr bool
	}{
		{"stored", func(context.Context) (mail.SMTPSettings, error) {
			return mail.SMTPSettings{Host: "smtp.example.test"}, nil
		}, true, false},
		{"nothing stored", func(context.Context) (mail.SMTPSettings, error) {
			return mail.SMTPSettings{}, fmt.Errorf("not found: %w", mail.ErrNotConfigured)
		}, false, false},
		{"empty host", func(context.Context) (mail.SMTPSettings, error) { return mail.SMTPSettings{}, nil }, false, false},
		{"loader failure", func(context.Context) (mail.SMTPSettings, error) { return mail.SMTPSettings{}, errors.New("db down") }, false, true},
	}
	for _, c := range cases {
		got, err := mail.NewSMTP(c.load).Configured(context.Background())
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: Configured = %v, %v", c.name, got, err)
		}
	}
}

// TestSendGivesUpOnASilentServer: a server that accepts the connection and
// never greets must not hold the caller past its context.
func TestSendGivesUpOnASilentServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	defer func() {
		select {
		case c := <-accepted:
			_ = c.Close()
		default:
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: "127.0.0.1", Port: addr.Port, From: "pulsix@example.test"}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := m.Send(ctx, "dest@example.test", "s", "b"); err == nil {
		t.Fatal("a silent server accepted the message")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Send took %s against a silent server", elapsed)
	}
}

// TestSendEndsImmediatelyOnContextCancellationWithoutDeadline is fix-round-1
// Minor 1: a context cancelled directly (a client disconnect, a
// dispatcher shutting down), rather than one that reaches its own
// deadline, must still end the conversation right away — not wait out the
// full 30s sessionTimeout, which is what happens when only ctx.Deadline
// is honoured after the dial.
func TestSendEndsImmediatelyOnContextCancellationWithoutDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	defer func() {
		select {
		case c := <-accepted:
			_ = c.Close()
		default:
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: "127.0.0.1", Port: addr.Port, From: "pulsix@example.test"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if err := m.Send(ctx, "dest@example.test", "s", "b"); err == nil {
		t.Fatal("a cancelled context (no deadline) accepted the message")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Send took %s after a context cancellation with no deadline, want well under sessionTimeout", elapsed)
	}
}
