package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

type channelOut struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Webhook *struct {
		URL     string   `json:"url"`
		Headers []string `json:"headers"`
		Signed  bool     `json:"signed"`
	} `json:"webhook"`
	Email *struct {
		Recipients []string `json:"recipients"`
	} `json:"email"`
	Revision int64 `json:"revision"`
}

// hookReceiver is a webhook endpoint recording every request.
type hookReceiver struct {
	mu     sync.Mutex
	events []string
	bodies []string
}

func (h *hookReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.events = append(h.events, r.Header.Get("X-Pulsix-Event"))
	h.bodies = append(h.bodies, string(body))
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (h *hookReceiver) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func (f runFixture) channelsURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/notification-channels"
}

func TestNotificationChannelLifecycle(t *testing.T) {
	f := newRunFixture(t, "notify-life")
	rec := &hookReceiver{}
	hook := httptest.NewServer(rec)
	defer hook.Close()

	body := map[string]any{
		"kind": "webhook", "name": "ops hook",
		"webhook": map[string]any{
			"url":     hook.URL + "/services/T0/B0/s3cret-path",
			"headers": []map[string]any{{"name": "Authorization", "value": "Bearer s3cret-token"}},
			"signed":  true, "signing_secret": "0123456789abcdef0123",
		},
	}
	// Operators see channels; only admins manage them.
	if resp := postJSON(t, f.operatorC, f.channelsURL(), body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator create = %d", resp.StatusCode)
	}
	resp := postJSON(t, f.adminC, f.channelsURL(), body)
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("create = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	raw := mustReadBody(t, resp)
	for _, secret := range []string{"s3cret-path", "s3cret-token", "0123456789abcdef0123"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("the response carries %q: %s", secret, raw)
		}
	}
	var created struct {
		Channel channelOut `json:"channel"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	c := created.Channel
	if c.Webhook == nil || !strings.HasSuffix(c.Webhook.URL, "/…") || c.Webhook.Headers[0] != "Authorization" || !c.Webhook.Signed || !c.Enabled {
		t.Fatalf("created = %+v", c)
	}

	var list struct {
		Channels      []channelOut `json:"channels"`
		MailAvailable bool         `json:"mail_available"`
	}
	decodeInto(t, requestJSON(t, f.operatorC, http.MethodGet, f.channelsURL(), nil), &list)
	if len(list.Channels) != 1 || !list.MailAvailable {
		t.Fatalf("list = %+v", list)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.channelsURL(), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer list = %d", resp.StatusCode)
	}

	// Send test: synchronous, recorded in the log.
	resp = postJSON(t, f.adminC, f.channelsURL()+"/"+c.ID+"/test", nil)
	var tested struct {
		Delivery struct {
			Status    string `json:"status"`
			EventKind string `json:"event_kind"`
		} `json:"delivery"`
	}
	decodeInto(t, resp, &tested)
	if tested.Delivery.Status != "sent" || tested.Delivery.EventKind != "test" || rec.count() != 1 || rec.events[0] != "test" {
		t.Fatalf("test = %+v, receiver saw %v", tested, rec.events)
	}
	var log struct {
		Deliveries []struct {
			Status string `json:"status"`
		} `json:"deliveries"`
	}
	decodeInto(t, requestJSON(t, f.operatorC, http.MethodGet, f.channelsURL()+"/"+c.ID+"/deliveries?limit=10", nil), &log)
	if len(log.Deliveries) != 1 || log.Deliveries[0].Status != "sent" {
		t.Fatalf("log = %+v", log)
	}

	// Update keeps the stored URL, header value and secret when left out.
	up := map[string]any{"kind": "webhook", "name": "ops hook 2", "enabled": false, "webhook": map[string]any{
		"headers": []map[string]any{{"name": "Authorization"}}, "signed": true,
	}}
	if resp := requestWithHeaders(t, f.adminC, http.MethodPut, f.channelsURL()+"/"+c.ID, up, map[string]string{"If-Match": `"7"`}); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale update = %d", resp.StatusCode)
	}
	resp = requestWithHeaders(t, f.adminC, http.MethodPut, f.channelsURL()+"/"+c.ID, up, map[string]string{"If-Match": `"1"`})
	var updated struct {
		Channel channelOut `json:"channel"`
	}
	decodeInto(t, resp, &updated)
	if updated.Channel.Name != "ops hook 2" || updated.Channel.Enabled || updated.Channel.Webhook.URL != c.Webhook.URL || updated.Channel.Revision != 2 {
		t.Fatalf("updated = %+v", updated.Channel)
	}

	// The change is in the activity log under the channel object type.
	var audited int
	if err := f.s.DB.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE object_type = 'channel' AND object_id = $1`, c.ID).Scan(&audited); err != nil || audited != 3 {
		t.Fatalf("channel audit events = %d, %v; want create, test and update", audited, err)
	}

	if resp := requestJSON(t, f.adminC, http.MethodDelete, f.channelsURL()+"/"+c.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.adminC, http.MethodGet, f.channelsURL()+"/"+c.ID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete = %d", resp.StatusCode)
	}
}

