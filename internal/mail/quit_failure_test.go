package mail_test

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/mail"
)

// rudeServer speaks just enough SMTP to accept one message and then hangs
// up without answering QUIT. Real servers do this — a connection reset, an
// idle reaper, a proxy closing a pooled socket the moment the message is
// handed on.
func rudeServer(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		r := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

		w("220 rude.test ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250-rude.test")
				w("250 SIZE 10240000")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				w("250 OK")
			case strings.HasPrefix(cmd, "DATA"):
				w("354 End data with <CR><LF>.<CR><LF>")
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(l) == "." {
						break
					}
				}
				// The message is accepted here. From this point on it is
				// out of our hands and on its way.
				w("250 2.0.0 Ok: queued as ABC123")
			case strings.HasPrefix(cmd, "QUIT"):
				// and then simply vanishes, without the 221 the client waits for.
				return
			default:
				w("250 OK")
			}
		}
	}()

	h, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, n
}

// Backlog 1.25. The server accepted the message — the 250 after the final
// dot says so — and only then dropped the connection. Reporting that as a
// delivery failure makes the caller undo work that did happen: the
// invitation is deleted while its mail is already on its way, and the
// recipient is left holding a link to something that no longer exists.
func TestSendSucceedsWhenTheServerHangsUpAfterAcceptingTheMessage(t *testing.T) {
	host, port := rudeServer(t)
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: host, Port: port, From: "pulsix@example.test"}, nil
	})

	if err := m.Send(context.Background(), "someone@example.test", "Subject", "Body"); err != nil {
		t.Fatalf("Send: %v — the message was accepted before the hang-up, so this is not a delivery failure", err)
	}
}
