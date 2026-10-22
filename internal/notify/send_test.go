package notify_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/outbound"
)

// receiver records what a webhook endpoint got and answers with status.
type receiver struct {
	mu      sync.Mutex
	status  int
	headers []http.Header
	bodies  [][]byte
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.headers = append(r.headers, req.Header.Clone())
	r.bodies = append(r.bodies, body)
	status := r.status
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusNoContent
	}
	w.WriteHeader(status)
}

func loopbackClient() *http.Client {
	return outbound.NewClient(outbound.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}), 2*time.Second, nil)
}

func webhookChannel(t *testing.T, w *world, url, template string, signed bool) notify.Channel {
	t.Helper()
	in := webhookInput("hook " + uuid.NewString()[:8])
	in.Webhook.URL, in.Webhook.BodyTemplate, in.Webhook.Signed = &url, template, signed
	if !signed {
		in.Webhook.SigningSecret = nil
	}
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWebhookSendHeadersSignatureAndBody(t *testing.T) {
	w := newWorld(t)
	rec := &receiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	c := webhookChannel(t, w, srv.URL+"/hook?token=abc", `{"text": {{ schedule.name | json }}}`, true)
	now := time.Unix(1790000000, 0)
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), func() time.Time { return now })
	delivery := uuid.Must(uuid.NewV7())
	ev := notify.Event{Kind: notify.EventRunFinished, Schedule: &notify.NamedRef{Name: "Nightly"}}
	if err := sender.Send(ctx, c, delivery, ev); err != nil {
		t.Fatal(err)
	}
	h, body := rec.headers[0], rec.bodies[0]
	if string(body) != `{"text": "Nightly"}` || h.Get("Authorization") != "Bearer t0ken" || h.Get("Content-Type") != "application/json" ||
		h.Get("X-Pulsix-Event") != "run.finished" || h.Get("X-Pulsix-Delivery") != delivery.String() || h.Get("X-Pulsix-Timestamp") != "1790000000" {
		t.Fatalf("headers %v, body %s", h, body)
	}
	mac := hmac.New(sha256.New, []byte("0123456789abcdef0123"))
	mac.Write([]byte("1790000000." + string(body)))
	if h.Get("X-Pulsix-Signature-256") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature = %q", h.Get("X-Pulsix-Signature-256"))
	}
}

func TestWebhookSendDefaultBodyIsTheEvent(t *testing.T) {
	w := newWorld(t)
	rec := &receiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	c := webhookChannel(t, w, srv.URL, "", false)
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil)
	if err := sender.Send(ctx, c, uuid.Must(uuid.NewV7()), notify.Event{Kind: notify.EventTest, Org: notify.OrgRef{Slug: "acme"}}); err != nil {
		t.Fatal(err)
	}
	var got notify.Event
	if err := json.Unmarshal(rec.bodies[0], &got); err != nil || got.Org.Slug != "acme" || rec.headers[0].Get("X-Pulsix-Signature-256") != "" {
		t.Fatalf("body %s, signature %q", rec.bodies[0], rec.headers[0].Get("X-Pulsix-Signature-256"))
	}
}

func TestWebhookSendClassifiesFailures(t *testing.T) {
	w := newWorld(t)
	cases := []struct {
		status    int
		permanent bool
	}{
		{http.StatusInternalServerError, false},
		{http.StatusServiceUnavailable, false},
		{http.StatusTooManyRequests, false},
		{http.StatusRequestTimeout, false},
		{http.StatusBadRequest, true},
		{http.StatusNotFound, true},
		{http.StatusFound, true}, // redirects are never followed
	}
	for _, c := range cases {
		rec := &receiver{status: c.status}
		srv := httptest.NewServer(rec)
		ch := webhookChannel(t, w, srv.URL, "", false)
		err := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil).Send(ctx, ch, uuid.Must(uuid.NewV7()), notify.Event{Kind: notify.EventTest})
		srv.Close()
		if err == nil || notify.IsPermanent(err) != c.permanent {
			t.Errorf("status %d: err = %v, permanent = %v", c.status, err, notify.IsPermanent(err))
			continue
		}
		if strings.Contains(err.Error(), srv.URL) {
			t.Errorf("status %d: the error repeats the URL: %v", c.status, err)
		}
	}
}

func TestWebhookSendRefusesLoopbackWithoutAllowlist(t *testing.T) {
	w := newWorld(t)
	rec := &receiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	ch := webhookChannel(t, w, srv.URL+"/secret-path", "", false)
	client := outbound.NewClient(outbound.NewPolicy(nil), time.Second, nil)
	err := notify.NewChannelSender(fakeBox{}, w.mail, client, nil).Send(ctx, ch, uuid.Must(uuid.NewV7()), notify.Event{Kind: notify.EventTest})
	if !notify.IsPermanent(err) || !errors.Is(err, outbound.ErrBlocked) || strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("err = %v, want a permanent ErrBlocked without the URL", err)
	}
	if len(rec.bodies) != 0 {
		t.Fatal("a blocked destination was contacted")
	}
}

func TestWebhookSendTransportFailureIsRetryable(t *testing.T) {
	w := newWorld(t)
	srv := httptest.NewServer(&receiver{})
	url := srv.URL
	srv.Close() // nothing listens any more
	ch := webhookChannel(t, w, url, "", false)
	err := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil).Send(ctx, ch, uuid.Must(uuid.NewV7()), notify.Event{Kind: notify.EventTest})
	if err == nil || notify.IsPermanent(err) || strings.Contains(err.Error(), url) {
		t.Fatalf("err = %v, want a retryable error without the URL", err)
	}
}

