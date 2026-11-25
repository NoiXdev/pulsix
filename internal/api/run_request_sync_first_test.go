package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// syncFirstDTO is the request as a sync_first caller reads it.
type syncFirstDTO struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at"`
	SyncJobID string     `json:"sync_job_id"`
	SyncError string     `json:"sync_error"`
}

type syncFirstDetail struct {
	Request     syncFirstDTO    `json:"request"`
	Picture     json.RawMessage `json:"picture"`
	Fingerprint string          `json:"fingerprint"`
}

// syncToken is an assistant token that may also sync repositories.
func (f runRequestFixture) syncToken(t *testing.T) string {
	t.Helper()
	operator := f.memberID(t, f.orgSlug+"-operator@example.test")
	return f.s.AssistantTokenFor(t, f.orgID, operator, tenancy.RoleOperator,
		tenancy.AreaJobsRequest, tenancy.AreaTemplatesRead, tenancy.AreaReposWrite)
}

func (f runRequestFixture) syncFirst(t *testing.T, tok string) syncFirstDetail {
	t.Helper()
	resp := f.s.AssistantDo(t, http.MethodPost, tok, f.orgPath()+"/job-templates/"+f.templateID.String()+"/run-requests", "",
		map[string]any{"reason": "deploy the fix", "sync_first": true, "expires_in": 15})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create sync_first request = %d %s", resp.Code, resp.Body)
	}
	var out syncFirstDetail
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f runRequestFixture) syncFirstDetail(t *testing.T, id string) syncFirstDetail {
	t.Helper()
	resp := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+id, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out syncFirstDetail
	decodeInto(t, resp, &out)
	return out
}

type repoSyncAnswer struct {
	Job struct {
		ID     string `json:"id"`
		ZoneID string `json:"zone_id"`
		Status string `json:"status"`
	} `json:"job"`
	AlreadyRunning bool `json:"already_running"`
}

func (f runRequestFixture) syncRepo(t *testing.T, repo repos.Repo) (int, repoSyncAnswer) {
	t.Helper()
	resp := postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/repos/"+repo.ID.String()+"/sync", nil)
	var out repoSyncAnswer
	code := resp.StatusCode
	decodeInto(t, resp, &out)
	return code, out
}

// finishSync drives the sync job to status through the real queue: claim
// and finish, as a worker does.
func (f runRequestFixture) finishSync(t *testing.T, jobID string, status jobs.Status, errText string) {
	t.Helper()
	id := uuid.MustParse(jobID)
	j, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, id)
	if err != nil {
		t.Fatal(err)
	}
	finishJob(t, f.s, j.ZoneID, id, status, nil, errText)
}

