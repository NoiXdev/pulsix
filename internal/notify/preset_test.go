package notify_test

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/notify"
)

// fakePreset stands in for a chat service: its URL must end in /send, its
// chat id must be "room", and its body names the event kind and the chat.
type fakePreset struct{}

func (fakePreset) CheckURL(u *url.URL) error {
	if !strings.HasSuffix(u.Path, "/send") {
		return errors.New("the path must end in /send")
	}
	return nil
}

func (fakePreset) CheckSettings(s notify.PresetSettings) (notify.PresetSettings, error) {
	if s.ChatID != "room" {
		return s, errors.New("chat id must be room")
	}
	return s, nil
}

func (fakePreset) Render(ev notify.Event, s notify.PresetSettings) ([]byte, error) {
	return json.Marshal(map[string]string{"kind": string(ev.Kind), "chat": s.ChatID})
}

type openPreset struct{ fakePreset }

func (openPreset) CheckSettings(s notify.PresetSettings) (notify.PresetSettings, error) {
	return s, nil
}

var testPresets = notify.Presets{"fake": fakePreset{}, "open": openPreset{}}

func presetWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.svc = notify.NewService(notify.Deps{
		Channels: w.channels, Box: fakeBox{}, Mail: w.mail, Presets: testPresets,
		Now: func() time.Time { return time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC) }, Rand: rand.Reader,
	})
	return w
}

func presetInput(name, preset, url, chat string) notify.Input {
	in := notify.Input{Kind: notify.KindWebhook, Name: name, Enabled: true, Webhook: &notify.WebhookInput{Preset: preset, ChatID: chat}}
	if url != "" {
		in.Webhook.URL = &url
	}
	return in
}

func TestPresetCreateAndView(t *testing.T) {
	w := presetWorld(t)
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, presetInput("chat", "fake", "https://chat.example.test/bot123:secret/send", "room"))
	if err != nil {
		t.Fatal(err)
	}
	_, v, err := c.View()
	if err != nil || c.Preset != "fake" || v.Preset != "fake" || v.ChatID != "room" || v.Method != "POST" || v.BodyTemplate != "" ||
		v.URLDisplay != "https://chat.example.test/…" {
		t.Fatalf("channel %+v, view %+v, %v", c, v, err)
	}
	if strings.Contains(string(c.Config), "secret") {
		t.Fatalf("stored config holds the token in clear: %s", c.Config)
	}
}

func TestPresetValidation(t *testing.T) {
	put := func(in notify.Input) notify.Input { in.Webhook.Method = "PUT"; return in }
	tmpl := func(in notify.Input) notify.Input { in.Webhook.BodyTemplate = `{"a":1}`; return in }
	ctype := func(in notify.Input) notify.Input {
		in.Webhook.Headers = []notify.HeaderInput{{Name: "content-type", Value: str("text/plain")}}
		return in
	}
	good := "https://chat.example.test/bot123:s3cr3t-token/send"
	cases := []struct {
		name string
		in   notify.Input
		want string
	}{
		{"unknown preset", presetInput("a", "discord", good, ""), "unknown preset"},
		{"PUT", put(presetInput("b", "fake", good, "room")), "POST"},
		{"body template", tmpl(presetInput("c", "fake", good, "room")), "body template"},
		{"content type header", ctype(presetInput("d", "fake", good, "room")), "Content-Type"},
		{"settings refused", presetInput("e", "fake", good, "lobby"), "chat id must be room"},
		{"url refused", presetInput("f", "fake", "https://chat.example.test/bot123:s3cr3t-token/other", "room"), "must end in /send"},
		{"chat id on raw", presetInput("g", "", good, "room"), "chat id"},
	}
	for _, c := range cases {
		w := presetWorld(t)
		_, err := w.svc.CreateChannel(ctx, w.org, w.actor, c.in)
		if !errors.Is(err, notify.ErrInvalidInput) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want ErrInvalidInput naming %q", c.name, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("%s: the error repeats the URL: %v", c.name, err)
		}
	}
}

