// Package mail sends transactional e-mail over SMTP, configured through
// instance settings rather than the environment: internal/setup persists
// the settings (sealed) and hands this package a loader function so every
// Send/Test call sees whatever is currently stored, without a restart.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// sessionTimeout bounds one whole SMTP conversation, from the dial to
// QUIT, unless the caller's context ends it sooner. Without it a server
// that accepts the connection and never answers would hold the caller —
// an invitation request or a notification dispatcher — forever.
const sessionTimeout = 30 * time.Second

// ErrNotConfigured means the instance has no outbound mail settings at all.
// It is a state, not a delivery failure: callers that can do without mail
// (an invitation, whose link the administrator can hand over instead) tell
// it apart from a server that rejected or could not take the message. A
// settings loader passed to NewSMTP reports "nothing stored" by returning an
// error that wraps it.
var ErrNotConfigured = errors.New("smtp is not configured")

// errHeaderInjection is returned when an address that goes into a header
// line carries a line break.
var errHeaderInjection = errors.New("smtp: address contains a line break")

// SMTPSettings is the outbound mail configuration an instance administrator
// provides through the settings API. Password is held in the clear only in
// memory — internal/setup seals it with the instance master key before it
// is ever persisted, and unseals it only to build one of these.
type SMTPSettings struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	StartTLS bool
}

// SMTP sends mail via net/smtp, using whatever SMTPSettings its loader
// currently reports. It implements tenancy.Mailer.
type SMTP struct {
	load func(ctx context.Context) (SMTPSettings, error)
}

// NewSMTP builds a Mailer that resolves its settings on every call through
// load — normally a *setup.Service's LoadSMTP method — so a settings change
// takes effect immediately, with no need to rebuild this value or restart
// the process.
func NewSMTP(load func(ctx context.Context) (SMTPSettings, error)) *SMTP {
	return &SMTP{load: load}
}

// Send implements tenancy.Mailer: it loads the current settings and
// delivers one message. It returns an error wrapping ErrNotConfigured when
// no settings exist.
func (m *SMTP) Send(ctx context.Context, to, subject, body string) error {
	settings, err := m.load(ctx)
	if err != nil {
		return fmt.Errorf("load smtp settings: %w", err)
	}
	return deliver(ctx, settings, []string{to}, subject, body)
}

// SendMany delivers one message to several recipients in one SMTP
// conversation (every address is a RCPT TO and listed in the To header).
// A notification channel's recipients share the message, the way a
// mailing list does.
func (m *SMTP) SendMany(ctx context.Context, to []string, subject, body string) error {
	if len(to) == 0 {
		return errors.New("smtp: no recipients")
	}
	settings, err := m.load(ctx)
	if err != nil {
		return fmt.Errorf("load smtp settings: %w", err)
	}
	return deliver(ctx, settings, to, subject, body)
}

// Configured reports whether the instance has outbound mail settings. A
// loader error other than "nothing stored" is returned as is.
func (m *SMTP) Configured(ctx context.Context) (bool, error) {
	settings, err := m.load(ctx)
	if errors.Is(err, ErrNotConfigured) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load smtp settings: %w", err)
	}
	return settings.Host != "", nil
}

// Test sends a fixed test message to to, using the currently stored
// settings, and returns whatever error the SMTP conversation actually
// produced (dial failure, authentication rejection, a 5xx from the
// server, ...) so the settings UI can show the server's real response
// rather than a generic failure.
func (m *SMTP) Test(ctx context.Context, to string) error {
	settings, err := m.load(ctx)
	if err != nil {
		return fmt.Errorf("load smtp settings: %w", err)
	}
	return deliver(ctx, settings, []string{to}, "Pulsix test e-mail", "This is a test message sent from your Pulsix instance's SMTP settings.")
}

// Discard is a Mailer that accepts and drops every message. It exists for
// tests and for any environment that deliberately runs without outbound
// mail configured.
type Discard struct{}

// Send always succeeds and sends nothing.
func (Discard) Send(context.Context, string, string, string) error { return nil }

