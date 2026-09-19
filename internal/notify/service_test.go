package notify_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/notify"
)

var ctx = context.Background()

// fakeBox "seals" by prefixing the AAD, so tests can see what was sealed
// with which associated data, and refuses to open with the wrong one.
type fakeBox struct{}

func (fakeBox) SealAAD(plain, aad []byte) ([]byte, error) {
	return append(append(append([]byte{}, aad...), '|'), plain...), nil
}

func (fakeBox) OpenAAD(sealed, aad []byte) ([]byte, error) {
	prefix := append(append([]byte{}, aad...), '|')
	if !bytes.HasPrefix(sealed, prefix) {
		return nil, errors.New("wrong aad")
	}
	return sealed[len(prefix):], nil
}

type fakeMail struct {
	mu         sync.Mutex
	configured bool
	sent       [][]string
	err        error
}

func (m *fakeMail) SendMany(_ context.Context, to []string, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, append(append([]string{}, to...), subject, body))
	return nil
}

func (m *fakeMail) Configured(context.Context) (bool, error) { return m.configured, nil }

type fakeChannels struct {
	mu   sync.Mutex
	byID map[uuid.UUID]notify.Channel
}

func newFakeChannels() *fakeChannels { return &fakeChannels{byID: map[uuid.UUID]notify.Channel{}} }

func (f *fakeChannels) CreateChannel(_ context.Context, c notify.Channel) (notify.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.byID {
		if o.OrgID == c.OrgID && o.Name == c.Name {
			return notify.Channel{}, notify.ErrNameTaken
		}
	}
	c.Revision = 1
	f.byID[c.ID] = c
	return c, nil
}

func (f *fakeChannels) GetChannel(_ context.Context, orgID, id uuid.UUID) (notify.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byID[id]
	if !ok || c.OrgID != orgID {
		return notify.Channel{}, notify.ErrNotFound
	}
	return c, nil
}

func (f *fakeChannels) ListChannels(_ context.Context, orgID uuid.UUID) ([]notify.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []notify.Channel
	for _, c := range f.byID {
		if c.OrgID == orgID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeChannels) UpdateChannel(_ context.Context, c notify.Channel, revision int64) (notify.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.byID[c.ID]
	if !ok {
		return notify.Channel{}, notify.ErrNotFound
	}
	if cur.Revision != revision {
		return notify.Channel{}, notify.ErrRevisionMismatch
	}
	c.Revision = revision + 1
	f.byID[c.ID] = c
	return c, nil
}

func (f *fakeChannels) DeleteChannel(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.byID[id]; !ok || c.OrgID != orgID {
		return notify.ErrNotFound
	}
	delete(f.byID, id)
	return nil
}

type world struct {
	svc      *notify.Service
	channels *fakeChannels
	mail     *fakeMail
	org      uuid.UUID
	actor    uuid.UUID
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{channels: newFakeChannels(), mail: &fakeMail{configured: true}, org: uuid.Must(uuid.NewV7()), actor: uuid.Must(uuid.NewV7())}
	w.svc = notify.NewService(notify.Deps{
		Channels: w.channels, Box: fakeBox{}, Mail: w.mail,
		Now: func() time.Time { return time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC) }, Rand: rand.Reader,
	})
	return w
}

func str(s string) *string { return &s }

func webhookInput(name string) notify.Input {
	return notify.Input{Kind: notify.KindWebhook, Name: name, Enabled: true, Webhook: &notify.WebhookInput{
		URL:           str("https://hooks.example.test/services/T0/B0/xyz"),
		Headers:       []notify.HeaderInput{{Name: "Authorization", Value: str("Bearer t0ken")}},
		BodyTemplate:  `{"text": {{ schedule.name | json }}}`,
		Signed:        true,
		SigningSecret: str("0123456789abcdef0123"),
	}}
}

