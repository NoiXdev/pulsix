package mcp_test

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/inventory/schema"
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

// startFailedJob runs the template's job to a failed end that names the
// fixture's host as the failed one, so a retry of it is requestable.
func (f orgFixture) startFailedJob(t *testing.T) uuid.UUID {
	t.Helper()
	id := f.startJob(t, nil)
	if err := f.s.Queue.Finish(context.Background(), id, jobs.StatusFailed, map[string]any{
		jobs.StatsFailedHosts: []any{f.host.String()}, jobs.StatsUnreachableHosts: []any{},
	}, "playbook failed"); err != nil {
		t.Fatal(err)
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

// TestJobLogSearch: get_job_log hands host, task, role and state to the
// REST route, which filters in the database; changed_only answers per host
// the tasks that changed something instead of the log.
func TestJobLogSearch(t *testing.T) {
	f := newOrgFixture(t)
	id := f.startJob(t, []jobs.Event{
		{Seq: 1, Host: "web-1", Task: "common : Install packages", State: "changed", Payload: payload("")},
		{Seq: 2, Host: "web-1", Task: "common : Install packages", State: "changed", Payload: payload("again")},
		{Seq: 3, Host: "web-1", Task: "nginx : Write 100%_config", State: "changed", Payload: payload("")},
		{Seq: 4, Host: "web-2", Task: "nginx : Write 100%_config", State: "ok", Payload: payload("")},
		{Seq: 5, Host: "web-2", Task: "nginx : Restart", State: "unreachable", Payload: json.RawMessage(`{"msg":"gone"}`)},
		{Seq: 6, Host: "web-2", Task: "Ping", State: "changed", Payload: payload("")},
	})
	cs := mustConnect(t, f.s, f.token)
	type logPage struct {
		Log     string              `json:"log"`
		Changed map[string][]string `json:"changed"`
		Next    int64               `json:"next_after"`
	}
	seqs := func(p logPage) []string {
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(p.Log), "\n") {
			if line != "" {
				out = append(out, strings.Fields(line)[0])
			}
		}
		return out
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want []string
		next int64
	}{
		{"task", map[string]any{"task": "100%_"}, []string{"3", "4"}, 4},
		{"role", map[string]any{"role": "NGINX"}, []string{"3", "4", "5"}, 5},
		{"state", map[string]any{"state": []string{"ok", "unreachable"}}, []string{"4", "5"}, 5},
		{"failed_only", map[string]any{"failed_only": true}, []string{"5"}, 5},
		{"combined and limited", map[string]any{"host": "web-1", "state": []string{"changed"}, "limit": 2}, []string{"1", "2"}, 2},
		{"nothing matches", map[string]any{"host": "db-1", "after": 2}, nil, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"job_id": id.String()}
			for k, v := range tc.args {
				args[k] = v
			}
			var p logPage
			callOK(t, cs, "get_job_log", args, &p)
			if got := seqs(p); !slices.Equal(got, tc.want) || p.Next != tc.next {
				t.Fatalf("lines %v next %d, want %v next %d:\n%s", got, p.Next, tc.want, tc.next, p.Log)
			}
		})
	}

	var changed logPage
	callOK(t, cs, "get_job_log", map[string]any{"job_id": id.String(), "changed_only": true}, &changed)
	want := map[string][]string{
		"web-1": {"common : Install packages", "nginx : Write 100%_config"},
		"web-2": {"Ping"},
	}
	if len(changed.Changed) != len(want) || changed.Log != "" || changed.Next != 6 {
		t.Fatalf("changed_only = %+v, want %v next 6", changed, want)
	}
	for host, tasks := range want {
		if !slices.Equal(changed.Changed[host], tasks) {
			t.Fatalf("changed_only[%s] = %v, want %v", host, changed.Changed[host], tasks)
		}
	}
	changed = logPage{}
	callOK(t, cs, "get_job_log", map[string]any{"job_id": id.String(), "changed_only": true, "role": "nginx"}, &changed)
	if len(changed.Changed) != 1 || !slices.Equal(changed.Changed["web-1"], []string{"nginx : Write 100%_config"}) {
		t.Fatalf("changed_only role=nginx = %+v", changed)
	}

	for name, args := range map[string]map[string]any{
		"failed_only and state":        {"failed_only": true, "state": []string{"ok"}},
		"changed_only and state":       {"changed_only": true, "state": []string{"ok"}},
		"changed_only and failed_only": {"changed_only": true, "failed_only": true},
		"unknown state":                {"state": []string{"bogus"}},
	} {
		args["job_id"] = id.String()
		if text, isErr := call(t, cs, "get_job_log", args); !isErr {
			t.Errorf("%s was accepted: %s", name, text)
		}
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
	approved, err := f.s.Deps.RunRequests.Approve(f.scoped, f.org, rid, f.owner, fp, nil, true)
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

// TestRequestRunPassesTheOverrides: request_run hands check_mode, diff,
// extra_vars, host_ids, limit and expires_in to the REST route, whose
// picture then shows them, and the route's refusals reach the assistant.
func TestRequestRunPassesTheOverrides(t *testing.T) {
	f := newOrgFixture(t)
	// A run request sets only the extra vars its template declares.
	cur, err := f.s.Deps.Templates.Get(f.scoped, f.org, f.template)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Templates.Update(f.scoped, f.org, f.template, templates.UpdateInput{
		Name: cur.Name, PlaybookID: cur.PlaybookID, Scope: cur.Scope,
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{Name: "release", Type: schema.TypeString}, {Name: "build", Type: schema.TypeFloat}}},
	}); err != nil {
		t.Fatal(err)
	}
	cs := mustConnect(t, f.s, f.token)
	var req struct {
		RequestID string          `json:"request_id"`
		ExpiresAt time.Time       `json:"expires_at"`
		Picture   json.RawMessage `json:"picture"`
	}
	callOK(t, cs, "request_run", map[string]any{
		"template_id": f.template.String(), "reason": "dry run first", "check_mode": true, "diff": false,
		"extra_vars": json.RawMessage(`{"release":"1.2","build":9007199254740993}`), "host_ids": []string{f.host.String()}, "limit": "web-1", "expires_in": 30,
	}, &req)
	var p struct {
		CheckMode         bool           `json:"check_mode"`
		Diff              bool           `json:"diff"`
		LimitOverride     string         `json:"limit_override"`
		ExtraVarsOverride map[string]any `json:"extra_vars_override"`
		RequestedHostIDs  []string       `json:"requested_host_ids"`
	}
	if err := json.Unmarshal(req.Picture, &p); err != nil {
		t.Fatal(err)
	}
	if !p.CheckMode || p.Diff || p.LimitOverride != "web-1" || p.ExtraVarsOverride["release"] != "1.2" || len(p.RequestedHostIDs) != 1 || p.RequestedHostIDs[0] != f.host.String() {
		t.Fatalf("picture = %s", req.Picture)
	}
	// An integer past 2^53 reaches the request with every digit.
	if !regexp.MustCompile(`"build":\s*9007199254740993\b`).Match(req.Picture) {
		t.Fatalf("picture lost digits: %s", req.Picture)
	}
	r, _, _, err := f.s.Deps.RunRequests.Get(f.scoped, f.org, uuid.MustParse(req.RequestID))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.ExpiresAt.Sub(r.CreatedAt); got != 30*time.Minute {
		t.Fatalf("expires after %s, want 30m", got)
	}

	text, isErr := call(t, cs, "request_run", map[string]any{
		"template_id": f.template.String(), "reason": "x", "host_ids": []string{uuid.Must(uuid.NewV7()).String()},
	})
	if !isErr || !strings.Contains(text, "not among the template's targets") {
		t.Fatalf("foreign host = %v %s, want the refusal", isErr, text)
	}
	// expires_in carries its bounds in the tool's schema.
	for _, n := range []int{4, 1441} {
		if text, isErr := call(t, cs, "request_run", map[string]any{"template_id": f.template.String(), "reason": "x", "expires_in": n}); !isErr || !strings.Contains(text, "expires_in") {
			t.Fatalf("expires_in %d = %v %s, want the schema's refusal", n, isErr, text)
		}
	}
}