// TestRunRequestSyncFirst (spec 2026-11-25 A, sync_first) end to end: the
// request is created at once, waiting for a sync of its repository that it
// started; a person's sync of the repository returns that same job, and a
// second request attaches to it; nobody can approve while it waits, and the
// badge does not count it; when the sync succeeds every waiting request
// becomes pending with its expiry starting then and the new commit in its
// picture, and is approved as any.
func TestRunRequestSyncFirst(t *testing.T) {
	f := newRunRequestFixture(t, "rr-sync-first")
	repo := f.repoTemplate(t)
	f.recordSync(t, repo, syncedA)
	tok := f.syncToken(t)

	created := f.syncFirst(t, tok)
	r := created.Request
	if r.Status != "waiting_for_sync" || r.ExpiresAt != nil || r.SyncJobID == "" {
		t.Fatalf("created = %+v", r)
	}
	code, sync := f.syncRepo(t, repo)
	if code != http.StatusOK || !sync.AlreadyRunning || sync.Job.ID != r.SyncJobID {
		t.Fatalf("sync while the request's is queued = %d %+v, want 200 with job %s", code, sync, r.SyncJobID)
	}
	// The sync the request queued is audited like a sync_repo call, as the
	// requesting token.
	if got := f.syncAudits(t, repo); len(got) != 2 || !strings.Contains(got[0], "sync first for run request "+r.ID) ||
		!strings.Contains(got[1], "was already queued") {
		t.Fatalf("sync audit events = %q", got)
	}
	second := f.syncFirst(t, tok)
	if second.Request.Status != "waiting_for_sync" || second.Request.SyncJobID != r.SyncJobID {
		t.Fatalf("second request = %+v, want it on sync %s", second.Request, r.SyncJobID)
	}

	resp := f.approve(t, f.adminC, r.ID, created.Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "waits for its repository's sync") {
		t.Fatalf("approve while waiting = %d %s, want 409", resp.StatusCode, body)
	}
	if n := f.pendingCount(t, f.adminC, f.orgSlug); n != 0 {
		t.Fatalf("badge while waiting = %d, want 0", n)
	}
	list := f.s.AssistantDo(t, http.MethodGet, tok, f.orgPath()+"/run-requests?status=waiting_for_sync", "", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), r.ID) || !strings.Contains(list.Body.String(), second.Request.ID) {
		t.Fatalf("list waiting = %d %s", list.Code, list.Body)
	}

	f.recordSync(t, repo, syncedB) // what the sync reads; this harness wires no source sink
	before := time.Now()
	f.finishSync(t, r.SyncJobID, jobs.StatusSucceeded, "")
	after := time.Now()
	for _, id := range []string{r.ID, second.Request.ID} {
		d := f.syncFirstDetail(t, id)
		exp := d.Request.ExpiresAt
		if d.Request.Status != "pending" || exp == nil || exp.Before(before.Add(15*time.Minute).Truncate(time.Second)) ||
			exp.After(after.Add(15*time.Minute)) {
			t.Fatalf("after the sync = %+v (finished between %v and %v)", d.Request, before, after)
		}
		var p struct {
			Commit *pictureCommit `json:"commit"`
		}
		if err := json.Unmarshal(d.Picture, &p); err != nil || p.Commit == nil || p.Commit.SHA != syncedB || len(p.Commit.Blocked) != 0 {
			t.Fatalf("picture commit after the sync = %+v %v, want %s", p.Commit, err, syncedB)
		}
	}
	d := f.syncFirstDetail(t, r.ID)
	if resp := f.approve(t, f.adminC, r.ID, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve after the sync = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if n := f.pendingCount(t, f.adminC, f.orgSlug); n != 1 {
		t.Fatalf("badge after the sync = %d, want the second request", n)
	}
}

// TestRunRequestSyncFirstFails: a sync that fails fails its waiting
// request with the sync's error, which then cannot be approved.
func TestRunRequestSyncFirstFails(t *testing.T) {
	f := newRunRequestFixture(t, "rr-sync-first-fail")
	f.repoTemplate(t)
	created := f.syncFirst(t, f.syncToken(t))
	f.finishSync(t, created.Request.SyncJobID, jobs.StatusFailed, "git fetch: authentication required")
	d := f.syncFirstDetail(t, created.Request.ID)
	if d.Request.Status != "failed" || !strings.Contains(d.Request.SyncError, "git fetch: authentication required") {
		t.Fatalf("after the failed sync = %+v", d.Request)
	}
	resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "did not succeed") {
		t.Fatalf("approve a failed request = %d %s, want 409", resp.StatusCode, body)
	}
}

// TestRunRequestSyncFirstRules: sync_first needs the right to sync
// repositories; a template whose playbook comes from no repository has
// nothing to sync and its request is pending at once; the sweep moves a
// request whose sync was cancelled while queued (which does not pass the
// sync's finish).
func TestRunRequestSyncFirstRules(t *testing.T) {
	f := newRunRequestFixture(t, "rr-sync-first-rules")
	resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/job-templates/"+f.templateID.String()+"/run-requests", "",
		map[string]any{"reason": "deploy", "sync_first": true})
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "repos.write") {
		t.Fatalf("sync_first without repos.write = %d %s, want 403", resp.Code, resp.Body)
	}

	tok := f.syncToken(t)
	editor := f.syncFirst(t, tok) // the fixture's template runs an editor playbook
	if editor.Request.Status != "pending" || editor.Request.SyncJobID != "" || editor.Request.ExpiresAt == nil {
		t.Fatalf("editor playbook = %+v, want pending at once", editor.Request)
	}

	repo := f.repoTemplate(t)
	waiting := f.syncFirst(t, tok)
	if code, cancel := f.cancelJob(t, waiting.Request.SyncJobID); code != http.StatusOK && code != http.StatusAccepted {
		t.Fatalf("cancel the queued sync = %d %s", code, cancel)
	}
	if err := f.s.Deps.RunRequests.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := f.syncFirstDetail(t, waiting.Request.ID); d.Request.Status != "failed" || !strings.Contains(d.Request.SyncError, "cancelled") {
		t.Fatalf("after the cancelled sync = %+v", d.Request)
	}
	// The next sync is a new one.
	if code, sync := f.syncRepo(t, repo); code != http.StatusAccepted || sync.AlreadyRunning || sync.Job.ID == waiting.Request.SyncJobID {
		t.Fatalf("sync after the cancel = %d %+v", code, sync)
	}
}

