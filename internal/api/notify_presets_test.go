package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type presetChannelOut struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Webhook  *struct {
		URL          string `json:"url"`
		Method       string `json:"method"`
		Preset       string `json:"preset"`
		ChatID       string `json:"chat_id"`
		BodyTemplate string `json:"body_template"`
	} `json:"webhook"`
}

func createPresetChannel(t *testing.T, f runFixture, body map[string]any) (presetChannelOut, string) {
	t.Helper()
	resp := postJSON(t, f.adminC, f.channelsURL(), body)
	raw := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Channel presetChannelOut `json:"channel"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out.Channel, raw
}

func sendTest(t *testing.T, f runFixture, id string) string {
	t.Helper()
	var tested struct {
		Delivery struct {
			Status    string `json:"status"`
			LastError string `json:"last_error"`
		} `json:"delivery"`
	}
	decodeInto(t, postJSON(t, f.adminC, f.channelsURL()+"/"+id+"/test", nil), &tested)
	if tested.Delivery.Status != "sent" {
		t.Fatalf("test send = %+v", tested)
	}
	return tested.Delivery.Status
}

// TestNotifyChannelTelegramPreset (backlog 4.7): a Telegram preset channel
// keeps its token-bearing URL sealed, returns the chat id, and "Send test"
// posts sendMessage's body to it.
func TestNotifyChannelTelegramPreset(t *testing.T) {
	f := newRunFixture(t, "notify-telegram")
	rec := &hookReceiver{}
	hook := httptest.NewServer(rec)
	defer hook.Close()
	const token = "123456:S3CRET-bot-token"
	c, raw := createPresetChannel(t, f, map[string]any{
		"kind": "webhook", "name": "telegram",
		"webhook": map[string]any{"preset": "telegram", "chat_id": "-1001234567890", "url": hook.URL + "/bot" + token + "/sendMessage"},
	})
	if strings.Contains(raw, "S3CRET") || strings.Contains(raw, "123456:") {
		t.Fatalf("the response carries the token: %s", raw)
	}
	if w := c.Webhook; w == nil || w.Preset != "telegram" || w.ChatID != "-1001234567890" || w.URL != hook.URL+"/…" || w.Method != "POST" {
		t.Fatalf("created = %+v", c.Webhook)
	}
	sendTest(t, f, c.ID)
	var sent struct {
		ChatID    string `json:"chat_id"`
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode"`
	}
	if err := json.Unmarshal([]byte(rec.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.ChatID != "-1001234567890" || sent.ParseMode != "HTML" || !strings.HasPrefix(sent.Text, "<b>Test notification from ") {
		t.Fatalf("sent = %+v", sent)
	}

	for name, body := range map[string]map[string]any{
		"bad chat id": {"preset": "telegram", "chat_id": "general", "url": hook.URL + "/bot" + token + "/sendMessage"},
		"bad path":    {"preset": "telegram", "chat_id": "-1", "url": hook.URL + "/bot" + token + "/getMe"},
		"template":    {"preset": "telegram", "chat_id": "-1", "url": hook.URL + "/bot" + token + "/sendMessage", "body_template": "{}"},
		"unknown":     {"preset": "discord", "url": hook.URL + "/x"},
	} {
		resp := postJSON(t, f.adminC, f.channelsURL(), map[string]any{"kind": "webhook", "name": "bad " + name, "webhook": body})
		raw := mustReadBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, resp.StatusCode, raw)
		}
		if strings.Contains(raw, "S3CRET") {
			t.Errorf("%s: the refusal carries the token: %s", name, raw)
		}
	}
}

// TestNotifyChannelSlackPreset: a Slack preset channel sends blocks in a
// coloured attachment, and switching it to another preset needs the URL.
func TestNotifyChannelSlackPreset(t *testing.T) {
	f := newRunFixture(t, "notify-slack")
	rec := &hookReceiver{}
	hook := httptest.NewServer(rec)
	defer hook.Close()
	c, _ := createPresetChannel(t, f, map[string]any{
		"kind": "webhook", "name": "slack",
		"webhook": map[string]any{"preset": "slack", "url": hook.URL + "/services/T0/B0/s3cret"},
	})
	if c.Webhook == nil || c.Webhook.Preset != "slack" || c.Webhook.ChatID != "" || c.Webhook.BodyTemplate != "" {
		t.Fatalf("created = %+v", c.Webhook)
	}
	sendTest(t, f, c.ID)
	var sent struct {
		Text        string `json:"text"`
		Attachments []struct {
			Color  string            `json:"color"`
			Blocks []json.RawMessage `json:"blocks"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(rec.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sent.Text, "Test notification from ") || len(sent.Attachments) != 1 || sent.Attachments[0].Color == "" || len(sent.Attachments[0].Blocks) < 2 {
		t.Fatalf("sent = %s", rec.bodies[0])
	}
	if rec.headers[0].Get("X-Pulsix-Event") != "test" || rec.headers[0].Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", rec.headers[0])
	}

	resp := requestWithHeaders(t, f.adminC, http.MethodPut, f.channelsURL()+"/"+c.ID,
		map[string]any{"kind": "webhook", "name": "slack", "webhook": map[string]any{"preset": "mattermost"}}, map[string]string{"If-Match": `"1"`})
	if raw := mustReadBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(raw, "URL again") {
		t.Fatalf("switch without url = %d %s", resp.StatusCode, raw)
	}
	resp = requestWithHeaders(t, f.adminC, http.MethodPut, f.channelsURL()+"/"+c.ID,
		map[string]any{"kind": "webhook", "name": "slack", "webhook": map[string]any{"preset": "slack"}}, map[string]string{"If-Match": `"1"`})
	if raw := mustReadBody(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(raw, `"preset":"slack"`) {
		t.Fatalf("keep = %d %s", resp.StatusCode, raw)
	}
}
