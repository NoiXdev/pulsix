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
	"net"
	"net/smtp"
	"strconv"
	"strings"
)

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
// delivers one message.
func (m *SMTP) Send(ctx context.Context, to, subject, body string) error {
	settings, err := m.load(ctx)
	if err != nil {
		return fmt.Errorf("load smtp settings: %w", err)
	}
	return deliver(settings, to, subject, body)
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
	return deliver(settings, to, "Pulsix test e-mail", "This is a test message sent from your Pulsix instance's SMTP settings.")
}

// Discard is a Mailer that accepts and drops every message. It exists for
// tests and for any environment that deliberately runs without outbound
// mail configured.
type Discard struct{}

// Send always succeeds and sends nothing.
func (Discard) Send(context.Context, string, string, string) error { return nil }

// deliver opens one SMTP conversation and sends a single message. It is
// deliberately not net/smtp's own SendMail: that function decides on its
// own, from what the server advertises, whether to use STARTTLS, giving the
// caller no way to either insist on it or refuse it. Here StartTLS is an
// explicit setting: when true and the server does not offer the extension,
// this fails loudly instead of silently sending in the clear; when false,
// no TLS is attempted at all, matching a test server that offers none.
// InsecureSkipVerify is never set — the certificate is always verified
// against settings.Host.
func deliver(settings SMTPSettings, to, subject, body string) error {
	if settings.Host == "" {
		return errors.New("smtp: not configured")
	}
	addr := net.JoinHostPort(settings.Host, strconv.Itoa(settings.Port))
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
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
		if ok, _ := c.Extension("AUTH"); ok {
			auth := smtp.PlainAuth("", settings.Username, settings.Password, settings.Host)
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("authenticate: %w", err)
			}
		}
	}

	if err := c.Mail(settings.From); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(buildMessage(settings.From, to, subject, body)); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close message: %w", err)
	}
	return c.Quit()
}

// buildMessage renders a minimal RFC 5322 message. It carries no MIME
// boilerplate (no Content-Type, no charset) since every caller in this
// codebase sends plain ASCII text.
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String())
}