// syncAudits are the summaries of repo's sync events, oldest first.
func (f runRequestFixture) syncAudits(t *testing.T, repo repos.Repo) []string {
	t.Helper()
	rows, err := f.s.DB.Pool().Query(context.Background(),
		`SELECT summary FROM audit_events WHERE action = 'POST syncRepo' AND object_type = 'repo' AND object_id = $1 ORDER BY at, id`, repo.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// TestRunRequestSyncFirstWantsAFreshFetch (controller ruling on the fresh
// head): a sync a worker already took may have fetched before the change
// the assistant wants, so a sync_first request queues a new sync behind
// it and waits for that one; the next caller attaches to the queued one.
// A sync_first start without a default zone is a 409, as on the sync route.
func TestRunRequestSyncFirstWantsAFreshFetch(t *testing.T) {
	f := newRunRequestFixture(t, "rr-sync-first-fresh")
	repo := f.repoTemplate(t)
	tok := f.syncToken(t)
	code, running := f.syncRepo(t, repo)
	if code != http.StatusAccepted || running.AlreadyRunning {
		t.Fatalf("first sync = %d %+v", code, running)
	}
	zone := uuid.MustParse(running.Job.ZoneID)
	claim, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{zone}, "v1")
	if err != nil || claim == nil || claim.Job.GetId() != running.Job.ID {
		t.Fatalf("claim the sync = %+v %v", claim, err)
	}
	created := f.syncFirst(t, tok)
	if created.Request.Status != "waiting_for_sync" || created.Request.SyncJobID == "" || created.Request.SyncJobID == running.Job.ID {
		t.Fatalf("request while a sync runs = %+v, want a new sync", created.Request)
	}
	if code, again := f.syncRepo(t, repo); code != http.StatusOK || !again.AlreadyRunning || again.Job.ID != created.Request.SyncJobID {
		t.Fatalf("sync with one queued behind the running one = %d %+v", code, again)
	}
	// The running sync ending moves nothing: the request waits for its own.
	if err := f.s.Queue.Finish(context.Background(), uuid.MustParse(running.Job.ID), jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	if d := f.syncFirstDetail(t, created.Request.ID); d.Request.Status != "waiting_for_sync" {
		t.Fatalf("after the older sync = %+v", d.Request)
	}

	if _, err := f.s.DB.Pool().Exec(context.Background(), `UPDATE zones SET slug = 'not-default' WHERE org_id = $1 AND slug = 'default'`, f.orgID); err != nil {
		t.Fatal(err)
	}
	resp := f.s.AssistantDo(t, http.MethodPost, tok, f.orgPath()+"/job-templates/"+f.templateID.String()+"/run-requests", "",
		map[string]any{"reason": "deploy", "sync_first": true})
	if resp.Code != http.StatusConflict || !strings.Contains(resp.Body.String(), "no default zone") {
		t.Fatalf("sync_first without a default zone = %d %s, want 409", resp.Code, resp.Body)
	}
}

func (f runRequestFixture) cancelJob(t *testing.T, id string) (int, string) {
	t.Helper()
	resp := postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/jobs/"+id+"/cancel", nil)
	return resp.StatusCode, mustReadBody(t, resp)
}

// TestSyncRouteDeclaresTheQueuedSync: the OpenAPI document declares both
// answers of the sync route, the new sync (202) and the one already queued
// (200), with the same body.
func TestSyncRouteDeclaresTheQueuedSync(t *testing.T) {
	raw, err := api.Document()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	res := doc.Paths["/api/v1/orgs/{org}/repos/{id}/sync"]["post"].Responses
	ok, accepted := res["200"].Content["application/json"].Schema.Ref, res["202"].Content["application/json"].Schema.Ref
	if ok == "" || ok != accepted {
		t.Fatalf("sync responses: 200 %q, 202 %q", ok, accepted)
	}
}