func TestEmailChannelNeedsInstanceMail(t *testing.T) {
	f := newRunFixture(t, "notify-mail")
	body := map[string]any{"kind": "email", "name": "ops mail", "email": map[string]any{"recipients": []string{"ops@example.test"}}}
	f.s.Mail.SetConfigured(false)
	resp := postJSON(t, f.adminC, f.channelsURL(), body)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(mustReadBody(t, resp), "outgoing mail") {
		t.Fatalf("e-mail channel without instance mail = %d", resp.StatusCode)
	}
	var list struct {
		MailAvailable bool `json:"mail_available"`
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodGet, f.channelsURL(), nil), &list)
	if list.MailAvailable {
		t.Fatal("mail_available must be false")
	}
	f.s.Mail.SetConfigured(true)
	resp = postJSON(t, f.adminC, f.channelsURL(), body)
	var created struct {
		Channel channelOut `json:"channel"`
	}
	decodeInto(t, resp, &created)
	if created.Channel.Email == nil || created.Channel.Email.Recipients[0] != "ops@example.test" {
		t.Fatalf("created = %+v", created)
	}
	resp = postJSON(t, f.adminC, f.channelsURL()+"/"+created.Channel.ID+"/test", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	sent := f.s.Mail.Sent()
	if len(sent) != 1 || sent[0].To != "ops@example.test" || !strings.HasPrefix(sent[0].Subject, "[Pulsix] Test notification") {
		t.Fatalf("sent = %+v", sent)
	}
	// Invalid input names the problem.
	bad := map[string]any{"kind": "email", "name": "bad", "email": map[string]any{"recipients": []string{"Ops <ops@example.test>"}}}
	if resp := postJSON(t, f.adminC, f.channelsURL(), bad); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("display-name recipient = %d", resp.StatusCode)
	}
}

// TestSendTestRefusesLoopbackDestination is Task 3 review's carry-over
// (a) — notify.Service.SendTest must actually go through the SSRF-safe
// outbound client, not just accept whatever internal/notify.Sender the
// caller happens to hand it. apitest.New's default Deps.Notify admits
// loopback on purpose (so every other test's httptest receiver, bound to
// 127.0.0.1, is reachable); this test rebuilds it on production's real
// policy — no PULSIX_NOTIFY_ALLOW_CIDRS entries — via
// apitest.WithRestrictiveNotifyPolicy, and checks that a webhook channel
// pointed at a loopback receiver is refused rather than delivered.
func TestSendTestRefusesLoopbackDestination(t *testing.T) {
	s := apitest.New(t, apitest.WithRestrictiveNotifyPolicy())
	admin := s.SeedUser("notify-ssrf-admin@example.test", "correct horse battery")
	s.SeedOrg("notify-ssrf", admin)
	adminC := s.LoginAs(admin)

	rec := &hookReceiver{}
	hook := httptest.NewServer(rec)
	defer hook.Close()

	channelsURL := s.HTTP.URL + "/api/v1/orgs/notify-ssrf/notification-channels"
	body := map[string]any{
		"kind": "webhook", "name": "loopback hook",
		"webhook": map[string]any{"url": hook.URL + "/x"},
	}
	resp := postJSON(t, adminC, channelsURL, body)
	var created struct {
		Channel channelOut `json:"channel"`
	}
	decodeInto(t, resp, &created)
	if created.Channel.ID == "" {
		t.Fatalf("create = %+v", created)
	}

	resp = postJSON(t, adminC, channelsURL+"/"+created.Channel.ID+"/test", nil)
	var tested struct {
		Delivery struct {
			Status    string `json:"status"`
			LastError string `json:"last_error"`
		} `json:"delivery"`
	}
	decodeInto(t, resp, &tested)
	if tested.Delivery.Status != "failed" {
		t.Fatalf("send test to a loopback receiver = %+v, want failed", tested.Delivery)
	}
	if rec.count() != 0 {
		t.Fatalf("the loopback receiver saw %d requests, want 0 (the outbound client must refuse to dial it)", rec.count())
	}
}
