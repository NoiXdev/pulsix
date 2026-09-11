package mail_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/noixdev/pulsix/internal/mail"
)

// recordedMessage is one delivery the test backend accepted.
type recordedMessage struct {
	from string
	to   []string
	data []byte
}

// testBackend is an in-process SMTP server backend that requires PLAIN auth
// against a fixed username/password and records every message it accepts,
// so tests can assert recipient, subject and body of what actually crossed
// the wire — not just that Send returned nil.
type testBackend struct {
	username, password string

	mu       sync.Mutex
	messages []recordedMessage
}

func (b *testBackend) NewSession(_ *gosmtp.Conn) (gosmtp.Session, error) {
	return &testSession{backend: b}, nil
}

func (b *testBackend) recorded() []recordedMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]recordedMessage, len(b.messages))
	copy(out, b.messages)
	return out
}

type testSession struct {
	backend *testBackend
	from    string
	to      []string
}

func (s *testSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *testSession) Auth(_ string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, username, password string) error {
		if username != s.backend.username || password != s.backend.password {
			return gosmtp.ErrAuthFailed
		}
		return nil
	}), nil
}

func (s *testSession) Mail(from string, _ *gosmtp.MailOptions) error {
	s.from = from
	return nil
}

func (s *testSession) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	s.to = append(s.to, to)
	return nil
}

func (s *testSession) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.backend.mu.Lock()
	s.backend.messages = append(s.backend.messages, recordedMessage{from: s.from, to: append([]string(nil), s.to...), data: data})
	s.backend.mu.Unlock()
	return nil
}

func (s *testSession) Reset()        { s.from, s.to = "", nil }
func (s *testSession) Logout() error { return nil }

// startTestServer starts an in-process SMTP server on 127.0.0.1:0 (an
// ephemeral port), the same approach apitest.New uses for the HTTP API,
// and returns the host and port to connect to. allowInsecureAuth controls
// go-smtp's own AllowInsecureAuth: the server offers no TLS at all (this
// package's own deliver only advertises STARTTLS when a *tls.Config is
// set, and none is here), so with it false go-smtp refuses to advertise
// AUTH at all — which is exactly the "server does not support AUTH"
// scenario TestSendFailsWhenCredentialsProvidedButServerDoesNotAdvertiseAUTH
// needs, without having to fake a non-authenticating backend.
func startTestServer(t *testing.T, backend *testBackend, allowInsecureAuth bool) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := gosmtp.NewServer(backend)
	srv.Domain = "localhost"
	srv.AllowInsecureAuth = allowInsecureAuth
	go func() {
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() { _ = srv.Close() })

	h, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	portNum, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, portNum
}

func TestSendDeliversRecipientSubjectAndBody(t *testing.T) {
	backend := &testBackend{username: "pulsix", password: "correct horse"}
	host, port := startTestServer(t, backend, true)

	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: host, Port: port, Username: "pulsix", Password: "correct horse", From: "pulsix@example.test"}, nil
	})
	if err := m.Send(context.Background(), "dest@example.test", "Hello", "This is the body."); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := backend.recorded()
	if len(got) != 1 {
		t.Fatalf("recorded %d messages, want 1", len(got))
	}
	msg := got[0]
	if msg.from != "pulsix@example.test" {
		t.Errorf("from = %q", msg.from)
	}
	if len(msg.to) != 1 || msg.to[0] != "dest@example.test" {
		t.Errorf("to = %v", msg.to)
	}
	if !strings.Contains(string(msg.data), "Subject: Hello") {
		t.Errorf("message missing subject: %s", msg.data)
	}
	if !strings.Contains(string(msg.data), "This is the body.") {
		t.Errorf("message missing body: %s", msg.data)
	}
}

func TestTestWithWrongPasswordFails(t *testing.T) {
	backend := &testBackend{username: "pulsix", password: "correct horse"}
	host, port := startTestServer(t, backend, true)

	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: host, Port: port, Username: "pulsix", Password: "wrong password", From: "pulsix@example.test"}, nil
	})
	if err := m.Test(context.Background(), "dest@example.test"); err == nil {
		t.Fatal("Test with a wrong password: got nil error, want one")
	}
	if len(backend.recorded()) != 0 {
		t.Fatal("a failed authentication must not deliver a message")
	}
}

func TestSendPropagatesSettingsLoaderError(t *testing.T) {
	loadErr := errors.New("settings unavailable")
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) { return mail.SMTPSettings{}, loadErr })
	err := m.Send(context.Background(), "dest@example.test", "s", "b")
	if !errors.Is(err, loadErr) {
		t.Fatalf("Send error = %v, want it to wrap %v", err, loadErr)
	}
}

func TestSendFailsWhenNotConfigured(t *testing.T) {
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) { return mail.SMTPSettings{}, nil })
	if err := m.Send(context.Background(), "dest@example.test", "s", "b"); err == nil {
		t.Fatal("Send with an empty Host: got nil error, want one")
	}
}

func TestSendRequiringStartTLSFailsWhenServerDoesNotOfferIt(t *testing.T) {
	backend := &testBackend{username: "pulsix", password: "correct horse"}
	host, port := startTestServer(t, backend, true)

	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: host, Port: port, From: "pulsix@example.test", StartTLS: true}, nil
	})
	if err := m.Send(context.Background(), "dest@example.test", "s", "b"); err == nil {
		t.Fatal("Send with StartTLS against a plaintext-only server: got nil error, want one")
	}
}