// TestGetJobShowsThePinnedCommit (spec 2026-11-25 A): an assistant reads
// which commit an approved run runs.
func TestGetJobShowsThePinnedCommit(t *testing.T) {
	f := newOrgFixture(t)
	created, err := f.s.Deps.Jobs.EnqueueRunWith(f.scoped, f.org, f.template, jobs.RunRequest{Trigger: jobs.TriggerManual, Actor: &f.owner})
	if err != nil || len(created) != 1 {
		t.Fatalf("enqueue: %v %v", created, err)
	}
	const commit = "0123456789abcdef0123456789abcdef01234567"
	if _, err := f.s.DB.Pool().Exec(context.Background(), `UPDATE jobs SET pinned_commit = $2 WHERE id = $1`, created[0].ID, commit); err != nil {
		t.Fatal(err)
	}
	cs := mustConnect(t, f.s, f.token)
	var got struct {
		Job struct {
			PinnedCommit string `json:"pinned_commit"`
		} `json:"job"`
	}
	callOK(t, cs, "get_job", map[string]any{"job_id": created[0].ID.String()}, &got)
	if got.Job.PinnedCommit != commit {
		t.Fatalf("get_job pinned_commit = %q, want %s", got.Job.PinnedCommit, commit)
	}
}

// TestRequestRunSyncsFirst (spec 2026-11-25 A and G): request_run with
// sync_first answers at once with a request waiting for the sync it
// started, and sync_repo hands that same sync back as already running.
func TestRequestRunSyncsFirst(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	pb, err := f.s.Deps.Playbooks.CreateRepo(f.scoped, f.org, f.repo, "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := f.s.Deps.Templates.Create(f.scoped, f.org, templates.CreateInput{
		Name: "deploy-repo", PlaybookID: pb.ID, Scope: templates.Scope{HostIDs: []uuid.UUID{f.host}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		RequestID string `json:"request_id"`
		Status    string `json:"status"`
		ExpiresAt string `json:"expires_at"`
		SyncJobID string `json:"sync_job_id"`
		Note      string `json:"note"`
	}
	callOK(t, cs, "request_run", map[string]any{"template_id": tmpl.ID.String(), "reason": "deploy the fix", "sync_first": true}, &req)
	if req.Status != "waiting_for_sync" || req.ExpiresAt != "" || req.SyncJobID == "" || !strings.Contains(req.Note, "sync") {
		t.Fatalf("request_run = %+v", req)
	}
	var sync struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
		AlreadyRunning bool `json:"already_running"`
	}
	callOK(t, cs, "sync_repo", map[string]any{"repo_id": f.repo.String()}, &sync)
	if !sync.AlreadyRunning || sync.Job.ID != req.SyncJobID {
		t.Fatalf("sync_repo = %+v, want the request's sync %s already running", sync, req.SyncJobID)
	}
}

