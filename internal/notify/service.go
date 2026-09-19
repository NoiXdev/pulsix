package notify

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	Channels ChannelStore
	Box      Sealer
	Mail     Mailer
	Now      func() time.Time
	Rand     io.Reader
	Log      *slog.Logger
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
	cfg, err := s.buildConfig(orgID, id, in, nil)
	if err != nil {
		return Channel{}, err
	}
	return s.d.Channels.CreateChannel(ctx, Channel{
		ID: id, OrgID: orgID, Kind: in.Kind, Name: name, Enabled: in.Enabled, Config: cfg, CreatedBy: &actor,
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
	cfg, err := s.buildConfig(orgID, id, in, &cur)
	if err != nil {
		return Channel{}, err
	}
	cur.Name, cur.Enabled, cur.Config = name, in.Enabled, cfg
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

// buildConfig turns an input into the stored config. cur is the channel
// being updated (nil on create): its sealed values are kept where the
// input leaves them out.
func (s *Service) buildConfig(orgID, id uuid.UUID, in Input, cur *Channel) (json.RawMessage, error) {
	switch in.Kind {
	case KindEmail:
		if in.Webhook != nil {
			return nil, fmt.Errorf("%w: an e-mail channel takes no webhook settings", ErrInvalidInput)
		}
		e, err := normalizeEmail(in.Email)
		if err != nil {
			return nil, err
		}
		return json.Marshal(e)
	case KindWebhook:
		if in.Email != nil || in.Webhook == nil {
			return nil, fmt.Errorf("%w: a webhook channel takes webhook settings only", ErrInvalidInput)
		}
		var stored webhookConfig
		if cur != nil {
			if err := json.Unmarshal(cur.Config, &stored); err != nil {
				return nil, fmt.Errorf("decode stored webhook channel: %w", err)
			}
		}
		w, err := s.buildWebhook(AAD(orgID, id), *in.Webhook, stored, cur != nil)
		if err != nil {
			return nil, err
		}
		return json.Marshal(w)
	}
	return nil, fmt.Errorf("%w: kind must be email or webhook", ErrInvalidInput)
}

func (s *Service) buildWebhook(aad []byte, in WebhookInput, stored webhookConfig, update bool) (webhookConfig, error) {
	var out webhookConfig
	var err error
	if out.Method, err = normalizeMethod(in.Method); err != nil {
		return out, err
	}
	switch {
	case in.URL != nil:
		if out.URLDisplay, err = checkURL(*in.URL); err != nil {
			return out, err
		}
		if out.URLSealed, err = s.d.Box.SealAAD([]byte(strings.TrimSpace(*in.URL)), aad); err != nil {
			return out, fmt.Errorf("seal webhook url: %w", err)
		}
	case update:
		out.URLSealed, out.URLDisplay = stored.URLSealed, stored.URLDisplay
	default:
		return out, fmt.Errorf("%w: a webhook channel needs a url", ErrInvalidInput)
	}
	if len(in.Headers) > maxHeaders {
		return out, fmt.Errorf("%w: at most %d headers", ErrInvalidInput, maxHeaders)
	}
	seen := map[string]bool{}
	for _, h := range in.Headers {
		if err := checkHeader(h.Name, h.Value); err != nil {
			return out, err
		}
		key := strings.ToLower(h.Name)
		if seen[key] {
			return out, fmt.Errorf("%w: header %s is listed twice", ErrInvalidInput, h.Name)
		}
		seen[key] = true
		sh := storedHeader{Name: h.Name}
		if h.Value != nil {
			if sh.ValueSealed, err = s.d.Box.SealAAD([]byte(*h.Value), aad); err != nil {
				return out, fmt.Errorf("seal header value: %w", err)
			}
		} else {
			kept := false
			for _, old := range stored.Headers {
				if strings.EqualFold(old.Name, h.Name) {
					sh.ValueSealed, kept = old.ValueSealed, true
				}
			}
			if !kept {
				return out, fmt.Errorf("%w: header %s needs a value", ErrInvalidInput, h.Name)
			}
		}
		out.Headers = append(out.Headers, sh)
	}
	if err := checkBodyTemplate(in.BodyTemplate); err != nil {
		return out, err
	}
	out.BodyTemplate = in.BodyTemplate
	if in.Signed {
		switch {
		case in.SigningSecret != nil:
			if err := checkSigningSecret(*in.SigningSecret); err != nil {
				return out, err
			}
			if out.SigningSealed, err = s.d.Box.SealAAD([]byte(*in.SigningSecret), aad); err != nil {
				return out, fmt.Errorf("seal signing secret: %w", err)
			}
		case len(stored.SigningSealed) > 0:
			out.SigningSealed = stored.SigningSealed
		default:
			return out, fmt.Errorf("%w: signed requests need a signing secret", ErrInvalidInput)
		}
	}
	return out, nil
}
