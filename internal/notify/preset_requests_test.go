package notify_test

import (
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/notify"
)

// multiPreset renders two requests: a form body to another path with its
// own Authorization header, then a JSON body to the channel URL.
type multiPreset struct{ openPreset }

func (multiPreset) Render(ev notify.Event, t notify.Target) ([]notify.Request, error) {
	return []notify.Request{
		{Path: "/other/path", ContentType: "application/x-www-form-urlencoded", Header: [][2]string{{"Authorization", "Basic cHJlc2V0"}}, Body: []byte("kind=" + string(ev.Kind))},
		{Body: []byte(`{"n":2,"secret":"` + t.Secrets["token"] + `"}`)},
	}, nil
}

// credPreset takes two secrets and renders them into its body.
type credPreset struct{ openPreset }

func (credPreset) SecretNames() []string { return []string{"token", "user"} }

func (credPreset) CheckSecret(name, value string) error {
	if len(value) < 8 {
		return errors.New("the " + name + " is at least 8 characters")
	}
	return nil
}

func (credPreset) Render(_ notify.Event, t notify.Target) ([]notify.Request, error) {
	return []notify.Request{{Body: []byte(t.Secrets["token"] + "/" + t.Secrets["user"])}}, nil
}

// fixedPreset builds its URL from the account setting, under base.
type fixedPreset struct {
	openPreset
	base string
}

func (fixedPreset) CheckSettings(s notify.PresetSettings) (notify.PresetSettings, error) {
	if s.AccountSID == "" {
		return s, errors.New("an account is required")
	}
	return s, s.Only("Fixed", "account_sid")
}

func (p fixedPreset) Endpoint(s notify.PresetSettings) string {
	return p.base + "/v1/" + s.AccountSID + "/send"
}

// pathReceiver records each request's path, headers and body.
type pathReceiver struct {
	mu      sync.Mutex
	fail    map[int]int // request number (1-based) → status
	paths   []string
	headers []http.Header
	bodies  []string
}

func (r *pathReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.RequestURI())
	r.headers = append(r.headers, req.Header.Clone())
	r.bodies = append(r.bodies, string(body))
	status := r.fail[len(r.paths)]
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusAccepted
	}
	w.WriteHeader(status)
}

func requestsWorld(t *testing.T, base string) (*world, notify.Presets) {
	t.Helper()
	w := newWorld(t)
	ps := notify.Presets{
		"fake": fakePreset{}, "multi": multiPreset{}, "cred": credPreset{}, "cred2": credPreset{},
		"fixed": fixedPreset{base: base},
	}
	w.svc = notify.NewService(notify.Deps{
		Channels: w.channels, Box: fakeBox{}, Mail: w.mail, Presets: ps,
		Now: func() time.Time { return time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC) }, Rand: rand.Reader,
	})
	return w, ps
}

func sendOnce(t *testing.T, ps notify.Presets, c notify.Channel) error {
	t.Helper()
	w := newWorld(t)
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil, ps)
	return sender.Send(ctx, notify.Claimed{Channel: c, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7())}}, notify.Event{Kind: notify.EventTest})
}

// TestPresetSendSeveralRequests (backlog 4.8): every request a preset
// renders is sent in order, to its own path, with its own content type
// and headers winning over the operator's, each signed; one failing
// request fails the send and names its place.
func TestPresetSendSeveralRequests(t *testing.T) {
	rec := &pathReceiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	w, ps := requestsWorld(t, srv.URL)
	in := presetInput("multi", "multi", srv.URL+"/topic/send?x=1", "")
	in.Webhook.Headers = []notify.HeaderInput{{Name: "Authorization", Value: str("Bearer relay")}}
	in.Webhook.Signed, in.Webhook.SigningSecret = true, str("0123456789abcdef0123")
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := sendOnce(t, ps, c); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.paths, []string{"/other/path", "/topic/send?x=1"}) {
		t.Fatalf("paths = %v", rec.paths)
	}
	h1, h2 := rec.headers[0], rec.headers[1]
	if h1.Get("Content-Type") != "application/x-www-form-urlencoded" || h1.Get("Authorization") != "Basic cHJlc2V0" || rec.bodies[0] != "kind=test" {
		t.Fatalf("first = %v %q", h1, rec.bodies[0])
	}
	if h2.Get("Content-Type") != "application/json" || h2.Get("Authorization") != "Bearer relay" {
		t.Fatalf("second = %v", h2)
	}
	if h1.Get("X-Pulsix-Signature-256") == "" || h2.Get("X-Pulsix-Signature-256") == h1.Get("X-Pulsix-Signature-256") || h2.Get("X-Pulsix-Event") != "test" {
		t.Fatalf("signatures = %q %q", h1.Get("X-Pulsix-Signature-256"), h2.Get("X-Pulsix-Signature-256"))
	}

	rec.fail = map[int]int{4: http.StatusBadRequest}
	err = sendOnce(t, ps, c)
	if !notify.IsPermanent(err) || !strings.Contains(err.Error(), "request 2 of 2") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("second failing = %v", err)
	}
	rec.fail = map[int]int{5: http.StatusServiceUnavailable}
	if err := sendOnce(t, ps, c); err == nil || notify.IsPermanent(err) || len(rec.paths) != 5 {
		t.Fatalf("first failing = %v after %d requests", err, len(rec.paths))
	}
}