func TestPresetUpdate(t *testing.T) {
	w := presetWorld(t)
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, presetInput("chat", "fake", "https://chat.example.test/x/send", "room"))
	if err != nil {
		t.Fatal(err)
	}
	// The same preset without a URL keeps the stored one.
	up, err := w.svc.UpdateChannel(ctx, w.org, c.ID, c.Revision, presetInput("chat", "fake", "", "room"))
	if err != nil || up.Preset != "fake" {
		t.Fatalf("keep = %+v, %v", up, err)
	}
	// A different preset (or none) needs the URL again.
	for _, preset := range []string{"open", ""} {
		_, err := w.svc.UpdateChannel(ctx, w.org, c.ID, up.Revision, presetInput("chat", preset, "", map[string]string{"open": "x"}[preset]))
		if !errors.Is(err, notify.ErrInvalidInput) || !strings.Contains(err.Error(), "URL again") {
			t.Fatalf("switch to %q without url = %v", preset, err)
		}
	}
	raw, err := w.svc.UpdateChannel(ctx, w.org, c.ID, up.Revision, presetInput("chat", "", "https://hooks.example.test/raw", ""))
	if err != nil || raw.Preset != "" {
		t.Fatalf("to raw = %+v, %v", raw, err)
	}
	if _, v, _ := raw.View(); v.ChatID != "" || v.Preset != "" {
		t.Fatalf("raw view keeps preset settings: %+v", v)
	}
	back, err := w.svc.UpdateChannel(ctx, w.org, c.ID, raw.Revision, presetInput("chat", "open", "https://chat.example.test/y/send", ""))
	if err != nil || back.Preset != "open" {
		t.Fatalf("raw to preset = %+v, %v", back, err)
	}
}

// TestPresetSend: a preset channel posts the preset's body with
// everything else of a webhook send unchanged — operator headers, the
// X-Pulsix-* headers and the signature.
func TestPresetSend(t *testing.T) {
	w := presetWorld(t)
	rec := &receiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	in := presetInput("chat", "fake", srv.URL+"/bot1:tok/send", "room")
	in.Webhook.Headers = []notify.HeaderInput{{Name: "Authorization", Value: str("Bearer relay")}}
	in.Webhook.Signed, in.Webhook.SigningSecret = true, str("0123456789abcdef0123")
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil, testPresets)
	if err := sender.Send(ctx, notify.Claimed{Channel: c, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7())}}, notify.Event{Kind: notify.EventTest}); err != nil {
		t.Fatal(err)
	}
	h := rec.headers[0]
	if string(rec.bodies[0]) != `{"chat":"room","kind":"test"}` || h.Get("Content-Type") != "application/json" ||
		h.Get("Authorization") != "Bearer relay" || h.Get("X-Pulsix-Signature-256") == "" || h.Get("X-Pulsix-Event") != "test" {
		t.Fatalf("headers %v, body %s", h, rec.bodies[0])
	}
}

func TestPresetSendUnknownPresetIsPermanent(t *testing.T) {
	w := presetWorld(t)
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, presetInput("chat", "fake", "https://chat.example.test/x/send", "room"))
	if err != nil {
		t.Fatal(err)
	}
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil, nil)
	err = sender.Send(ctx, notify.Claimed{Channel: c, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7())}}, notify.Event{Kind: notify.EventTest})
	if !notify.IsPermanent(err) || !strings.Contains(err.Error(), "fake") {
		t.Fatalf("send = %v", err)
	}
}

// TestPresetSendErrorRedactsURL: a failed request through a preset
// channel never repeats the URL, whose path holds the token.
func TestPresetSendErrorRedactsURL(t *testing.T) {
	w := presetWorld(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	defer srv.Close()
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, presetInput("chat", "fake", srv.URL+"/botS3CRET/send", "room"))
	if err != nil {
		t.Fatal(err)
	}
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil, testPresets)
	claimed := notify.Claimed{Channel: c, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7())}}
	err = sender.Send(ctx, claimed, notify.Event{Kind: notify.EventTest})
	if !notify.IsPermanent(err) || strings.Contains(err.Error(), "S3CRET") {
		t.Fatalf("4xx = %v", err)
	}
	srv.Close() // a transport error now
	err = sender.Send(ctx, claimed, notify.Event{Kind: notify.EventTest})
	if err == nil || strings.Contains(err.Error(), "S3CRET") {
		t.Fatalf("transport = %v", err)
	}
}
