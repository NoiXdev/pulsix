package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/notify/presets"
)

// serviceFake stands in for ntfy, Pushover, Twilio and PagerDuty: it
// records every request's path, headers and body and answers 202.
type serviceFake struct {
	mu      sync.Mutex
	paths   []string
	headers []http.Header
	bodies  []string
}

func (f *serviceFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.headers = append(f.headers, r.Header.Clone())
	f.bodies = append(f.bodies, string(body))
	f.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func (f *serviceFake) last() (string, http.Header, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.paths) - 1
	return f.paths[n], f.headers[n], f.bodies[n]
}

type preset48Out struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Webhook  *struct {
		URL        string   `json:"url"`
		Preset     string   `json:"preset"`
		AccountSID string   `json:"account_sid"`
		From       string   `json:"from"`
		To         string   `json:"to"`
		Region     string   `json:"region"`
		Secrets    []string `json:"secrets"`
	} `json:"webhook"`
}

func create48(t *testing.T, f runFixture, webhook map[string]any) (preset48Out, string) {
	t.Helper()
	resp := postJSON(t, f.adminC, f.channelsURL(), map[string]any{"kind": "webhook", "name": "c " + webhook["preset"].(string), "webhook": webhook})
	raw := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Channel preset48Out `json:"channel"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out.Channel, raw
}

// refused48 asserts each body is refused with 4xx and never echoes leak.
func refused48(t *testing.T, f runFixture, leak string, bodies map[string]map[string]any) {
	t.Helper()
	for name, webhook := range bodies {
		resp := postJSON(t, f.adminC, f.channelsURL(), map[string]any{"kind": "webhook", "name": "bad " + name, "webhook": webhook})
		raw := mustReadBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, resp.StatusCode, raw)
		}
		if strings.Contains(raw, leak) {
			t.Errorf("%s: the refusal repeats a secret: %s", name, raw)
		}
	}
}

// TestNotifyChannelNtfyPreset (backlog 4.8): an ntfy channel publishes to
// its server's root with the topic in the body, its access token in a
// sealed header.
func TestNotifyChannelNtfyPreset(t *testing.T) {
	f := newRunFixture(t, "notify-ntfy")
	fake := &serviceFake{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, raw := create48(t, f, map[string]any{
		"preset": "ntfy", "url": srv.URL + "/pulsix-S3CRET",
		"headers": []map[string]any{{"name": "Authorization", "value": "Bearer tk_T0KEN"}},
	})
	if strings.Contains(raw, "S3CRET") || strings.Contains(raw, "T0KEN") || c.Webhook.Preset != "ntfy" || c.Webhook.URL != srv.URL+"/…" {
		t.Fatalf("created = %s", raw)
	}
	sendTest(t, f, c.ID)
	path, h, body := fake.last()
	var sent struct {
		Topic string `json:"topic"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil || path != "/" || sent.Topic != "pulsix-S3CRET" || h.Get("Authorization") != "Bearer tk_T0KEN" ||
		!strings.HasPrefix(sent.Title, "Test notification") {
		t.Fatalf("sent %s %v %s", path, h, body)
	}
	refused48(t, f, "S3CRET", map[string]map[string]any{
		"query":   {"preset": "ntfy", "url": srv.URL + "/topic?auth=S3CRET"},
		"secrets": {"preset": "ntfy", "url": srv.URL + "/topic", "secrets": map[string]string{"token": "S3CRET"}},
	})
}