func credInput(name, preset string, secrets map[string]string) notify.Input {
	in := presetInput(name, preset, "https://cred.example.test/send", "")
	in.Webhook.Secrets = secrets
	return in
}

// TestPresetSecretsSealedAndKept: a preset's secrets are sealed, shown by
// name only, kept when omitted on an update of the same preset, replaced
// when given, and required again when the preset changes.
func TestPresetSecretsSealedAndKept(t *testing.T) {
	rec := &pathReceiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	w, ps := requestsWorld(t, srv.URL)
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, credInput("cred", "cred", map[string]string{"token": "tok-AAAAAAAA", "user": "usr-BBBBBBBB"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(c.Config), "AAAAAAAA") || strings.Contains(string(c.Config), "BBBBBBBB") {
		t.Fatalf("stored config holds a secret in clear: %s", c.Config)
	}
	if _, v, _ := c.View(); !slices.Equal(v.SecretNames, []string{"token", "user"}) {
		t.Fatalf("view = %+v", v)
	}

	// Kept: same preset, no secrets, no URL.
	up, err := w.svc.UpdateChannel(ctx, w.org, c.ID, c.Revision, presetInput("cred", "cred", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	// Replaced: one new value, the other kept.
	in := presetInput("cred", "cred", "", "")
	in.Webhook.URL = str(srv.URL + "/send")
	in.Webhook.Secrets = map[string]string{"token": "tok-CCCCCCCC"}
	up, err = w.svc.UpdateChannel(ctx, w.org, c.ID, up.Revision, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := sendOnce(t, ps, up); err != nil || rec.bodies[0] != "tok-CCCCCCCC/usr-BBBBBBBB" {
		t.Fatalf("send = %v, bodies %q", err, rec.bodies)
	}

	for name, in := range map[string]notify.Input{
		"missing":      credInput("a", "cred", map[string]string{"token": "tok-AAAAAAAA"}),
		"bad value":    credInput("b", "cred", map[string]string{"token": "short-1", "user": "usr-BBBBBBBB"}),
		"unknown name": credInput("c", "cred", map[string]string{"token": "tok-AAAAAAAA", "user": "usr-BBBBBBBB", "pin": "PIN-SECRET-1"}),
		"not taken":    credInput("d", "fake", map[string]string{"token": "tok-AAAAAAAA"}),
		"raw":          credInput("e", "", map[string]string{"token": "tok-AAAAAAAA"}),
	} {
		if name == "not taken" {
			in.Webhook.ChatID = "room"
		}
		_, err := w.svc.CreateChannel(ctx, w.org, w.actor, in)
		if !errors.Is(err, notify.ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, secret := range []string{"short-1", "AAAAAAAA", "PIN-SECRET"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error repeats a secret: %v", name, err)
			}
		}
	}

	// Another preset: the old secrets do not carry over.
	in = credInput("cred", "cred2", nil)
	_, err = w.svc.UpdateChannel(ctx, w.org, c.ID, up.Revision, in)
	if !errors.Is(err, notify.ErrInvalidInput) || !strings.Contains(err.Error(), "token") {
		t.Fatalf("switch without secrets = %v", err)
	}
}

// TestPresetEndpoint: a preset that builds its URL refuses a submitted
// one, stores the one it builds (shown as scheme and host), and rebuilds
// it from the settings on every save.
func TestPresetEndpoint(t *testing.T) {
	rec := &pathReceiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	w, ps := requestsWorld(t, srv.URL)
	in := presetInput("fixed", "fixed", "", "")
	in.Webhook.AccountSID = "acct-1"
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, v, _ := c.View(); v.URLDisplay != srv.URL+"/…" || v.AccountSID != "acct-1" {
		t.Fatalf("view = %+v", v)
	}
	in.Webhook.AccountSID = "acct-2"
	up, err := w.svc.UpdateChannel(ctx, w.org, c.ID, c.Revision, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := sendOnce(t, ps, up); err != nil || rec.paths[0] != "/v1/acct-2/send" {
		t.Fatalf("send = %v, paths %v", err, rec.paths)
	}

	withURL := presetInput("fixed 2", "fixed", srv.URL+"/elsewhere", "")
	withURL.Webhook.AccountSID = "acct-1"
	if _, err := w.svc.CreateChannel(ctx, w.org, w.actor, withURL); !errors.Is(err, notify.ErrInvalidInput) || !strings.Contains(err.Error(), "leave the URL out") {
		t.Fatalf("submitted url = %v", err)
	}
	// Leaving it for a preset that needs a URL needs one.
	if _, err := w.svc.UpdateChannel(ctx, w.org, c.ID, up.Revision, presetInput("fixed", "fake", "", "room")); !errors.Is(err, notify.ErrInvalidInput) || !strings.Contains(err.Error(), "URL again") {
		t.Fatalf("switch away without url = %v", err)
	}
}

// TestPresetRefusesSettingsItDoesNotTake: a raw channel takes no preset
// setting, and a preset refuses what its CheckSettings does not allow.
func TestPresetRefusesSettingsItDoesNotTake(t *testing.T) {
	w, _ := requestsWorld(t, "https://fixed.example.test")
	raw := presetInput("raw", "", "https://hooks.example.test/x", "")
	raw.Webhook.To = "+4915112345678"
	if _, err := w.svc.CreateChannel(ctx, w.org, w.actor, raw); !errors.Is(err, notify.ErrInvalidInput) || !strings.Contains(err.Error(), "to belongs to a preset channel") {
		t.Fatalf("raw = %v", err)
	}
	fixed := presetInput("fixed", "fixed", "", "")
	fixed.Webhook.AccountSID, fixed.Webhook.Region = "acct", "eu"
	if _, err := w.svc.CreateChannel(ctx, w.org, w.actor, fixed); !errors.Is(err, notify.ErrInvalidInput) || !strings.Contains(err.Error(), "takes no region") {
		t.Fatalf("fixed = %v", err)
	}
	if err := (notify.PresetSettings{ChatID: "1"}).Only("Slack"); err == nil || !strings.Contains(err.Error(), "a Slack channel takes no chat_id") {
		t.Fatalf("only = %v", err)
	}
}

// emptyPreset renders nothing, hugePreset a body over the cap.
type emptyPreset struct{ openPreset }

func (emptyPreset) Render(notify.Event, notify.Target) ([]notify.Request, error) { return nil, nil }

type hugePreset struct{ openPreset }

func (hugePreset) Render(notify.Event, notify.Target) ([]notify.Request, error) {
	return []notify.Request{{Body: []byte(`{}`)}, {Body: make([]byte, 65<<10)}}, nil
}

// TestPresetSendRefusesWhatCannotBeSent: a preset that renders no
// request, or one body over the cap, fails the delivery permanently and
// sends nothing.
func TestPresetSendRefusesWhatCannotBeSent(t *testing.T) {
	rec := &pathReceiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	w, _ := requestsWorld(t, srv.URL)
	ps := notify.Presets{"empty": emptyPreset{}, "huge": hugePreset{}}
	for _, name := range []string{"empty", "huge"} {
		in := presetInput(name, name, srv.URL+"/x/send", "")
		w.svc = notify.NewService(notify.Deps{Channels: w.channels, Box: fakeBox{}, Mail: w.mail, Presets: ps, Rand: rand.Reader})
		c, err := w.svc.CreateChannel(ctx, w.org, w.actor, in)
		if err != nil {
			t.Fatal(err)
		}
		if err := sendOnce(t, ps, c); !notify.IsPermanent(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(rec.paths) != 0 {
		t.Fatalf("sent %v", rec.paths)
	}
}
