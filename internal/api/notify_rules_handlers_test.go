package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// TestScheduleNotificationRules: rules round-trip in the spec's
// {on, channels} shape, a channel named by two rules gets one message per
// run, and a finished scheduled run queues it.
func TestScheduleNotificationRules(t *testing.T) {
	f := newRunFixture(t, "notify-rules")
	hook := map[string]any{"kind": "webhook", "name": "ops hook", "webhook": map[string]any{"url": "https://hooks.example.test/x"}}
	resp := postJSON(t, f.adminC, f.channelsURL(), hook)
	var ch struct {
		Channel channelOut `json:"channel"`
	}
	decodeInto(t, resp, &ch)
	resp = postJSON(t, f.operatorC, f.schedulesURL(), f.scheduleBody("Nightly"))
	var sch struct {
		Schedule struct {
			ID string `json:"id"`
		} `json:"schedule"`
	}
	decodeInto(t, resp, &sch)
	rulesURL := f.schedulesURL() + "/" + sch.Schedule.ID + "/notifications"

	body := map[string]any{"rules": []map[string]any{
		{"on": "failure", "channels": []string{ch.Channel.ID}},
		{"on": "change", "channels": []string{ch.Channel.ID}},
	}}
	if resp := requestJSON(t, f.viewerC, http.MethodPut, rulesURL, body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer put = %d", resp.StatusCode)
	}
	resp = requestJSON(t, f.operatorC, http.MethodPut, rulesURL, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	var rules struct {
		Rules []struct {
			On       string `json:"on"`
			Channels []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"channels"`
		} `json:"rules"`
	}
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, rulesURL, nil), &rules)
	if len(rules.Rules) != 2 || rules.Rules[0].On != "failure" || rules.Rules[1].On != "change" || rules.Rules[0].Channels[0].Name != "ops hook" {
		t.Fatalf("rules = %+v", rules)
	}
	unknown := map[string]any{"rules": []map[string]any{{"on": "failure", "channels": []string{uuid.Must(uuid.NewV7()).String()}}}}
	if resp := requestJSON(t, f.operatorC, http.MethodPut, rulesURL, unknown); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown channel = %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.operatorC, http.MethodGet, f.schedulesURL()+"/"+uuid.Must(uuid.NewV7()).String()+"/notifications", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown schedule = %d", resp.StatusCode)
	}

	// A scheduled run that fails with a changed host queues ONE message.
	resp = postJSON(t, f.operatorC, f.schedulesURL()+"/"+sch.Schedule.ID+"/run", nil)
	var started struct {
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	decodeInto(t, resp, &started)
	jobID := uuid.MustParse(started.Jobs[0].ID)
	if _, err := f.s.DB.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'running', started_at = now() WHERE id = $1`, jobID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Queue.Finish(context.Background(), jobID, jobs.StatusFailed, map[string]any{"changed_hosts": []string{f.hostA.String()}}, "exited with code 2"); err != nil {
		t.Fatal(err)
	}
	var log struct {
		Deliveries []struct {
			EventKind string `json:"event_kind"`
			Status    string `json:"status"`
		} `json:"deliveries"`
	}
	decodeInto(t, requestJSON(t, f.operatorC, http.MethodGet, f.channelsURL()+"/"+ch.Channel.ID+"/deliveries", nil), &log)
	if len(log.Deliveries) != 1 || log.Deliveries[0].EventKind != "run.finished" || log.Deliveries[0].Status != "pending" {
		t.Fatalf("deliveries = %+v, want one queued run.finished", log.Deliveries)
	}
}
