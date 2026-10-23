package api_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	mu      sync.Mutex
	events  []string
	bodies  []string
	headers []http.Header
}

func (h *hookReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.events = append(h.events, r.Header.Get("X-Pulsix-Event"))
	h.bodies = append(h.bodies, string(body))
	h.headers = append(h.headers, r.Header.Clone())
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

// assertNoSecrets fails the test if raw (a whole HTTP response body)
// contains any of secrets verbatim, naming which one leaked and where.
func assertNoSecrets(t *testing.T, where, raw string, secrets []string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(raw, secret) {
			t.Fatalf("%s response carries the secret %q: %s", where, secret, raw)
		}
	}
}

// TestNotificationChannelSecretsRedactedOnReadAndAudit is Task 4 review's
// Important finding: TestNotificationChannelLifecycle only ever checked the
// *create* response for leaked secrets — GET of a single, still-live
// channel and the LIST response were never asserted on directly, and
// neither was the audit trail. This test creates a webhook channel whose
// URL carries a token in both the path and the query string, plus a secret
// header value and an HMAC signing secret, then:
//  1. re-fetches it via GET and via LIST and checks the raw response
//     bodies for every secret string, and that the URL reads back as
//     exactly scheme://host (no path, no query);
//  2. updates the channel with every secret field left out (the "keep
//     what is stored" markers) and proves, through a real Send test
//     against an httptest receiver, that the stored header value and
//     signing secret are byte-for-byte unchanged — not just hidden;
//  3. checks that the create/update/test audit rows carry none of the
//     secrets either, in their action, object_id or summary columns.
func TestNotificationChannelSecretsRedactedOnReadAndAudit(t *testing.T) {
	f := newRunFixture(t, "notify-secrets")
	rec := &hookReceiver{}
	hook := httptest.NewServer(rec)
	defer hook.Close()

	const (
		pathToken   = "p4th-s3cret-91a2"
		queryToken  = "qu3ry-s3cret-77bd"
		headerValue = "Bearer h34der-s3cret-c40e"
		hmacSecret  = "0123456789abcdef0123"
	)
	secrets := []string{pathToken, queryToken, headerValue, hmacSecret}

	body := map[string]any{
		"kind": "webhook", "name": "secret hook",
		"webhook": map[string]any{
			"url":     hook.URL + "/hooks/" + pathToken + "?token=" + queryToken,
			"headers": []map[string]any{{"name": "Authorization", "value": headerValue}},
			"signed":  true, "signing_secret": hmacSecret,
		},
	}
	createResp := postJSON(t, f.adminC, f.channelsURL(), body)
	createRaw := mustReadBody(t, createResp)
	assertNoSecrets(t, "create", createRaw, secrets)
	var created struct {
		Channel channelOut `json:"channel"`
	}
	if err := json.Unmarshal([]byte(createRaw), &created); err != nil {
		t.Fatal(err)
	}
	c := created.Channel
	wantDisplayURL := hook.URL + "/…"
	if c.Webhook == nil || c.Webhook.URL != wantDisplayURL {
		t.Fatalf("created webhook URL = %+v, want exactly %q (scheme and host only)", c.Webhook, wantDisplayURL)
	}

	// GET of the still-live channel.
	getResp := requestJSON(t, f.operatorC, http.MethodGet, f.channelsURL()+"/"+c.ID, nil)
	getRaw := mustReadBody(t, getResp)
	assertNoSecrets(t, "GET", getRaw, secrets)
	var got struct {
		Channel channelOut `json:"channel"`
	}
	if err := json.Unmarshal([]byte(getRaw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Channel.Webhook == nil || got.Channel.Webhook.URL != wantDisplayURL || len(got.Channel.Webhook.Headers) != 1 || got.Channel.Webhook.Headers[0] != "Authorization" {
		t.Fatalf("GET channel = %+v", got.Channel)
	}

	// LIST.
	listResp := requestJSON(t, f.operatorC, http.MethodGet, f.channelsURL(), nil)
	listRaw := mustReadBody(t, listResp)
	assertNoSecrets(t, "LIST", listRaw, secrets)
	var list struct {
		Channels []channelOut `json:"channels"`
	}
	if err := json.Unmarshal([]byte(listRaw), &list); err != nil {
		t.Fatal(err)
	}
	var listed *channelOut
	for i := range list.Channels {
		if list.Channels[i].ID == c.ID {
			listed = &list.Channels[i]
		}
	}
	if listed == nil || listed.Webhook == nil || listed.Webhook.URL != wantDisplayURL || len(listed.Webhook.Headers) != 1 || listed.Webhook.Headers[0] != "Authorization" {
		t.Fatalf("LIST channel = %+v", listed)
	}

	// Update with every secret field left out (the "keep what is stored"
	// markers): a bare header name and no url/signing_secret.
	up := map[string]any{"kind": "webhook", "name": "secret hook", "webhook": map[string]any{
		"headers": []map[string]any{{"name": "Authorization"}}, "signed": true,
	}}
	updateResp := requestWithHeaders(t, f.adminC, http.MethodPut, f.channelsURL()+"/"+c.ID, up, map[string]string{"If-Match": `"1"`})
	updateRaw := mustReadBody(t, updateResp)
	assertNoSecrets(t, "update", updateRaw, secrets)
	var updated struct {
		Channel channelOut `json:"channel"`
	}
	if err := json.Unmarshal([]byte(updateRaw), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Channel.Webhook == nil || updated.Channel.Webhook.URL != wantDisplayURL {
		t.Fatalf("updated channel = %+v, want the URL unchanged", updated.Channel)
	}

	// Prove the kept header value and signing secret are byte-for-byte
	// unchanged, not just hidden: Send test really dials the receiver
	// (apitest.New's default policy admits loopback), and the receiver
	// records the exact Authorization header and the exact HMAC signature
	// it was sent.
	testResp := postJSON(t, f.adminC, f.channelsURL()+"/"+c.ID+"/test", nil)
	var tested struct {
		Delivery struct {
			Status string `json:"status"`
		} `json:"delivery"`
	}
	decodeInto(t, testResp, &tested)
	if tested.Delivery.Status != "sent" || rec.count() != 1 {
		t.Fatalf("send test after update = %+v, receiver saw %d requests", tested, rec.count())
	}
	gotHeaders := rec.headers[0]
	if gotHeaders.Get("Authorization") != headerValue {
		t.Fatalf("Authorization header after update = %q, want the original %q (must survive an update that never resent it)", gotHeaders.Get("Authorization"), headerValue)
	}
	mac := hmac.New(sha256.New, []byte(hmacSecret))
	mac.Write([]byte(gotHeaders.Get("X-Pulsix-Timestamp") + "."))
	mac.Write([]byte(rec.bodies[0]))
	wantSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotHeaders.Get("X-Pulsix-Signature-256") != wantSig {
		t.Fatalf("signature after update = %q, want %q computed from the original signing secret", gotHeaders.Get("X-Pulsix-Signature-256"), wantSig)
	}

	// The audit trail — action, object_id and summary — carries none of
	// the secrets either (meta carries only source_ip; see
	// auditMiddleware's own doc comment).
	rows, err := f.s.DB.Pool().Query(context.Background(),
		`SELECT action, object_id, summary FROM audit_events WHERE object_type = 'channel' AND object_id = $1`, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var action, objectID, summary string
		if err := rows.Scan(&action, &objectID, &summary); err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, "audit row ("+action+")", action+" "+objectID+" "+summary, secrets)
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("channel audit events = %d, want 3 (create, update, test)", n)
	}
}

// TestDeliveryPayloadViewAndResend is backlog 2.75. The delivery log
// carried status, attempts and the last error; an operator looking at a
// failed row could neither see what had been sent nor put it back on
// the queue once the receiver was fixed, so the message was simply
// lost.
func TestDeliveryPayloadViewAndResend(t *testing.T) {
	f := newRunFixture(t, "notify-resend")
	// A receiver that refuses, so the test send fails and leaves a row
	// worth resending.
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer refusing.Close()

	var created struct {
		Channel channelOut `json:"channel"`
	}
	decodeInto(t, postJSON(t, f.adminC, f.channelsURL(), map[string]any{
		"kind": "webhook", "name": "flaky hook", "webhook": map[string]any{"url": refusing.URL + "/hook"},
	}), &created)
	ch := created.Channel.ID

	var tested struct {
		Delivery struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"delivery"`
	}
	decodeInto(t, postJSON(t, f.adminC, f.channelsURL()+"/"+ch+"/test", nil), &tested)
	if tested.Delivery.Status != "failed" {
		t.Fatalf("the test send = %+v, want a failed delivery to work with", tested.Delivery)
	}
	one := f.channelsURL() + "/" + ch + "/deliveries/" + tested.Delivery.ID

	// The payload view is part of the log, so an operator may read it.
	var got struct {
		Delivery struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"delivery"`
		Payload json.RawMessage `json:"payload"`
	}
	decodeInto(t, requestJSON(t, f.operatorC, http.MethodGet, one, nil), &got)
	if got.Delivery.ID != tested.Delivery.ID || len(got.Payload) == 0 {
		t.Fatalf("payload view = %+v, payload %s", got.Delivery, got.Payload)
	}
	var ev map[string]any
	if err := json.Unmarshal(got.Payload, &ev); err != nil || ev["kind"] != "test" {
		t.Fatalf("payload = %s, %v; want the stored event", got.Payload, err)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, one, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer payload view = %d", resp.StatusCode)
	}

	// Resending puts organization data in front of an outside receiver,
	// so it is an admin action, like a test send.
	if resp := postJSON(t, f.operatorC, one+"/resend", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator resend = %d", resp.StatusCode)
	}
	var resent struct {
		Delivery struct {
			Status    string `json:"status"`
			Attempts  int    `json:"attempts"`
			LastError string `json:"last_error"`
		} `json:"delivery"`
	}
	decodeInto(t, postJSON(t, f.adminC, one+"/resend", nil), &resent)
	if resent.Delivery.Status != "pending" || resent.Delivery.Attempts != 0 || resent.Delivery.LastError != "" {
		t.Fatalf("resent = %+v, want pending with a fresh budget and no stale error", resent.Delivery)
	}

	// It is now pending, so a second resend is refused rather than
	// silently queueing the same message twice.
	if resp := postJSON(t, f.adminC, one+"/resend", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("resending a pending delivery = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	missing := f.channelsURL() + "/" + ch + "/deliveries/" + "0199a000-0000-7000-8000-000000000001"
	if resp := postJSON(t, f.adminC, missing+"/resend", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("resending an unknown delivery = %d", resp.StatusCode)
	}

	// The resend is in the activity log: it sent data outward.
	var audited int
	if err := f.s.DB.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE object_type = 'channel' AND object_id = $1 AND action = 'POST resendNotificationDelivery'`, ch).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("resend audit events = %d, %v", audited, err)
	}
}
