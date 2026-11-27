package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/outbound"
)

// emailTimeout bounds one e-mail send (the whole SMTP conversation).
const emailTimeout = 30 * time.Second

// maxErrorText bounds the error a delivery row keeps.
const maxErrorText = 500

// permanentError marks a failure a retry cannot fix: a refused
// destination, a redirect, a 4xx answer, an instance without mail. (A body
// template always renders since the placeholder syntax of Task 2.) The dispatcher fails such a delivery at
// once instead of retrying it.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks err as not worth retrying.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// ChannelSender delivers one event through one channel.
type ChannelSender struct {
	box     Sealer
	mail    Mailer
	http    *http.Client
	now     func() time.Time
	presets Presets
}

// NewChannelSender wires the sender. client must be an outbound client
// (internal/outbound): it is the one that refuses internal destinations.
// presets renders the body of a preset channel (presets.Registry); a
// channel whose preset is missing from it fails permanently.
func NewChannelSender(box Sealer, m Mailer, client *http.Client, now func() time.Time, presets Presets) *ChannelSender {
	if now == nil {
		now = time.Now
	}
	return &ChannelSender{box: box, mail: m, http: client, now: now, presets: presets}
}

// Send delivers ev through ch. deliveryID travels as X-Pulsix-Delivery so
// a receiver can drop the duplicate an at-least-once retry may produce.
func (s *ChannelSender) Send(ctx context.Context, c Claimed, ev Event) error {
	ch, deliveryID := c.Channel, c.Delivery.ID
	switch ch.Kind {
	case KindEmail:
		var cfg EmailConfig
		if err := json.Unmarshal(ch.Config, &cfg); err != nil {
			return Permanent(fmt.Errorf("decode e-mail channel: %w", err))
		}
		// A retry after a partial send goes only to the addresses that
		// attempt did not reach; everything else goes to the channel's
		// own list (backlog 2.74).
		to := cfg.Recipients
		if len(c.Delivery.PendingRecipients) > 0 {
			to = c.Delivery.PendingRecipients
		}
		return s.sendEmail(ctx, to, ev)
	case KindWebhook:
		w, err := s.openWebhook(ch)
		if err != nil {
			return Permanent(err)
		}
		return s.sendWebhook(ctx, w, deliveryID, ev)
	}
	return Permanent(fmt.Errorf("unknown channel kind %q", ch.Kind))
}

func (s *ChannelSender) sendEmail(ctx context.Context, to []string, ev Event) error {
	ok, err := s.mail.Configured(ctx)
	if err != nil {
		return fmt.Errorf("check outgoing mail: %w", err)
	}
	if !ok {
		return Permanent(ErrMailUnavailable)
	}
	subject, body := renderEmail(ev)
	ctx, cancel := context.WithTimeout(ctx, emailTimeout)
	defer cancel()
	if err := s.mail.SendMany(ctx, to, subject, body); err != nil {
		if errors.Is(err, mail.ErrNotConfigured) {
			return Permanent(ErrMailUnavailable)
		}
		return fmt.Errorf("send e-mail: %w", err)
	}
	return nil
}

// openedWebhook is a webhook channel with its secrets opened, only ever
// held for the duration of one send.
type openedWebhook struct {
	method, url, bodyTemplate string
	preset                    string
	settings                  PresetSettings
	secrets                   map[string]string
	headers                   [][2]string
	signingSecret             []byte
}

func (s *ChannelSender) openWebhook(ch Channel) (openedWebhook, error) {
	var w webhookConfig
	if err := json.Unmarshal(ch.Config, &w); err != nil {
		return openedWebhook{}, fmt.Errorf("decode webhook channel: %w", err)
	}
	aad := AAD(ch.OrgID, ch.ID)
	u, err := s.box.OpenAAD(w.URLSealed, aad)
	if err != nil {
		return openedWebhook{}, fmt.Errorf("open webhook url: %w", err)
	}
	out := openedWebhook{method: w.Method, url: string(u), bodyTemplate: w.BodyTemplate, preset: ch.Preset, settings: w.PresetSettings}
	for _, sec := range w.Secrets {
		v, err := s.box.OpenAAD(sec.ValueSealed, aad)
		if err != nil {
			return openedWebhook{}, fmt.Errorf("open secret %s: %w", sec.Name, err)
		}
		if out.secrets == nil {
			out.secrets = map[string]string{}
		}
		out.secrets[sec.Name] = string(v)
	}
	for _, h := range w.Headers {
		v, err := s.box.OpenAAD(h.ValueSealed, aad)
		if err != nil {
			return openedWebhook{}, fmt.Errorf("open header %s: %w", h.Name, err)
		}
		out.headers = append(out.headers, [2]string{h.Name, string(v)})
	}
	if len(w.SigningSealed) > 0 {
		if out.signingSecret, err = s.box.OpenAAD(w.SigningSealed, aad); err != nil {
			return openedWebhook{}, fmt.Errorf("open signing secret: %w", err)
		}
	}
	return out, nil
}

