// The v0.2d milestone gate: a scheduled run fails and a signed webhook
// channel receives exactly one message for it, a maintenance window with
// exclude_hosts leaves a host out of a scheduled run, and the calendar
// shows the schedules' occurrences together with the window.
package e2e

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/app"
)

// notifySecret signs the webhook channel's requests.
const notifySecret = "e2e-notify-signing-secret"

// received is one request the webhook receiver got.
type received struct {
	event, delivery, timestamp, signature string
	body                                  []byte
}

// hookReceiver is the webhook channel's endpoint: it records every request
// and answers 204.
type hookReceiver struct {
	mu   sync.Mutex
	reqs []received
}

func (h *hookReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.reqs = append(h.reqs, received{
		event: r.Header.Get("X-Pulsix-Event"), delivery: r.Header.Get("X-Pulsix-Delivery"),
		timestamp: r.Header.Get("X-Pulsix-Timestamp"), signature: r.Header.Get("X-Pulsix-Signature-256"), body: body,
	})
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (h *hookReceiver) snapshot() []received {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]received(nil), h.reqs...)
}

func TestNotificationsEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker for the testcontainer database: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rec := &hookReceiver{}
	hook := httptest.NewServer(rec)
	t.Cleanup(hook.Close)
	clock := &fakeClock{}
	s := wireScenario(t, ctx, cancel, wiring{
		scheduleTick: 100 * time.Millisecond,
		opts:         []app.Option{app.WithScheduleClock(clock.Now)},
		notifyAllow:  []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})
	s.notifications(clock, rec, hook.URL)
}

type (
	channelCreatedOut struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	calendarOut struct {
		Entries []struct {
			ScheduleID string `json:"schedule_id"`
			State      string `json:"state"`
			JobStatus  string `json:"job_status"`
		} `json:"entries"`
		Maintenance []struct {
			Name string `json:"name"`
		} `json:"maintenance"`
	}
)