// TestNotifyChannelPushoverPreset: Pulsix builds the URL, the token and
// user key are sealed, named on read and kept on an update without them.
func TestNotifyChannelPushoverPreset(t *testing.T) {
	fake := &serviceFake{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	f := newRunFixtureWith(t, "notify-pushover", apitest.WithNotifyPresets(presets.RegistryAt(srv.URL)))
	const token, user = "aS3CRETtoken56789aToken0123456", "uS3CRETuser456789uUser01234567"
	c, raw := create48(t, f, map[string]any{"preset": "pushover", "secrets": map[string]string{"token": token, "user": user}})
	if strings.Contains(raw, "S3CRET") || c.Webhook.URL != srv.URL+"/…" || !slices.Equal(c.Webhook.Secrets, []string{"token", "user"}) {
		t.Fatalf("created = %s", raw)
	}
	resp := requestWithHeaders(t, f.adminC, http.MethodPut, f.channelsURL()+"/"+c.ID,
		map[string]any{"kind": "webhook", "name": "renamed", "webhook": map[string]any{"preset": "pushover"}}, map[string]string{"If-Match": `"1"`})
	if raw := mustReadBody(t, resp); resp.StatusCode != http.StatusOK || strings.Contains(raw, "S3CRET") {
		t.Fatalf("keep = %d %s", resp.StatusCode, raw)
	}
	sendTest(t, f, c.ID)
	path, h, body := fake.last()
	form, _ := url.ParseQuery(body)
	if path != "/1/messages.json" || h.Get("Content-Type") != "application/x-www-form-urlencoded" || form.Get("token") != token || form.Get("user") != user {
		t.Fatalf("sent %s %v %s", path, h, body)
	}
	refused48(t, f, "S3CRET", map[string]map[string]any{
		"url":       {"preset": "pushover", "url": srv.URL + "/1/messages.json", "secrets": map[string]string{"token": token, "user": user}},
		"no user":   {"preset": "pushover", "secrets": map[string]string{"token": token}},
		"bad token": {"preset": "pushover", "secrets": map[string]string{"token": "S3CRET", "user": user}},
	})
}

// TestNotifyChannelTwilioPreset: the account, sender and recipient come
// back, the auth token goes out as basic auth only.
func TestNotifyChannelTwilioPreset(t *testing.T) {
	fake := &serviceFake{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	f := newRunFixtureWith(t, "notify-twilio", apitest.WithNotifyPresets(presets.RegistryAt(srv.URL)))
	const sid, token = "AC0123456789abcdef0123456789abcdef", "S3CRET0123456789abcdef0123456789"
	c, raw := create48(t, f, map[string]any{
		"preset": "twilio", "account_sid": sid, "from": "+15005550006", "to": "+4915112345678",
		"secrets": map[string]string{"auth_token": token},
	})
	w := c.Webhook
	if strings.Contains(raw, "S3CRET") || w.AccountSID != sid || w.From != "+15005550006" || w.To != "+4915112345678" || !slices.Equal(w.Secrets, []string{"auth_token"}) {
		t.Fatalf("created = %s", raw)
	}
	sendTest(t, f, c.ID)
	path, h, body := fake.last()
	user, pass, ok := (&http.Request{Header: h}).BasicAuth()
	form, _ := url.ParseQuery(body)
	if path != "/2010-04-01/Accounts/"+sid+"/Messages.json" || !ok || user != sid || pass != token || form.Get("To") != "+4915112345678" || !strings.HasPrefix(form.Get("Body"), "Test notification") {
		t.Fatalf("sent %s %v %s", path, h, body)
	}
	refused48(t, f, "S3CRET", map[string]map[string]any{
		"two recipients": {"preset": "twilio", "account_sid": sid, "from": "+15005550006", "to": "+4915112345678,+4915112345679", "secrets": map[string]string{"auth_token": token}},
		"bad token":      {"preset": "twilio", "account_sid": sid, "from": "+15005550006", "to": "+4915112345678", "secrets": map[string]string{"auth_token": "S3CRET"}},
		"chat id":        {"preset": "twilio", "account_sid": sid, "from": "+15005550006", "to": "+4915112345678", "chat_id": "1", "secrets": map[string]string{"auth_token": token}},
	})
}

// TestNotifyChannelPagerDutyPreset: Send test is a change event, which
// pages nobody; the region comes back.
func TestNotifyChannelPagerDutyPreset(t *testing.T) {
	fake := &serviceFake{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	f := newRunFixtureWith(t, "notify-pagerduty", apitest.WithNotifyPresets(presets.RegistryAt(srv.URL)))
	const key = "S3CRET0123456789abcdef0123456789"
	c, raw := create48(t, f, map[string]any{"preset": "pagerduty", "region": "eu", "secrets": map[string]string{"routing_key": key}})
	if strings.Contains(raw, "S3CRET") || c.Webhook.Region != "eu" || !slices.Equal(c.Webhook.Secrets, []string{"routing_key"}) {
		t.Fatalf("created = %s", raw)
	}
	sendTest(t, f, c.ID)
	path, _, body := fake.last()
	var sent struct {
		RoutingKey string `json:"routing_key"`
		Payload    struct {
			Summary string `json:"summary"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil || path != "/v2/change/enqueue" || sent.RoutingKey != key || sent.Payload.Summary == "" {
		t.Fatalf("sent %s %s", path, body)
	}
	refused48(t, f, "S3CRET", map[string]map[string]any{
		"no key":     {"preset": "pagerduty"},
		"bad key":    {"preset": "pagerduty", "secrets": map[string]string{"routing_key": "S3CRET"}},
		"bad region": {"preset": "pagerduty", "region": "apac", "secrets": map[string]string{"routing_key": key}},
	})
}
