// Package notify tells people what Pulsix did (spec 2026-09-18 §5, §7):
// notification channels (e-mail through the instance SMTP, a generic
// webhook), a delivery queue shared by every manager, schedule
// notification rules and the weekly digest. Messages carry names, results,
// counts, durations and links — never log output, variables or secrets.
package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Sentinels the API maps to HTTP.
var (
	ErrNotFound         = errors.New("not found")
	ErrInvalidInput     = errors.New("invalid input")
	ErrNameTaken        = errors.New("name already in use")
	ErrRevisionMismatch = errors.New("revision mismatch")
	// ErrMailUnavailable: the instance has no outgoing mail settings, so
	// no e-mail channel can be created (spec §5: "the kind is unavailable
	// and the UI says why").
	ErrMailUnavailable = errors.New("this instance has no outgoing mail settings; an instance administrator sets them under Settings → Outgoing mail")
)

// Kind is a channel's delivery mechanism. v0.4 adds more.
type Kind string

const (
	KindEmail   Kind = "email"
	KindWebhook Kind = "webhook"
)

// Channel is one stored channel. Config is the storage form: for webhooks
// the URL, every header value and the signing secret are sealed with
// AAD(OrgID, ID); View is the secret-free reading of it. BodyJSON is a
// webhook channel's effective Content-Type classification (unsealed — a
// media type is not a secret — and kept outside Config so an update that
// only resubmits the body template can still classify a kept Content-Type
// header without unsealing it); unused for an e-mail channel.
type Channel struct {
	ID, OrgID uuid.UUID
	Kind      Kind
	// Preset names the chat service a webhook channel renders its body
	// for (backlog 4.7): "" is the raw webhook (the body template or the
	// event's JSON), anything else a key of the wired Presets. Always ""
	// for an e-mail channel (the schema's own CHECK).
	Preset               string
	Name                 string
	Enabled              bool
	Config               json.RawMessage
	BodyJSON             bool
	CreatedBy            *uuid.UUID
	CreatedAt, UpdatedAt time.Time
	Revision             int64
}

// EmailConfig is an e-mail channel's settings (nothing in it is secret).
type EmailConfig struct {
	Recipients []string `json:"recipients"`
}

// webhookConfig is a webhook channel's stored settings. BodyTemplate is
// not secret and is never sealed: it is stored and returned in clear to
// any reader with channels.read, the same as the method and the URL's
// display form. Never put a token, password or other credential in it —
// put those in a header instead, where the value is sealed.
type webhookConfig struct {
	Method        string         `json:"method"`
	URLSealed     []byte         `json:"url_sealed"`
	URLDisplay    string         `json:"url_display"`
	Headers       []storedHeader `json:"headers,omitempty"`
	BodyTemplate  string         `json:"body_template,omitempty"`
	SigningSealed []byte         `json:"signing_sealed,omitempty"`
	// PresetSettings are a preset channel's settings, in clear (chat_id,
	// account_sid, from, to, region).
	PresetSettings
	// Secrets are a Credentialed preset's secrets (backlog 4.8), each
	// value sealed like a header value.
	Secrets []storedHeader `json:"secrets,omitempty"`
}

type storedHeader struct {
	Name        string `json:"name"`
	ValueSealed []byte `json:"value_sealed"`
}

// WebhookView is what a reader sees of a webhook channel: the URL only as
// scheme and host (a path can carry a token, as Slack's do), header names
// without values, and whether requests are signed. BodyTemplate, unlike
// every other field here, is not redacted: it is not secret, so it comes
// back in full — an admin who put a token or credential in it by mistake
// (instead of in a sealed header) has exposed it to every channels.read
// reader, not just other admins.
type WebhookView struct {
	Method       string
	URLDisplay   string
	HeaderNames  []string
	BodyTemplate string
	Signed       bool
	// Preset is the channel's preset ("" raw), PresetSettings its
	// settings, SecretNames the names of its sealed secrets.
	Preset string
	PresetSettings
	SecretNames []string
}

// View decodes the channel's settings into their secret-free form: exactly
// one of the two results is set, matching Kind.
func (c Channel) View() (*EmailConfig, *WebhookView, error) {
	switch c.Kind {
	case KindEmail:
		var e EmailConfig
		if err := json.Unmarshal(c.Config, &e); err != nil {
			return nil, nil, fmt.Errorf("decode e-mail channel: %w", err)
		}
		return &e, nil, nil
	case KindWebhook:
		var w webhookConfig
		if err := json.Unmarshal(c.Config, &w); err != nil {
			return nil, nil, fmt.Errorf("decode webhook channel: %w", err)
		}
		v := &WebhookView{
			Method: w.Method, URLDisplay: w.URLDisplay, BodyTemplate: w.BodyTemplate, Signed: len(w.SigningSealed) > 0,
			Preset: c.Preset, PresetSettings: w.PresetSettings,
		}
		for _, h := range w.Headers {
			v.HeaderNames = append(v.HeaderNames, h.Name)
		}
		for _, sec := range w.Secrets {
			v.SecretNames = append(v.SecretNames, sec.Name)
		}
		return nil, v, nil
	}
	return nil, nil, fmt.Errorf("unknown channel kind %q", c.Kind)
}

// HeaderInput is one webhook header as submitted. A nil Value on update
// keeps the stored value of the header with the same name.
type HeaderInput struct {
	Name  string
	Value *string
}

// WebhookInput is a webhook channel as submitted. A nil URL or
// SigningSecret on update keeps the stored one. BodyTemplate is not
// secret (see webhookConfig): it is always resubmitted in full and always
// returned in full by View — do not put a token or credential in it.
type WebhookInput struct {
	Method        string
	URL           *string
	Headers       []HeaderInput
	BodyTemplate  string
	Signed        bool
	SigningSecret *string
	// Preset names the chat service the body is rendered for ("" raw);
	// with one, BodyTemplate must be empty (the preset owns the body) and
	// changing it needs URL again (unless the preset builds its own).
	// ChatID is Telegram's chat; AccountSID, From and To Twilio's; Region
	// PagerDuty's. Secrets are a Credentialed preset's secrets by name: one
	// left out keeps its stored value while the preset stays the same.
	Preset     string
	ChatID     string
	AccountSID string
	From, To   string
	Region     string
	Secrets    map[string]string
}

// settings are the input's preset settings.
func (in WebhookInput) settings() PresetSettings {
	return PresetSettings{ChatID: in.ChatID, AccountSID: in.AccountSID, From: in.From, To: in.To, Region: in.Region}
}

// Input is what an operator submits for create and update; exactly one
// of Email and Webhook matches Kind.
type Input struct {
	Kind    Kind
	Name    string
	Enabled bool
	Email   *EmailConfig
	Webhook *WebhookInput
}

// AAD is the associated data a channel's secrets are sealed with.
func AAD(orgID, id uuid.UUID) []byte {
	return []byte("channel:" + orgID.String() + ":" + id.String())
}