func TestEmailSend(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, notify.Input{Kind: notify.KindEmail, Name: "ops", Enabled: true, Email: &notify.EmailConfig{Recipients: []string{"ops@example.test", "dba@example.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil)
	ev := notify.Event{Kind: notify.EventTest, Org: notify.OrgRef{Name: "Acme"}}
	if err := sender.Send(ctx, c, uuid.Must(uuid.NewV7()), ev); err != nil {
		t.Fatal(err)
	}
	if got := w.mail.sent[0]; len(got) != 4 || got[1] != "dba@example.test" || got[2] != "[Pulsix] Test notification from Acme" {
		t.Fatalf("sent = %v", got)
	}
	w.mail.err = errors.New("421 try later")
	if err := sender.Send(ctx, c, uuid.Must(uuid.NewV7()), ev); err == nil || notify.IsPermanent(err) {
		t.Fatalf("an SMTP failure = %v, want retryable", err)
	}
	w.mail.err = mail.ErrNotConfigured
	if err := sender.Send(ctx, c, uuid.Must(uuid.NewV7()), ev); !notify.IsPermanent(err) {
		t.Fatalf("settings removed mid-send = %v, want permanent", err)
	}
	w.mail.err, w.mail.configured = nil, false
	if err := sender.Send(ctx, c, uuid.Must(uuid.NewV7()), ev); !errors.Is(err, notify.ErrMailUnavailable) || !notify.IsPermanent(err) {
		t.Fatalf("no instance mail = %v, want permanent ErrMailUnavailable", err)
	}
}

func TestSendRefusesBrokenChannels(t *testing.T) {
	sender := notify.NewChannelSender(fakeBox{}, &fakeMail{configured: true}, loopbackClient(), nil)
	for _, ch := range []notify.Channel{
		{Kind: "sms"},
		{Kind: notify.KindEmail, Config: []byte(`{`)},
		{Kind: notify.KindWebhook, Config: []byte(`{"url_sealed":"bm9wZQ=="}`)}, // opens with the wrong AAD
	} {
		if err := sender.Send(ctx, ch, uuid.Must(uuid.NewV7()), notify.Event{}); !notify.IsPermanent(err) {
			t.Errorf("channel %+v: err = %v, want permanent", ch, err)
		}
	}
	if notify.Permanent(nil) != nil {
		t.Fatal("Permanent(nil) must stay nil")
	}
}

// TestWebhookErrorsNeverCarryTheURLsSecrets is backlog 2.80's redaction
// gap. redactURL exists because url.Error repeats the whole request URL
// in its text and a webhook URL routinely carries a shared secret — in a
// query parameter, which every chat tool's incoming-webhook URL uses, or
// in userinfo. Nothing pinned either shape, nor the redirect path, where
// the error comes from the client's own CheckRedirect rather than the
// transport. A delivery's last_error is shown in the GUI and kept in the
// database, so a leak here is a stored one.
func TestWebhookErrorsNeverCarryTheURLsSecrets(t *testing.T) {
	const secret = "s3cr3t-webhook-token"

	// A listener that is closed again, so the transport always fails.
	dead := httptest.NewServer(&receiver{})
	deadURL := dead.URL
	dead.Close()

	// A live receiver that answers 302 to somewhere else: the client
	// refuses to follow, and the error is ErrRedirect wrapped by
	// url.Error — a different path through redactURL than a dial failure.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.example.test/", http.StatusFound)
	}))
	defer redirector.Close()

	cases := []struct {
		name string
		url  string
	}{
		{name: "token in the query string", url: deadURL + "/hook?token=" + secret},
		{name: "token in the path", url: deadURL + "/hook/" + secret},
		{name: "redirect refused, token in the query string", url: redirector.URL + "/hook?token=" + secret},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			ch := webhookChannel(t, w, c.url, "", false)
			err := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil).Send(ctx, ch, uuid.Must(uuid.NewV7()), notify.Event{Kind: notify.EventTest})
			if err == nil {
				t.Fatal("want an error")
			}
			// What the delivery row stores is this text, bounded and
			// single-lined by errorText — which can shorten it but
			// never add to it, so checking here is checking the row.
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("the error carries the URL's secret: %s", err)
			}
		})
	}
}

// TestWebhookURLWithUserinfoIsRefusedAtSaveTime records why the redaction
// tests above have no userinfo case: a URL carrying a user name or
// password never reaches the sender at all, because validation rejects it
// when the channel is saved. That is the stronger guarantee of the two —
// a secret that was never stored cannot leak out of an error — and it is
// worth a test of its own, since removing the check would silently move
// the burden onto redaction, which the backlog row assumed was already
// carrying it (backlog 2.80).
func TestWebhookURLWithUserinfoIsRefusedAtSaveTime(t *testing.T) {
	w := newWorld(t)
	in := webhookInput("hook with userinfo")
	url := "https://user:s3cr3t@hooks.example.test/x"
	in.Webhook.URL = &url
	in.Webhook.SigningSecret = nil
	_, err := w.svc.CreateChannel(ctx, w.org, w.actor, in)
	if err == nil {
		t.Fatal("a webhook URL with userinfo was accepted")
	}
	if !strings.Contains(err.Error(), "user name or password") {
		t.Fatalf("err = %v, want the userinfo refusal", err)
	}
}