func (s *scenario) notifications(clock *fakeClock, rec *hookReceiver, hookURL string) {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	s.pinHostKey()

	// --- A signed webhook channel and a failing schedule notifying it -------
	var ch channelCreatedOut
	s.post(prefix+"/notification-channels", map[string]any{
		"kind": "webhook", "name": "e2e receiver",
		"webhook": map[string]any{"url": hookURL + "/pulsix?token=e2e", "signed": true, "signing_secret": notifySecret},
	}, &ch)
	s.failPB = s.createEditorPlaybook("fail", failTemplate(s.t))
	s.failTID = s.createTemplate("deliberate", s.failPB, map[string]any{"should_fail": "true"})
	var failing scheduleOut
	s.post(prefix+"/schedules", map[string]any{
		"template_id": s.failTID, "name": "failing nightly", "mode": "cron", "cron": "*/5 * * * *", "timezone": "UTC",
		"guards": map[string]any{"catch_up": map[string]any{"enabled": true, "window": "1h"}},
	}, &failing)
	// failure and change both name the channel: still one message per run.
	s.put(prefix+"/schedules/"+failing.Schedule.ID+"/notifications", map[string]any{"rules": []map[string]any{
		{"on": "failure", "channels": []string{ch.Channel.ID}},
		{"on": "change", "channels": []string{ch.Channel.ID}},
	}}, &map[string]any{})

	clock.setAt(*failing.Schedule.NextRunAt)
	fired := s.awaitScheduleRun(failing.Schedule.ID, 30*time.Second)
	if fired.Outcome != "triggered" || fired.JobGroupID == "" {
		s.t.Fatalf("failing schedule run = %+v", fired)
	}
	var group jobListOut
	s.get(prefix+"/jobs?group="+fired.JobGroupID, &group)
	if job := s.awaitJob(group.Jobs[0].ID, "run", 90*time.Second); job.Job.Status != "failed" {
		s.t.Fatalf("scheduled run = %q (error %q), want failed%s", job.Job.Status, job.Job.Error, s.runnerOutput())
	}
	// Stop the schedule, so moving the clock later fires nothing more.
	s.post(prefix+"/schedules/"+failing.Schedule.ID+"/pause", struct{}{}, &map[string]any{})

	// --- Exactly one message, signed, naming the failed run ------------------
	deadline := time.Now().Add(30 * time.Second)
	for len(rec.snapshot()) == 0 {
		if time.Now().After(deadline) {
			s.t.Fatal("the webhook channel received nothing in 30s")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(5 * time.Second) // two dispatcher intervals and more: a duplicate would have arrived
	got := rec.snapshot()
	if len(got) != 1 {
		s.t.Fatalf("the channel received %d messages, want exactly 1", len(got))
	}
	msg := got[0]
	mac := hmac.New(sha256.New, []byte(notifySecret))
	mac.Write([]byte(msg.timestamp + "."))
	mac.Write(msg.body)
	if msg.event != "run.finished" || msg.delivery == "" || msg.signature != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		s.t.Fatalf("message headers = %+v", msg)
	}
	var ev struct {
		Kind       string   `json:"kind"`
		Categories []string `json:"categories"`
		Schedule   struct {
			Name string `json:"name"`
		} `json:"schedule"`
		Run struct {
			ID          string `json:"id"`
			Result      string `json:"result"`
			Hosts       int    `json:"hosts"`
			HostsFailed int    `json:"hosts_failed"`
		} `json:"run"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(msg.body, &ev); err != nil {
		s.t.Fatal(err)
	}
	if ev.Schedule.Name != "failing nightly" || ev.Run.Result != "failed" || ev.Run.Hosts != 1 || ev.Run.HostsFailed != 1 ||
		ev.Run.ID != group.Jobs[0].ID || !strings.HasSuffix(ev.URL, "/runs/"+group.Jobs[0].ID) {
		s.t.Fatalf("message = %s", msg.body)
	}
	// Never log output or variables: the playbook's failure text and the
	// template's extra var name stay out of the message.
	if strings.Contains(string(msg.body), "deliberate failure") || strings.Contains(string(msg.body), "should_fail") {
		s.t.Fatalf("the message carries run output or variables: %s", msg.body)
	}
	var log struct {
		Deliveries []struct {
			Status string `json:"status"`
		} `json:"deliveries"`
	}
	s.get(prefix+"/notification-channels/"+ch.Channel.ID+"/deliveries", &log)
	if len(log.Deliveries) != 1 || log.Deliveries[0].Status != "sent" {
		s.t.Fatalf("delivery log = %+v", log.Deliveries)
	}

	// --- A maintenance window leaves a host out of a scheduled run ------------
	var spare hostOut
	s.post(prefix+"/hosts", map[string]any{"name": "e2e-maintained", "zone_id": s.zoneID, "address": "192.0.2.10", "managed": true}, &spare)
	var both templateOut
	s.post(prefix+"/job-templates", map[string]any{
		"name": "hello-both", "playbook_id": s.createEditorPlaybook("hello-both", greetTemplate(s.t)),
		"scope":      map[string]any{"host_ids": []string{s.hostID, spare.Host.ID}},
		"extra_vars": map[string]any{"greeting": s.greeting},
	}, &both)
	now := time.Now().UTC()
	s.post(prefix+"/maintenance-windows", map[string]any{
		"name": "spare host upgrade", "target_kind": "host", "target_id": spare.Host.ID,
		"starts_at": now.Add(-24 * time.Hour).Format(time.RFC3339), "ends_at": now.Add(48 * time.Hour).Format(time.RFC3339),
	}, &map[string]any{})
	var guarded scheduleOut
	s.post(prefix+"/schedules", map[string]any{
		"template_id": both.Template.ID, "name": "guarded", "mode": "cron", "cron": "*/5 * * * *", "timezone": "UTC",
		"guards": map[string]any{"maintenance": "exclude_hosts", "catch_up": map[string]any{"enabled": true, "window": "1h"}},
	}, &guarded)
	if guarded.Schedule.NextRunAt.After(clock.Now()) {
		clock.setAt(*guarded.Schedule.NextRunAt)
	}
	run := s.awaitScheduleRun(guarded.Schedule.ID, 30*time.Second)
	if run.Outcome != "triggered" || !strings.Contains(run.Reason, "1 host(s) left out for maintenance") {
		s.t.Fatalf("guarded run = %+v", run)
	}
	var guardedJobs jobListOut
	s.get(prefix+"/jobs?group="+run.JobGroupID, &guardedJobs)
	if len(guardedJobs.Jobs) != 1 {
		s.t.Fatalf("guarded jobs = %+v", guardedJobs.Jobs)
	}
	done := s.awaitJob(guardedJobs.Jobs[0].ID, "run", 90*time.Second)
	if done.Job.Status != "succeeded" || len(done.Job.Hosts) != 1 || done.Job.Hosts[0] != s.hostID {
		s.t.Fatalf("guarded run = %q on %v, want succeeded on the one host outside the window%s", done.Job.Status, done.Job.Hosts, s.runnerOutput())
	}

	// --- The calendar shows both schedules and the window ----------------------
	q := url.Values{"from": {now.Add(-time.Hour).Format(time.RFC3339)}, "to": {now.Add(7 * 24 * time.Hour).Format(time.RFC3339)}}
	var cal calendarOut
	s.get(prefix+"/calendar?"+q.Encode(), &cal)
	var failedPast, planned bool
	for _, e := range cal.Entries {
		if e.ScheduleID == failing.Schedule.ID && e.State == "triggered" && e.JobStatus == "failed" {
			failedPast = true
		}
		if e.ScheduleID == guarded.Schedule.ID && e.State == "planned" {
			planned = true
		}
	}
	if !failedPast || !planned {
		s.t.Fatalf("calendar entries = %+v, want the failed past run and planned guarded runs", cal.Entries)
	}
	if len(cal.Maintenance) != 1 || cal.Maintenance[0].Name != "spare host upgrade" {
		s.t.Fatalf("calendar maintenance = %+v", cal.Maintenance)
	}
}