// waitPauses is the fake pause of a long poll: it counts pauses and runs
// onPause(n) at the n-th, so a test changes the row while the tool waits.
type waitPauses struct {
	calls   int
	onPause func(n int)
}

func (w *waitPauses) sleep(context.Context, time.Duration) error {
	w.calls++
	if w.onPause != nil {
		w.onPause(w.calls)
	}
	return nil
}

func TestGetJobWaitsThroughTheTool(t *testing.T) {
	w := &waitPauses{}
	f := newOrgFixtureWith(t, []apitest.Option{apitest.WithWaitSleep(w.sleep)})
	id := f.startJob(t, nil)
	cs := mustConnect(t, f.s, f.token)
	w.onPause = func(n int) {
		if n == 2 {
			if err := f.s.Queue.Finish(context.Background(), id, jobs.StatusSucceeded, nil, ""); err != nil {
				t.Error(err)
			}
		}
	}
	var job struct {
		Job struct {
			Status string `json:"status"`
		} `json:"job"`
	}
	callOK(t, cs, "get_job", map[string]any{"job_id": id.String(), "wait_seconds": 25}, &job)
	if job.Job.Status != "succeeded" || w.calls != 2 {
		t.Fatalf("status %s after %d pauses, want succeeded after 2", job.Job.Status, w.calls)
	}

	// unless is passed on: the job is no longer running, so no pause.
	w.calls = 0
	callOK(t, cs, "get_job", map[string]any{"job_id": id.String(), "wait_seconds": 25, "unless": "running"}, &job)
	if job.Job.Status != "succeeded" || w.calls != 0 {
		t.Fatalf("unless=running: status %s, pauses %d", job.Job.Status, w.calls)
	}

	// Beyond the cap is refused (the schema's bound, and the API's).
	if _, isErr := call(t, cs, "get_job", map[string]any{"job_id": id.String(), "wait_seconds": 26}); !isErr {
		t.Fatal("wait_seconds 26 was accepted")
	}
}

