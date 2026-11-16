package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// startJob enqueues a run of the fixture's template as its owner, claims it
// and appends events, the way a worker reports them.
func (f orgFixture) startJob(t *testing.T, events []jobs.Event) uuid.UUID {
	t.Helper()
	created, err := f.s.Deps.Jobs.EnqueueRunWith(f.scoped, f.org, f.template, jobs.RunRequest{Trigger: jobs.TriggerManual, Actor: &f.owner})
	if err != nil || len(created) != 1 {
		t.Fatalf("enqueue: %v %v", created, err)
	}
	id := created[0].ID
	claim, err := f.s.Queue.Claim(context.Background(), f.org, []uuid.UUID{created[0].ZoneID}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	for i, ev := range events {
		if ev.At.IsZero() {
			ev.At = time.Now().Add(time.Duration(i) * time.Millisecond)
		}
		if err := f.s.Queue.AppendEvent(context.Background(), id, ev); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func payload(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"text": text})
	return b
}

func TestJobReadTools(t *testing.T) {
	f := newOrgFixture(t)
	id := f.startJob(t, []jobs.Event{
		{Seq: 1, Host: "web-1", Task: "Gathering Facts", State: "ok", Payload: payload("")},
		{Seq: 2, Host: "web-1", Task: "install nginx", State: "failed", Payload: json.RawMessage(`{"msg":"No package nginx available"}`)},
		{Seq: 3, Host: "web-2", Task: "install nginx", State: "ok", Payload: payload("done")},
	})
	cs := mustConnect(t, f.s, f.token)
	if text := callOK(t, cs, "list_jobs", nil, nil); !strings.Contains(text, id.String()) {
		t.Fatalf("list_jobs lacks the job:\n%s", text)
	}
	if text := callOK(t, cs, "get_job", map[string]any{"job_id": id.String()}, nil); !strings.Contains(text, `"status"`) {
		t.Fatalf("get_job:\n%s", text)
	}
	var log struct {
		Log  string `json:"log"`
		Next int64  `json:"next_after"`
	}
	callOK(t, cs, "get_job_log", map[string]any{"job_id": id.String(), "failed_only": true}, &log)
	if !strings.Contains(log.Log, "2 web-1 install nginx failed: No package nginx available") || strings.Contains(log.Log, "Gathering Facts") {
		t.Fatalf("failed_only log = %q", log.Log)
	}
	callOK(t, cs, "get_job_log", map[string]any{"job_id": id.String(), "host": "web-2"}, &log)
	if !strings.Contains(log.Log, "3 web-2 install nginx ok: done") || strings.Contains(log.Log, "web-1") {
		t.Fatalf("host log = %q", log.Log)
	}
}

// TestJobLogPagesWithoutGapsOrRepeats: reading a log in pages returns
// every event exactly once, and the next value stays put when nothing new
// arrived (Review Focus 5).
func TestJobLogPagesWithoutGapsOrRepeats(t *testing.T) {
	f := newOrgFixture(t)
	var evs []jobs.Event
	for i := 1; i <= 5; i++ {
		evs = append(evs, jobs.Event{Seq: uint64(i), Host: "web-1", Task: "t", State: "ok", Payload: payload("line")})
	}
	id := f.startJob(t, evs)
	cs := mustConnect(t, f.s, f.token)
	seen := map[string]int{}
	after := int64(0)
	for range 4 {
		var page struct {
			Log  string `json:"log"`
			Next int64  `json:"next_after"`
		}
		callOK(t, cs, "get_job_log", map[string]any{"job_id": id.String(), "after": after, "limit": 2}, &page)
		for _, line := range strings.Split(strings.TrimSpace(page.Log), "\n") {
			if line != "" {
				seen[strings.Fields(line)[0]]++
			}
		}
		if page.Next < after {
			t.Fatalf("next went back: %d after %d", page.Next, after)
		}
		after = page.Next
	}
	if len(seen) != 5 {
		t.Fatalf("seen = %v, want 5 distinct events", seen)
	}
	for seq, n := range seen {
		if n != 1 {
			t.Errorf("event %s read %d times", seq, n)
		}
	}
	if after != 5 {
		t.Fatalf("final next_after = %d, want 5", after)
	}
}

func TestLogLimitIsCapped(t *testing.T) {
	f := newOrgFixture(t)
	id := f.startJob(t, []jobs.Event{{Seq: 1, Host: "h", Task: "t", State: "ok", Payload: payload("x")}})
	text, isErr := call(t, mustConnect(t, f.s, f.token), "get_job_log", map[string]any{"job_id": id.String(), "limit": 5000})
	if !isErr || !strings.Contains(text, "1000") {
		t.Fatalf("limit 5000 = %v %s, want a refusal naming the cap", isErr, text)
	}
}

func TestRequestRunAndCancel(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	var req struct {
		RequestID   string `json:"request_id"`
		Status      string `json:"status"`
		ApprovalURL string `json:"approval_url"`
	}
	callOK(t, cs, "request_run", map[string]any{"template_id": f.template.String(), "reason": "deploy web"}, &req)
	if req.Status != "pending" || !strings.HasPrefix(req.ApprovalURL, f.s.PublicURL+"/o/") || !strings.HasSuffix(req.ApprovalURL, "/runs/requests/"+req.RequestID) {
		t.Fatalf("request_run = %+v", req)
	}
	if text := callOK(t, cs, "list_run_requests", map[string]any{"status": "pending"}, nil); !strings.Contains(text, req.RequestID) {
		t.Fatalf("list_run_requests:\n%s", text)
	}
	if text := callOK(t, cs, "get_run_request", map[string]any{"request_id": req.RequestID}, nil); !strings.Contains(text, `"picture"`) {
		t.Fatalf("get_run_request:\n%s", text)
	}

	// Approve as a person through the service, then cancel the run.
	rid := uuid.MustParse(req.RequestID)
	_, _, fp, err := f.s.Deps.RunRequests.Get(f.scoped, f.org, rid)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := f.s.Deps.RunRequests.Approve(f.scoped, f.org, rid, f.owner, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	group := approved.Request.RunGroupID.String()
	list, _, err := f.s.Deps.Jobs.List(f.scoped, f.org, jobs.Filter{RunGroupID: approved.Request.RunGroupID}, "", 10)
	if err != nil || len(list) == 0 {
		t.Fatalf("jobs of group %s: %v %v", group, list, err)
	}
	callOK(t, cs, "cancel_job", map[string]any{"job_id": list[0].ID.String()}, nil)

	foreign := f.startJob(t, nil)
	if text, isErr := call(t, cs, "cancel_job", map[string]any{"job_id": foreign.String()}); !isErr {
		t.Fatalf("cancelled a run it did not request: %s", text)
	}
}

func TestJobToolsNeedTheirArea(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaJobsRead)
	cs := mustConnect(t, f.s, f.token)
	names := toolNames(t, cs)
	for _, tool := range []string{"list_jobs", "get_job", "get_job_log", "list_run_requests", "get_run_request"} {
		if !names[tool] {
			t.Errorf("%s missing for a jobs.read token", tool)
		}
	}
	for _, tool := range []string{"request_run", "request_retry", "cancel_job"} {
		if names[tool] {
			t.Errorf("%s listed for a jobs.read token", tool)
		}
		if _, isErr := call(t, cs, tool, map[string]any{}); !isErr {
			t.Errorf("%s ran for a jobs.read token", tool)
		}
	}
}

// TestRunRequestShowsFollowUps: the request detail the assistant reads
// says which follow-ups an approval would start, and request_run says the
// organization decides.
func TestRunRequestShowsFollowUps(t *testing.T) {
	f := newOrgFixture(t)
	notify, err := f.s.Deps.Templates.Create(f.scoped, f.org, templates.CreateInput{
		Name: "notify", PlaybookID: f.playbook, Scope: templates.Scope{HostIDs: []uuid.UUID{f.host}},
	})
	if err != nil {
		t.Fatal(err)
	}
	build, err := f.s.Deps.Templates.Create(f.scoped, f.org, templates.CreateInput{
		Name: "build", PlaybookID: f.playbook, Scope: templates.Scope{HostIDs: []uuid.UUID{f.host}}, FollowUps: []uuid.UUID{notify.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	cs := mustConnect(t, f.s, f.token)
	var req struct {
		RequestID string `json:"request_id"`
	}
	callOK(t, cs, "request_run", map[string]any{"template_id": build.ID.String(), "reason": "build"}, &req)
	text := callOK(t, cs, "get_run_request", map[string]any{"request_id": req.RequestID}, nil)
	if !strings.Contains(text, `"follow_ups"`) || !strings.Contains(text, notify.ID.String()) {
		t.Fatalf("get_run_request lacks the follow-ups:\n%s", text)
	}

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	described := false
	for _, tl := range res.Tools {
		if tl.Name == "request_run" {
			described = strings.Contains(tl.Description, "Whether an approval also starts the template's follow-ups is the organization's setting; the request detail says.")
			if !described {
				t.Fatalf("request_run description = %q", tl.Description)
			}
		}
	}
	if !described {
		t.Fatal("request_run is not listed")
	}
}
