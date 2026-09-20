package api_test

import (
	"net/http"
	"testing"
)

func TestNotificationDigestSettings(t *testing.T) {
	f := newRunFixture(t, "notify-digest")
	url := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/notification-digest"
	var got struct {
		Digest struct {
			Enabled   bool    `json:"enabled"`
			Weekday   string  `json:"weekday"`
			Time      string  `json:"time"`
			NextRunAt *string `json:"next_run_at"`
			Channels  []struct {
				Name string `json:"name"`
			} `json:"channels"`
		} `json:"digest"`
	}
	decodeInto(t, requestJSON(t, f.operatorC, http.MethodGet, url, nil), &got)
	if got.Digest.Enabled || got.Digest.Weekday != "mon" || got.Digest.Time != "08:00" {
		t.Fatalf("default = %+v", got.Digest)
	}
	resp := postJSON(t, f.adminC, f.channelsURL(), map[string]any{"kind": "webhook", "name": "ops hook", "webhook": map[string]any{"url": "https://hooks.example.test/x"}})
	var ch struct {
		Channel channelOut `json:"channel"`
	}
	decodeInto(t, resp, &ch)
	body := map[string]any{"enabled": true, "weekday": "fri", "time": "16:30", "timezone": "Europe/Berlin", "channels": []string{ch.Channel.ID}}
	if resp := requestJSON(t, f.operatorC, http.MethodPut, url, body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator put = %d", resp.StatusCode)
	}
	decodeInto(t, requestJSON(t, f.adminC, http.MethodPut, url, body), &got)
	if !got.Digest.Enabled || got.Digest.Weekday != "fri" || got.Digest.NextRunAt == nil || len(got.Digest.Channels) != 1 || got.Digest.Channels[0].Name != "ops hook" {
		t.Fatalf("stored = %+v", got.Digest)
	}
	body["channels"] = []string{}
	if resp := requestJSON(t, f.adminC, http.MethodPut, url, body); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("enabled without channels = %d", resp.StatusCode)
	}
	body["time"] = "4pm"
	if resp := requestJSON(t, f.adminC, http.MethodPut, url, body); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad time = %d", resp.StatusCode)
	}
}