func TestGetRunRequestWaitsThroughTheTool(t *testing.T) {
	w := &waitPauses{}
	f := newOrgFixtureWith(t, []apitest.Option{apitest.WithWaitSleep(w.sleep)})
	cs := mustConnect(t, f.s, f.token)
	var req struct {
		RequestID string `json:"request_id"`
	}
	callOK(t, cs, "request_run", map[string]any{"template_id": f.template.String(), "reason": "deploy web"}, &req)
	reqID := uuid.MustParse(req.RequestID)
	w.onPause = func(n int) {
		if n == 3 {
			if _, err := f.s.Deps.RunRequests.Reject(f.scoped, f.org, reqID, f.owner); err != nil {
				t.Error(err)
			}
		}
	}
	var got struct {
		Request struct {
			Status string `json:"status"`
		} `json:"request"`
	}
	callOK(t, cs, "get_run_request", map[string]any{"request_id": req.RequestID, "wait_seconds": 10, "unless": "pending"}, &got)
	if got.Request.Status != "rejected" || w.calls != 3 {
		t.Fatalf("status %s after %d pauses, want rejected after 3", got.Request.Status, w.calls)
	}
}

// TestWaitSecondsIsBoundedInTheSchema: get_job and get_run_request declare
// wait_seconds' range, so an assistant sees the cap before it calls.
func TestWaitSecondsIsBoundedInTheSchema(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tl := range res.Tools {
		if tl.Name != "get_job" && tl.Name != "get_run_request" {
			continue
		}
		seen++
		raw, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var s struct {
			Properties map[string]struct {
				Minimum *float64 `json:"minimum"`
				Maximum *float64 `json:"maximum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		p := s.Properties["wait_seconds"]
		if p.Minimum == nil || *p.Minimum != 0 || p.Maximum == nil || *p.Maximum != 25 {
			t.Errorf("%s wait_seconds = %s, want minimum 0 and maximum 25", tl.Name, raw)
		}
	}
	if seen != 2 {
		t.Fatalf("saw %d of the two tools", seen)
	}
}

// TestJobLogChangedOnlyTruncated: changed_only says when its filtered fetch
// hit the limit, so the per-host summary may be incomplete.
func TestJobLogChangedOnlyTruncated(t *testing.T) {
	f := newOrgFixture(t)
	id := f.startJob(t, []jobs.Event{
		{Seq: 1, Host: "web-1", Task: "A", State: "changed", Payload: payload("")},
		{Seq: 2, Host: "web-1", Task: "B", State: "changed", Payload: payload("")},
		{Seq: 3, Host: "web-1", Task: "C", State: "changed", Payload: payload("")},
	})
	cs := mustConnect(t, f.s, f.token)
	for limit, want := range map[int]bool{2: true, 3: true, 4: false} {
		var out struct {
			Truncated bool `json:"truncated"`
		}
		callOK(t, cs, "get_job_log", map[string]any{"job_id": id.String(), "changed_only": true, "limit": limit}, &out)
		if out.Truncated != want {
			t.Errorf("limit %d: truncated = %v, want %v", limit, out.Truncated, want)
		}
	}
}

// TestJobLogDescriptions: the log tool says what an empty page means and
// names the states the service accepts.
func TestJobLogDescriptions(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name != "get_job_log" {
			continue
		}
		raw, _ := json.Marshal(tl)
		for _, want := range []string{"nothing new yet", "run_start", "run_end", "fatal", "failed, unreachable, error and fatal"} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("get_job_log definition lacks %q: %s", want, raw)
			}
		}
	}
}
