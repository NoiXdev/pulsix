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
	got := backend.recorded()
	if len(got) != 1 || len(got[0].to) != 2 || got[0].to[1] != "dba@example.test" {
		t.Fatalf("recorded = %+v, want one message with two recipients", got)
	}
	if !strings.Contains(string(got[0].data), "To: ops@example.test, dba@example.test\r\n") {
		t.Fatalf("To header missing: %s", got[0].data)
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