func TestCreateEmailChannel(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, notify.Input{Kind: notify.KindEmail, Name: "ops mail", Enabled: true, Email: &notify.EmailConfig{Recipients: []string{"ops@example.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	email, hook, err := c.View()
	if err != nil || hook != nil || email.Recipients[0] != "ops@example.test" || *c.CreatedBy != w.actor {
		t.Fatalf("view = %+v %+v %v", email, hook, err)
	}
	w.mail.configured = false
	if _, err := w.svc.CreateChannel(ctx, w.org, w.actor, notify.Input{Kind: notify.KindEmail, Name: "second", Email: &notify.EmailConfig{Recipients: []string{"a@example.test"}}}); !errors.Is(err, notify.ErrMailUnavailable) {
		t.Fatalf("e-mail channel without instance mail = %v, want ErrMailUnavailable", err)
	}
	if ok, _ := w.svc.MailAvailable(ctx); ok {
		t.Fatal("MailAvailable must follow the instance settings")
	}
}

func TestCreateWebhookChannelSealsEverySecret(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, webhookInput("ops hook"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"services/T0/B0/xyz", "Bearer t0ken", "0123456789abcdef0123"} {
		if bytes.Contains(c.Config, []byte(secret)) {
			t.Fatalf("stored config carries %q in the clear: %s", secret, c.Config)
		}
	}
	var stored struct {
		URLSealed []byte `json:"url_sealed"`
	}
	if err := json.Unmarshal(c.Config, &stored); err != nil {
		t.Fatal(err)
	}
	if url, err := (fakeBox{}).OpenAAD(stored.URLSealed, notify.AAD(w.org, c.ID)); err != nil || string(url) != "https://hooks.example.test/services/T0/B0/xyz" {
		t.Fatalf("the url is not sealed with the channel's AAD: %q, %v", url, err)
	}
	_, hook, err := c.View()
	if err != nil || hook.URLDisplay != "https://hooks.example.test/…" || hook.Method != "POST" || !hook.Signed ||
		len(hook.HeaderNames) != 1 || hook.HeaderNames[0] != "Authorization" {
		t.Fatalf("view = %+v, %v", hook, err)
	}
}

// sealedWebhookConfig mirrors the JSON shape of the package-private
// webhookConfig, so a black-box test can open the values a kept secret
// round-trips to instead of trusting the display fields alone.
type sealedWebhookConfig struct {
	URLSealed []byte `json:"url_sealed"`
	Headers   []struct {
		Name        string `json:"name"`
		ValueSealed []byte `json:"value_sealed"`
	} `json:"headers"`
	SigningSealed []byte `json:"signing_sealed"`
}

func TestUpdateWebhookKeepsSecretsLeftOut(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, webhookInput("ops hook"))
	if err != nil {
		t.Fatal(err)
	}
	var created sealedWebhookConfig
	if err := json.Unmarshal(c.Config, &created); err != nil {
		t.Fatal(err)
	}

	in := webhookInput("ops hook renamed")
	in.Webhook.URL, in.Webhook.SigningSecret = nil, nil
	in.Webhook.Headers = []notify.HeaderInput{{Name: "authorization"}}
	up, err := w.svc.UpdateChannel(ctx, w.org, c.ID, c.Revision, in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(up.Config, []byte(`"url_display":"https://hooks.example.test/…"`)) {
		t.Fatalf("url not kept: %s", up.Config)
	}
	_, hook, _ := up.View()
	if !hook.Signed || hook.HeaderNames[0] != "authorization" || up.Name != "ops hook renamed" {
		t.Fatalf("updated view = %+v", hook)
	}

	// The kept secrets are not just display-equal: they are the exact
	// same sealed bytes as at creation, and open to the original
	// plaintext under the channel's AAD (review Minor 2 — a regression
	// that kept the display but dropped a sealed value must fail this).
	var kept sealedWebhookConfig
	if err := json.Unmarshal(up.Config, &kept); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kept.URLSealed, created.URLSealed) {
		t.Fatalf("url_sealed changed on an update that left the url out")
	}
	if !bytes.Equal(kept.SigningSealed, created.SigningSealed) {
		t.Fatalf("signing_sealed changed on an update that left the secret out")
	}
	var keptHeaderSealed []byte
	for _, h := range kept.Headers {
		if strings.EqualFold(h.Name, "authorization") {
			keptHeaderSealed = h.ValueSealed
		}
	}
	if url, err := (fakeBox{}).OpenAAD(kept.URLSealed, notify.AAD(w.org, c.ID)); err != nil || string(url) != "https://hooks.example.test/services/T0/B0/xyz" {
		t.Fatalf("kept url = %q, %v", url, err)
	}
	if val, err := (fakeBox{}).OpenAAD(keptHeaderSealed, notify.AAD(w.org, c.ID)); err != nil || string(val) != "Bearer t0ken" {
		t.Fatalf("kept header value = %q, %v", val, err)
	}
	if secret, err := (fakeBox{}).OpenAAD(kept.SigningSealed, notify.AAD(w.org, c.ID)); err != nil || string(secret) != "0123456789abcdef0123" {
		t.Fatalf("kept signing secret = %q, %v", secret, err)
	}

	// A new header without a value, a kind change and a stale revision
	// are refused.
	in.Webhook.Headers = []notify.HeaderInput{{Name: "X-New"}}
	if _, err := w.svc.UpdateChannel(ctx, w.org, c.ID, up.Revision, in); !errors.Is(err, notify.ErrInvalidInput) {
		t.Fatalf("header without value = %v", err)
	}
	in.Kind = notify.KindEmail
	if _, err := w.svc.UpdateChannel(ctx, w.org, c.ID, up.Revision, in); !errors.Is(err, notify.ErrInvalidInput) {
		t.Fatalf("kind change = %v", err)
	}
	if _, err := w.svc.UpdateChannel(ctx, w.org, c.ID, c.Revision, webhookInput("x")); !errors.Is(err, notify.ErrRevisionMismatch) {
		t.Fatalf("stale revision = %v", err)
	}

	// Signing without any secret is refused: an unsigned channel turning
	// signing on must supply one, keeping is not an option because there
	// is nothing stored to keep.
	unsigned := webhookInput("plain hook")
	unsigned.Webhook.Signed, unsigned.Webhook.SigningSecret = false, nil
	plain, err := w.svc.CreateChannel(ctx, w.org, w.actor, unsigned)
	if err != nil {
		t.Fatal(err)
	}
	sign := webhookInput("plain hook")
	sign.Webhook.SigningSecret = nil
	if _, err := w.svc.UpdateChannel(ctx, w.org, plain.ID, plain.Revision, sign); !errors.Is(err, notify.ErrInvalidInput) {
		t.Fatalf("signing without any secret = %v, want ErrInvalidInput", err)
	}
}

func TestWebhookInputRefusals(t *testing.T) {
	w := newWorld(t)
	cases := map[string]func(in *notify.Input){
		"no url":  func(in *notify.Input) { in.Webhook.URL = nil },
		"bad url": func(in *notify.Input) { in.Webhook.URL = str("ftp://x.test") },
		"header twice": func(in *notify.Input) {
			in.Webhook.Headers = append(in.Webhook.Headers, notify.HeaderInput{Name: "AUTHORIZATION", Value: str("x")})
		},
		"reserved header": func(in *notify.Input) {
			in.Webhook.Headers = []notify.HeaderInput{{Name: "X-Pulsix-Event", Value: str("x")}}
		},
		"broken template":     func(in *notify.Input) { in.Webhook.BodyTemplate = "{{ nope }}" },
		"signed, no secret":   func(in *notify.Input) { in.Webhook.SigningSecret = nil },
		"unsigned but secret": func(in *notify.Input) { in.Webhook.Signed = false },
		"short secret":        func(in *notify.Input) { in.Webhook.SigningSecret = str("short") },
		"email settings too":  func(in *notify.Input) { in.Email = &notify.EmailConfig{} },
		"bad method":          func(in *notify.Input) { in.Webhook.Method = "DELETE" },
		"bad name":            func(in *notify.Input) { in.Name = " " },
		"unknown kind":        func(in *notify.Input) { in.Kind = "sms" },
	}
	for name, mutate := range cases {
		in := webhookInput("hook " + name)
		mutate(&in)
		if _, err := w.svc.CreateChannel(ctx, w.org, w.actor, in); !errors.Is(err, notify.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

// TestEffectiveContentTypeDrivesJSONValidation is the fix for the
// re-review's "New Breakage" finding: save-time JSON validation must use
// the channel's *effective* Content-Type — this request's own header,
// else a kept header's stored classification, else JSON (the default
// every sender in this codebase uses when no header is set) — not just
// "did this request happen to send a Content-Type header".
func TestEffectiveContentTypeDrivesJSONValidation(t *testing.T) {
	w := newWorld(t)
	const invalidJSON = `{"n": {{ schedule.name }}}` // unquoted: schedule.name has a space

	noHeader := webhookInput("no content type")
	noHeader.Webhook.BodyTemplate = invalidJSON
	if _, err := w.svc.CreateChannel(ctx, w.org, w.actor, noHeader); !errors.Is(err, notify.ErrInvalidInput) {
		t.Fatalf("no Content-Type header with an invalid-JSON body = %v, want ErrInvalidInput (JSON is the default)", err)
	}

	textPlain := webhookInput("text plain")
	textPlain.Webhook.Headers = append(textPlain.Webhook.Headers, notify.HeaderInput{Name: "Content-Type", Value: str("text/plain")})
	textPlain.Webhook.BodyTemplate = invalidJSON
	if _, err := w.svc.CreateChannel(ctx, w.org, w.actor, textPlain); err != nil {
		t.Fatalf("an explicit non-JSON Content-Type must not be JSON-validated: %v", err)
	}

	jsonHeader := webhookInput("kept json header")
	jsonHeader.Webhook.Headers = append(jsonHeader.Webhook.Headers, notify.HeaderInput{Name: "Content-Type", Value: str("application/json")})
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, jsonHeader)
	if err != nil {
		t.Fatal(err)
	}
	update := webhookInput("kept json header")
	update.Webhook.URL, update.Webhook.SigningSecret = nil, nil
	update.Webhook.Headers = []notify.HeaderInput{{Name: "Authorization"}, {Name: "Content-Type"}}
	update.Webhook.BodyTemplate = invalidJSON
	if _, err := w.svc.UpdateChannel(ctx, w.org, c.ID, c.Revision, update); !errors.Is(err, notify.ErrInvalidInput) {
		t.Fatalf("a kept application/json header with a body-only update = %v, want ErrInvalidInput", err)
	}
}

func TestChannelListGetDelete(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, webhookInput("ops hook"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.CreateChannel(ctx, w.org, w.actor, webhookInput("ops hook")); !errors.Is(err, notify.ErrNameTaken) {
		t.Fatalf("duplicate name = %v", err)
	}
	if list, err := w.svc.ListChannels(ctx, w.org); err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if got, err := w.svc.GetChannel(ctx, w.org, c.ID); err != nil || got.ID != c.ID {
		t.Fatalf("get = %v", err)
	}
	if err := w.svc.DeleteChannel(ctx, w.org, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.GetChannel(ctx, w.org, c.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("after delete = %v", err)
	}
}
