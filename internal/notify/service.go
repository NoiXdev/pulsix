package notify

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ChannelStore persists channels (org-scoped).
type ChannelStore interface {
	CreateChannel(ctx context.Context, c Channel) (Channel, error)
	GetChannel(ctx context.Context, orgID, id uuid.UUID) (Channel, error)
	ListChannels(ctx context.Context, orgID uuid.UUID) ([]Channel, error)
	UpdateChannel(ctx context.Context, c Channel, revision int64) (Channel, error)
	DeleteChannel(ctx context.Context, orgID, id uuid.UUID) error
}

// Sealer seals and opens channel secrets (crypto.Box).
type Sealer interface {
	SealAAD(plain, aad []byte) ([]byte, error)
	OpenAAD(sealed, aad []byte) ([]byte, error)
}

// Mailer is the instance SMTP (mail.SMTP).
type Mailer interface {
	SendMany(ctx context.Context, to []string, subject, body string) error
	Configured(ctx context.Context) (bool, error)
}

// Deps wires the service. Now, Rand and Log default when nil.
type Deps struct {
	Channels   ChannelStore
	Deliveries DeliveryLog
	Rules      RuleStore
	Box        Sealer
	Mail       Mailer
	// Sender delivers "Send test" synchronously (ChannelSender).
	Sender Sender
	// PublicURL is the instance's base URL, for the links in messages.
	PublicURL string
	Now       func() time.Time
	Rand      io.Reader
	Log       *slog.Logger
}

// Service manages channels.
type Service struct{ d Deps }

// NewService wires the service.
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Rand == nil {
		d.Rand = rand.Reader
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d}
}

func (s *Service) newID() uuid.UUID {
	id, err := uuid.NewV7FromReader(s.d.Rand)
	if err != nil {
		panic(err) //nolint:forbidigo // entropy failure is unrecoverable, like uuid.Must
	}
	return id
}

// MailAvailable reports whether e-mail channels can be used on this
// instance (it has outgoing mail settings).
func (s *Service) MailAvailable(ctx context.Context) (bool, error) {
	return s.d.Mail.Configured(ctx)
}

// CreateChannel validates, seals and stores a new channel.
func (s *Service) CreateChannel(ctx context.Context, orgID, actor uuid.UUID, in Input) (Channel, error) {
	id := s.newID()
	name, err := normalizeName(in.Name)
	if err != nil {
		return Channel{}, err
	}
	if in.Kind == KindEmail {
		ok, err := s.d.Mail.Configured(ctx)
		if err != nil {
			return Channel{}, fmt.Errorf("check outgoing mail: %w", err)
		}
		if !ok {
			return Channel{}, ErrMailUnavailable
		}
	}
	cfg, bodyJSON, err := s.buildConfig(orgID, id, in, nil)
	if err != nil {
		return Channel{}, err
	}
	return s.d.Channels.CreateChannel(ctx, Channel{
		ID: id, OrgID: orgID, Kind: in.Kind, Name: name, Enabled: in.Enabled, Config: cfg, BodyJSON: bodyJSON, CreatedBy: &actor,
	})
}

// UpdateChannel replaces a channel's settings when revision is current.
// The kind never changes; secrets submitted as nil keep their stored value.
func (s *Service) UpdateChannel(ctx context.Context, orgID, id uuid.UUID, revision int64, in Input) (Channel, error) {
	cur, err := s.d.Channels.GetChannel(ctx, orgID, id)
	if err != nil {
		return Channel{}, err
	}
	if cur.Revision != revision {
		return Channel{}, ErrRevisionMismatch
	}
	if in.Kind != cur.Kind {
		return Channel{}, fmt.Errorf("%w: the kind of a channel cannot change; create a new channel instead", ErrInvalidInput)
	}
	name, err := normalizeName(in.Name)
	if err != nil {
		return Channel{}, err
	}
	cfg, bodyJSON, err := s.buildConfig(orgID, id, in, &cur)
	if err != nil {
		return Channel{}, err
	}
	cur.Name, cur.Enabled, cur.Config, cur.BodyJSON = name, in.Enabled, cfg, bodyJSON
	return s.d.Channels.UpdateChannel(ctx, cur, revision)
}

func (s *Service) GetChannel(ctx context.Context, orgID, id uuid.UUID) (Channel, error) {
	return s.d.Channels.GetChannel(ctx, orgID, id)
}

func (s *Service) ListChannels(ctx context.Context, orgID uuid.UUID) ([]Channel, error) {
	return s.d.Channels.ListChannels(ctx, orgID)
}

// DeleteChannel removes a channel; its rules, digest membership and
// delivery log go with it (foreign key cascades).
func (s *Service) DeleteChannel(ctx context.Context, orgID, id uuid.UUID) error {
	return s.d.Channels.DeleteChannel(ctx, orgID, id)
}