func TestTestPropagatesSettingsLoaderError(t *testing.T) {
	loadErr := errors.New("settings unavailable")
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) { return mail.SMTPSettings{}, loadErr })
	err := m.Test(context.Background(), "dest@example.test")
	if !errors.Is(err, loadErr) {
		t.Fatalf("Test error = %v, want it to wrap %v", err, loadErr)
	}
}

func TestSendFailsOnDialError(t *testing.T) {
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		// Port 0 with no listener behind it never accepts a connection.
		return mail.SMTPSettings{Host: "127.0.0.1", Port: 1, From: "pulsix@example.test"}, nil
	})
	if err := m.Send(context.Background(), "dest@example.test", "s", "b"); err == nil {
		t.Fatal("Send against an unreachable host: got nil error, want one")
	}
}

func TestDiscardAlwaysSucceedsAndSendsNothing(t *testing.T) {
	var d mail.Discard
	if err := d.Send(context.Background(), "dest@example.test", "s", "b"); err != nil {
		t.Fatalf("Discard.Send: %v", err)
	}
}

// TestSendFailsWhenCredentialsProvidedButServerDoesNotAdvertiseAUTH is a
// regression test for the "also fix" item in Task 12 fix round 1: deliver
// used to silently fall through to an unauthenticated SMTP conversation
// when a username was configured but the server did not advertise AUTH.
// An operator who supplied credentials expects them to be used — falling
// back silently either masks a real misconfiguration or, worse, lets mail
// go out unauthenticated when the operator believed a credential was
// required. Starting the server with allowInsecureAuth=false is what makes
// go-smtp itself refuse to advertise AUTH over a connection with no TLS
// (see startTestServer's own doc comment) — a realistic stand-in for a
// production server that requires STARTTLS before AUTH.
func TestSendFailsWhenCredentialsProvidedButServerDoesNotAdvertiseAUTH(t *testing.T) {
	backend := &testBackend{username: "pulsix", password: "correct horse"}
	host, port := startTestServer(t, backend, false)

	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: host, Port: port, Username: "pulsix", Password: "correct horse", From: "pulsix@example.test"}, nil
	})
	if err := m.Send(context.Background(), "dest@example.test", "s", "b"); err == nil {
		t.Fatal("Send with credentials but no AUTH advertised: got nil error, want one")
	}
	if len(backend.recorded()) != 0 {
		t.Fatal("a message must not be delivered unauthenticated when credentials were configured")
	}
}

// TestSubjectCannotInjectHeaders covers M4: an organization name carrying a
// CR/LF goes into the invitation's Subject header, and must not be able to
// end that header and start one of its own.
func TestSubjectCannotInjectHeaders(t *testing.T) {
	backend := &testBackend{username: "pulsix", password: "correct horse"}
	host, port := startTestServer(t, backend, true)
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: host, Port: port, Username: "pulsix", Password: "correct horse", From: "pulsix@example.test"}, nil
	})

	subject := "Invitation to Acme\r\nBcc: attacker@evil.test\r\nX-Injected: yes"
	if err := m.Send(context.Background(), "dest@example.test", subject, "body"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := backend.recorded()
	if len(got) != 1 {
		t.Fatalf("recorded %d messages, want 1", len(got))
	}
	headers, _, _ := strings.Cut(string(got[0].data), "\r\n\r\n")
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") || strings.HasPrefix(line, "X-Injected:") {
			t.Fatalf("subject injected a header line %q into:\n%s", line, headers)
		}
	}
	if !strings.Contains(headers, "Subject: Invitation to Acme Bcc: attacker@evil.test X-Injected: yes") {
		t.Fatalf("subject not folded onto one line:\n%s", headers)
	}
}

// TestNonASCIISubjectIsEncoded: a subject that is not plain ASCII (an
// organization named "Müller GmbH") goes out as an RFC 2047 encoded word.
func TestNonASCIISubjectIsEncoded(t *testing.T) {
	backend := &testBackend{username: "pulsix", password: "correct horse"}
	host, port := startTestServer(t, backend, true)
	m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
		return mail.SMTPSettings{Host: host, Port: port, Username: "pulsix", Password: "correct horse", From: "pulsix@example.test"}, nil
	})
	if err := m.Send(context.Background(), "dest@example.test", "Invitation to Müller GmbH", "body"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	data := string(backend.recorded()[0].data)
	if !strings.Contains(data, "Subject: =?utf-8?q?") || !strings.Contains(data, "charset=utf-8") {
		t.Fatalf("non-ASCII subject not encoded:\n%s", data)
	}
}

// TestAddressesWithLineBreaksAreRefused: the recipient and sender go into
// header lines too, so a line break in either is refused before anything is
// sent.
func TestAddressesWithLineBreaksAreRefused(t *testing.T) {
	backend := &testBackend{username: "pulsix", password: "correct horse"}
	host, port := startTestServer(t, backend, true)
	for _, tc := range []struct{ from, to string }{
		{"pulsix@example.test", "dest@example.test\r\nBcc: x@evil.test"},
		{"pulsix@example.test\nBcc: x@evil.test", "dest@example.test"},
	} {
		m := mail.NewSMTP(func(context.Context) (mail.SMTPSettings, error) {
			return mail.SMTPSettings{Host: host, Port: port, Username: "pulsix", Password: "correct horse", From: tc.from}, nil
		})
		if err := m.Send(context.Background(), tc.to, "s", "b"); err == nil {
			t.Errorf("Send(from=%q, to=%q) succeeded, want a refusal", tc.from, tc.to)
		}
	}
	if len(backend.recorded()) != 0 {
		t.Fatal("a message with a line break in an address was delivered")
	}
}