// sendWebhook posts the rendered requests in order: one for a raw
// channel, as many as its preset renders otherwise (backlog 4.8). The
// first failure fails the attempt; a retry sends every request again.
func (s *ChannelSender) sendWebhook(ctx context.Context, w openedWebhook, deliveryID uuid.UUID, ev Event) error {
	reqs, err := s.webhookRequests(w, ev)
	if err != nil {
		return Permanent(err)
	}
	for i, r := range reqs {
		if err := s.sendRequest(ctx, w, r, deliveryID, ev); err != nil {
			if len(reqs) == 1 {
				return err
			}
			wrapped := fmt.Errorf("request %d of %d: %w", i+1, len(reqs), err)
			if IsPermanent(err) {
				return Permanent(wrapped)
			}
			return wrapped
		}
	}
	return nil
}

// sendRequest posts one request. Operator headers are set first, so the
// X-Pulsix-* headers (validation already refuses them as operator
// headers) always carry Pulsix's own values, and a preset's own headers
// come after the operator's. With a signing secret the signature covers
// "<timestamp>.<body>", the scheme incoming Pulsix webhooks use too, so a
// replayed body with a fresh timestamp fails.
func (s *ChannelSender) sendRequest(ctx context.Context, w openedWebhook, r Request, deliveryID uuid.UUID, ev Event) error {
	target := w.url
	if r.Path != "" {
		u, err := url.Parse(w.url)
		if err != nil {
			return Permanent(errors.New("build request: the stored URL does not parse"))
		}
		u.Path, u.RawPath, u.RawQuery = r.Path, "", ""
		target = u.String()
	}
	req, err := http.NewRequestWithContext(ctx, w.method, target, bytes.NewReader(r.Body))
	if err != nil {
		return Permanent(fmt.Errorf("build request: %v", redactURL(err)))
	}
	contentType := r.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "Pulsix-Notify/1")
	for _, h := range w.headers {
		req.Header.Set(h[0], h[1])
	}
	for _, h := range r.Header {
		req.Header.Set(h[0], h[1])
	}
	ts := strconv.FormatInt(s.now().Unix(), 10)
	req.Header.Set("X-Pulsix-Event", string(ev.Kind))
	req.Header.Set("X-Pulsix-Delivery", deliveryID.String())
	req.Header.Set("X-Pulsix-Timestamp", ts)
	if len(w.signingSecret) > 0 {
		mac := hmac.New(sha256.New, w.signingSecret)
		mac.Write([]byte(ts + "."))
		mac.Write(r.Body)
		req.Header.Set("X-Pulsix-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := s.http.Do(req)
	if err != nil {
		err = redactURL(err)
		if errors.Is(err, outbound.ErrBlocked) || errors.Is(err, outbound.ErrRedirect) {
			return Permanent(err)
		}
		return err
	}
	defer resp.Body.Close()                                       //nolint:errcheck
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // reuse the connection
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500:
		return fmt.Errorf("the endpoint answered %d", code)
	default:
		return Permanent(fmt.Errorf("the endpoint answered %d", code))
	}
}

// webhookRequests are the requests a webhook channel sends: its preset's
// rendering (backlog 4.7, 4.8), else one request carrying the body
// template or the event's JSON. Every body stays under maxBodyBytes.
func (s *ChannelSender) webhookRequests(w openedWebhook, ev Event) ([]Request, error) {
	if w.preset == "" {
		body, err := renderWebhookBody(w.bodyTemplate, ev)
		if err != nil {
			return nil, err
		}
		return []Request{{Body: body}}, nil
	}
	p, ok := s.presets[w.preset]
	if !ok {
		return nil, fmt.Errorf("unknown preset %q", w.preset)
	}
	u, err := url.Parse(w.url)
	if err != nil {
		return nil, errors.New("the stored URL does not parse")
	}
	reqs, err := p.Render(ev, Target{Settings: w.settings, URL: u, Secrets: w.secrets})
	if err != nil {
		return nil, fmt.Errorf("render %s message: %w", w.preset, err)
	}
	if len(reqs) == 0 {
		return nil, errNoRequests
	}
	for _, r := range reqs {
		if len(r.Body) > maxBodyBytes {
			return nil, errBodyTooLarge
		}
	}
	return reqs, nil
}

// redactURL drops the request URL from a transport error: url.Error's
// text repeats the whole URL, and a webhook URL can carry a token. The
// wrapped cause (a refused address, a timeout, a TLS failure) stays.
func redactURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return fmt.Errorf("%s request timed out", ue.Op)
		}
		return fmt.Errorf("%s request failed: %w", ue.Op, ue.Err)
	}
	return err
}

// errorText is the bounded, single-line error a delivery row keeps.
func errorText(err error) string {
	msg := []rune(err.Error())
	if len(msg) > maxErrorText {
		msg = append(msg[:maxErrorText], '…')
	}
	return string(msg)
}