// buildConfig turns an input into the stored config, plus the channel's
// effective Content-Type classification (unused for KindEmail; true is
// returned as a harmless, unread default). cur is the channel being
// updated (nil on create): its sealed values, and its stored
// classification for a kept Content-Type header, are kept where the
// input leaves them out.
func (s *Service) buildConfig(orgID, id uuid.UUID, in Input, cur *Channel) (json.RawMessage, bool, error) {
	switch in.Kind {
	case KindEmail:
		if in.Webhook != nil {
			return nil, false, fmt.Errorf("%w: an e-mail channel takes no webhook settings", ErrInvalidInput)
		}
		e, err := normalizeEmail(in.Email)
		if err != nil {
			return nil, false, err
		}
		cfg, err := json.Marshal(e)
		return cfg, true, err
	case KindWebhook:
		if in.Email != nil || in.Webhook == nil {
			return nil, false, fmt.Errorf("%w: a webhook channel takes webhook settings only", ErrInvalidInput)
		}
		var stored webhookConfig
		keptBodyJSON := true
		if cur != nil {
			if err := json.Unmarshal(cur.Config, &stored); err != nil {
				return nil, false, fmt.Errorf("decode stored webhook channel: %w", err)
			}
			keptBodyJSON = cur.BodyJSON
		}
		w, bodyJSON, err := s.buildWebhook(AAD(orgID, id), *in.Webhook, stored, cur != nil, keptBodyJSON)
		if err != nil {
			return nil, false, err
		}
		cfg, err := json.Marshal(w)
		return cfg, bodyJSON, err
	}
	return nil, false, fmt.Errorf("%w: kind must be email or webhook", ErrInvalidInput)
}

func (s *Service) buildWebhook(aad []byte, in WebhookInput, stored webhookConfig, update, keptBodyJSON bool) (webhookConfig, bool, error) {
	var out webhookConfig
	var err error
	if out.Method, err = normalizeMethod(in.Method); err != nil {
		return out, false, err
	}
	switch {
	case in.URL != nil:
		if out.URLDisplay, err = checkURL(*in.URL); err != nil {
			return out, false, err
		}
		if out.URLSealed, err = s.d.Box.SealAAD([]byte(strings.TrimSpace(*in.URL)), aad); err != nil {
			return out, false, fmt.Errorf("seal webhook url: %w", err)
		}
	case update:
		out.URLSealed, out.URLDisplay = stored.URLSealed, stored.URLDisplay
	default:
		return out, false, fmt.Errorf("%w: a webhook channel needs a url", ErrInvalidInput)
	}
	if len(in.Headers) > maxHeaders {
		return out, false, fmt.Errorf("%w: at most %d headers", ErrInvalidInput, maxHeaders)
	}
	seen := map[string]bool{}
	for _, h := range in.Headers {
		if err := checkHeader(h.Name, h.Value); err != nil {
			return out, false, err
		}
		key := strings.ToLower(h.Name)
		if seen[key] {
			return out, false, fmt.Errorf("%w: header %s is listed twice", ErrInvalidInput, h.Name)
		}
		seen[key] = true
		sh := storedHeader{Name: h.Name}
		if h.Value != nil {
			if sh.ValueSealed, err = s.d.Box.SealAAD([]byte(*h.Value), aad); err != nil {
				return out, false, fmt.Errorf("seal header value: %w", err)
			}
		} else {
			kept := false
			for _, old := range stored.Headers {
				if strings.EqualFold(old.Name, h.Name) {
					sh.ValueSealed, kept = old.ValueSealed, true
				}
			}
			if !kept {
				return out, false, fmt.Errorf("%w: header %s needs a value", ErrInvalidInput, h.Name)
			}
		}
		out.Headers = append(out.Headers, sh)
	}
	bodyJSON := resolveBodyJSON(in.Headers, keptBodyJSON)
	if err := checkBodyTemplate(in.BodyTemplate, bodyJSON); err != nil {
		return out, false, err
	}
	out.BodyTemplate = in.BodyTemplate
	switch {
	case in.Signed && in.SigningSecret != nil:
		if err := checkSigningSecret(*in.SigningSecret); err != nil {
			return out, false, err
		}
		if out.SigningSealed, err = s.d.Box.SealAAD([]byte(*in.SigningSecret), aad); err != nil {
			return out, false, fmt.Errorf("seal signing secret: %w", err)
		}
	case in.Signed && len(stored.SigningSealed) > 0:
		out.SigningSealed = stored.SigningSealed
	case in.Signed:
		return out, false, fmt.Errorf("%w: signed requests need a signing secret", ErrInvalidInput)
	case in.SigningSecret != nil:
		return out, false, fmt.Errorf("%w: a signing secret was given but signed is false", ErrInvalidInput)
	}
	return out, bodyJSON, nil
}

// resolveBodyJSON classifies a webhook channel's *effective* Content-Type
// at save time, so save-time JSON validation (checkBodyTemplate) applies
// to the type the channel will actually be sent as, not just to whichever
// request happened to mention it:
//   - a Content-Type header supplied fresh in this request: classify that
//     value with isJSONMediaType;
//   - a Content-Type header kept (listed with no value, matching a stored
//     header by name): keptBodyJSON, the stored channel's own
//     classification from when that header's value was last set;
//   - no Content-Type header at all: true. Every sender in this codebase
//     (mail.SMTP does not apply; the webhook dispatcher, Task 3's
//     sendWebhook) sets "Content-Type: application/json" by default
//     before applying operator headers, so this is the transport default,
//     not a guess.
func resolveBodyJSON(headers []HeaderInput, keptBodyJSON bool) bool {
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Content-Type") {
			if h.Value != nil {
				return isJSONMediaType(*h.Value)
			}
			return keptBodyJSON
		}
	}
	return true
}

// isJSONMediaType parses a Content-Type-style value and reports whether
// it names a JSON media type: application/json, or a type with a "+json"
// structured-syntax suffix (RFC 6839, e.g. application/vnd.api+json). A
// value that fails to parse, or that names some other type such as
// application/x-ndjson or text/plain, is not JSON — this is a real media
// type match, not a substring test, so a "profile=json" parameter or an
// "ndjson" type name do not count.
func isJSONMediaType(value string) bool {
	mt, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}