// deliver opens one SMTP conversation, bounded by sessionTimeout or ctx's
// deadline, and sends a single message to every recipient. It is
// deliberately not net/smtp's own SendMail: that function decides on its
// own, from what the server advertises, whether to use STARTTLS, giving the
// caller no way to either insist on it or refuse it. Here StartTLS is an
// explicit setting: when true and the server does not offer the extension,
// this fails loudly instead of silently sending in the clear; when false,
// no TLS is attempted at all, matching a test server that offers none.
// InsecureSkipVerify is never set — the certificate is always verified
// against settings.Host.
func deliver(ctx context.Context, settings SMTPSettings, to []string, subject, body string) error {
	if settings.Host == "" {
		return ErrNotConfigured
	}
	if strings.ContainsAny(settings.From, "\r\n") {
		return errHeaderInjection
	}
	for _, rcpt := range to {
		if strings.ContainsAny(rcpt, "\r\n") {
			return errHeaderInjection
		}
	}
	deadline := time.Now().Add(sessionTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	addr := net.JoinHostPort(settings.Host, strconv.Itoa(settings.Port))
	conn, err := (&net.Dialer{}).DialContext(dctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	// Every later read and write of the conversation shares the deadline.
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return fmt.Errorf("set smtp deadline: %w", err)
	}
	// dctx.Done fires at the deadline above, or the moment ctx itself is
	// cancelled — a caller disconnecting or shutting down should not have
	// to wait out the rest of sessionTimeout for a conn read to notice.
	stop := context.AfterFunc(dctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	c, err := smtp.NewClient(conn, settings.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp greeting from %s: %w", addr, err)
	}
	defer c.Close() //nolint:errcheck

	if settings.StartTLS {
		ok, _ := c.Extension("STARTTLS")
		if !ok {
			return errors.New("smtp: server does not support STARTTLS")
		}
		if err := c.StartTLS(&tls.Config{ServerName: settings.Host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if settings.Username != "" {
		ok, _ := c.Extension("AUTH")
		if !ok {
			// An operator who configured a username expects it to actually
			// be used. Silently falling through to an unauthenticated
			// conversation would either have the server reject the message
			// anyway (masking the real problem as a Mail/Rcpt/Data error)
			// or, worse, have it accept mail the operator believed required
			// a credential — fail loudly here instead.
			return errors.New("smtp: server does not support AUTH but credentials were provided")
		}
		auth := smtp.PlainAuth("", settings.Username, settings.Password, settings.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("authenticate: %w", err)
		}
	}

	if err := c.Mail(settings.From); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("rcpt to: %w", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(buildMessage(settings.From, strings.Join(to, ", "), subject, body)); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close message: %w", err)
	}
	// The server accepted the message here: w.Close sends the final dot and
	// reads the reply, so a non-nil error above means it was refused and a
	// nil one means it is queued and out of our hands.
	//
	// Everything after this point is therefore no longer about delivery.
	// QUIT is a courtesy, and servers routinely skip the 221 — a reset, an
	// idle reaper, a proxy closing a pooled socket the moment the message
	// is handed on. Returning that as a send failure makes callers undo
	// work that did happen: the invitation is deleted while its mail is
	// already travelling, and the recipient is left holding a link to
	// something that no longer exists (backlog 1.25). The deferred Close
	// releases the connection either way.
	_ = c.Quit()
	return nil
}

// buildMessage renders a minimal RFC 5322 plain-text message in UTF-8.
//
// The subject is the one header built from data a user controls (an
// organization's name goes into every invitation's subject), so it is
// encoded defensively rather than trusted: any line break is folded into a
// space first — a CR or LF there would otherwise end the header and let the
// rest of the value inject headers of its own — and the result is written
// as an RFC 2047 encoded word whenever it is not plain printable ASCII,
// which also carries non-ASCII names correctly. Organization names are
// additionally refused control characters at validation
// (tenancy.ValidateOrgName); this is the second line of defence. The
// addresses are checked for line breaks by deliver before this runs.
func buildMessage(from, to, subject, body string) []byte {
	subject = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(subject)
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String())
}
