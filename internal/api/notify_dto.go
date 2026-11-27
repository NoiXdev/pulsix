package api

import (
	"time"

	"github.com/noixdev/pulsix/internal/notify"
)

type notifyEmailDTO struct {
	Recipients []string `json:"recipients" maxItems:"20" doc:"Plain addresses; every recipient gets the same message"`
}

type notifyWebhookDTO struct {
	Method       string   `json:"method" enum:"POST,PUT"`
	URL          string   `json:"url" doc:"Scheme and host only: a path can carry a token and is never returned"`
	Headers      []string `json:"headers" doc:"Header names; values are never returned"`
	BodyTemplate string   `json:"body_template,omitempty"`
	Signed       bool     `json:"signed" doc:"Requests carry X-Pulsix-Signature-256"`
	Preset       string   `json:"preset" enum:",slack,mattermost,telegram,ntfy,pushover,twilio,pagerduty" doc:"The service the message is rendered for; empty is a raw webhook"`
	ChatID       string   `json:"chat_id,omitempty" doc:"Telegram preset only: the chat the bot posts to"`
	AccountSID   string   `json:"account_sid,omitempty" doc:"Twilio preset only: the account SID"`
	From         string   `json:"from,omitempty" doc:"Twilio preset only: the sender number or alphanumeric sender ID"`
	To           string   `json:"to,omitempty" doc:"Twilio preset only: the recipient's number"`
	Region       string   `json:"region,omitempty" enum:",us,eu" doc:"PagerDuty preset only: the account's service region"`
	Secrets      []string `json:"secrets,omitempty" doc:"The names of the preset's stored secrets; values are never returned"`
}

type notifyChannelDTO struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind" enum:"email,webhook"`
	Name      string            `json:"name"`
	Enabled   bool              `json:"enabled"`
	Email     *notifyEmailDTO   `json:"email,omitempty"`
	Webhook   *notifyWebhookDTO `json:"webhook,omitempty"`
	Revision  int64             `json:"revision"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type notifyHeaderBody struct {
	Name  string  `json:"name" minLength:"1" maxLength:"100"`
	Value *string `json:"value,omitempty" maxLength:"4096" doc:"Omit on update to keep the stored value"`
}

type notifyWebhookBody struct {
	Method        string             `json:"method,omitempty" enum:"POST,PUT" doc:"Default POST"`
	URL           *string            `json:"url,omitempty" maxLength:"2048" doc:"Required on create; omit on update to keep the stored URL"`
	Headers       []notifyHeaderBody `json:"headers,omitempty" maxItems:"20"`
	BodyTemplate  string             `json:"body_template,omitempty" maxLength:"16384" doc:"Empty sends the event as JSON"`
	Signed        bool               `json:"signed,omitempty"`
	SigningSecret *string            `json:"signing_secret,omitempty" minLength:"16" maxLength:"256" doc:"Omit on update to keep the stored secret"`
	Preset        string             `json:"preset,omitempty" enum:",slack,mattermost,telegram,ntfy,pushover,twilio,pagerduty" doc:"A service whose message Pulsix renders (the body template must then be empty, the method POST); changing it needs the url again, except for pushover, twilio and pagerduty, whose url Pulsix sets (leave it out)"`
	ChatID        string             `json:"chat_id,omitempty" maxLength:"64" doc:"Telegram preset only: a chat id such as -1001234567890, or a public @username"`
	AccountSID    string             `json:"account_sid,omitempty" maxLength:"64" doc:"Twilio preset only: the account SID (AC followed by 32 hexadecimal digits)"`
	From          string             `json:"from,omitempty" maxLength:"32" doc:"Twilio preset only: the sender, a number in international format (+15005550006) or an alphanumeric sender ID"`
	To            string             `json:"to,omitempty" maxLength:"32" doc:"Twilio preset only: one recipient number in international format"`
	Region        string             `json:"region,omitempty" enum:",us,eu" doc:"PagerDuty preset only: the account's service region, default us"`
	Secrets       map[string]string  `json:"secrets,omitempty" doc:"The preset's secrets by name: pushover token and user, twilio auth_token, pagerduty routing_key. Omit one on update to keep the stored value"`
}

// NotifyChannelBody is the create/update body (exported so huma names
// the schema after it).
type NotifyChannelBody struct {
	Kind    string             `json:"kind" enum:"email,webhook"`
	Name    string             `json:"name" minLength:"1" maxLength:"100"`
	Enabled *bool              `json:"enabled,omitempty" doc:"Default true"`
	Email   *notifyEmailDTO    `json:"email,omitempty"`
	Webhook *notifyWebhookBody `json:"webhook,omitempty"`
}

type notifyDeliveryDTO struct {
	ID            string     `json:"id"`
	EventKind     string     `json:"event_kind" enum:"run.finished,schedule.skipped,schedule.failed,digest.weekly,test"`
	Status        string     `json:"status" enum:"pending,sent,failed"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty" doc:"Pending only: when the next attempt is due"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	SentAt        *time.Time `json:"sent_at,omitempty"`
}

func (b NotifyChannelBody) input() notify.Input {
	in := notify.Input{Kind: notify.Kind(b.Kind), Name: b.Name, Enabled: b.Enabled == nil || *b.Enabled}
	if b.Email != nil {
		in.Email = &notify.EmailConfig{Recipients: b.Email.Recipients}
	}
	if w := b.Webhook; w != nil {
		in.Webhook = &notify.WebhookInput{
			Method: w.Method, URL: w.URL, BodyTemplate: w.BodyTemplate, Signed: w.Signed, SigningSecret: w.SigningSecret,
			Preset: w.Preset, ChatID: w.ChatID, AccountSID: w.AccountSID, From: w.From, To: w.To, Region: w.Region, Secrets: w.Secrets,
		}
		for _, h := range w.Headers {
			in.Webhook.Headers = append(in.Webhook.Headers, notify.HeaderInput{Name: h.Name, Value: h.Value})
		}
	}
	return in
}

func toNotifyChannelDTO(c notify.Channel) (notifyChannelDTO, error) {
	dto := notifyChannelDTO{
		ID: c.ID.String(), Kind: string(c.Kind), Name: c.Name, Enabled: c.Enabled,
		Revision: c.Revision, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	email, hook, err := c.View()
	if err != nil {
		return notifyChannelDTO{}, err
	}
	if email != nil {
		dto.Email = &notifyEmailDTO{Recipients: email.Recipients}
	}
	if hook != nil {
		dto.Webhook = &notifyWebhookDTO{
			Method: hook.Method, URL: hook.URLDisplay, Headers: hook.HeaderNames, BodyTemplate: hook.BodyTemplate, Signed: hook.Signed,
			Preset: hook.Preset, ChatID: hook.ChatID, AccountSID: hook.AccountSID, From: hook.From, To: hook.To, Region: hook.Region,
			Secrets: hook.SecretNames,
		}
		if dto.Webhook.Headers == nil {
			dto.Webhook.Headers = []string{}
		}
	}
	return dto, nil
}

func toNotifyDeliveryDTO(d notify.Delivery) notifyDeliveryDTO {
	dto := notifyDeliveryDTO{
		ID: d.ID.String(), EventKind: string(d.EventKind), Status: string(d.Status), Attempts: d.Attempts,
		LastError: d.LastError, CreatedAt: d.CreatedAt, SentAt: d.SentAt,
	}
	if d.Status == notify.DeliveryPending {
		next := d.NextAttemptAt
		dto.NextAttemptAt = &next
	}
	return dto
}
