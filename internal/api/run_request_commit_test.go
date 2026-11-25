package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/varsource"
)

const (
	syncedA = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	syncedB = "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432"
)

// pictureCommit is the picture's commit as the API serves it.
type pictureCommit struct {
	SHA     string   `json:"sha"`
	Blocked []string `json:"blocked"`
}

func commitOf(t *testing.T, d runRequestDetail) *pictureCommit {
	t.Helper()
	var p struct {
		Commit *pictureCommit `json:"commit"`
	}
	if err := json.Unmarshal(d.Picture, &p); err != nil {
		t.Fatal(err)
	}
	return p.Commit
}

// repoTemplate points the fixture's template at a playbook of a new
// repository and returns the repository.
func (f runRequestFixture) repoTemplate(t *testing.T) repos.Repo {
	t.Helper()
	repo, err := f.s.Deps.Repos.Create(f.scoped, f.orgID, repos.CreateInput{Name: "infra", URL: "https://git.example.com/acme/infra.git"})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := f.s.Deps.Playbooks.CreateRepo(f.scoped, f.orgID, repo.ID, "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	updateTemplate(t, f, func(in *templates.UpdateInput) { in.PlaybookID = pb.ID })
	return repo
}

// recordSync stores what a successful sync of repo read, as its finish
// does: commit, fetched from the repository as it is configured now.
func (f runRequestFixture) recordSync(t *testing.T, repo repos.Repo, commit string) {
	t.Helper()
	raw, err := json.Marshal(varsource.Report{Commit: commit, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Repos.RecordVarSources(f.scoped, f.orgID, repo.ID, time.Now(), raw, nil,
		jobs.SyncedRef(repo.URL, repo.Branch, repo.Subpath), time.Time{}); err != nil {
		t.Fatal(err)
	}
}

// TestApprovedRunRunsTheCommitItsApproverSaw (spec 2026-11-25 A, D3) end
// to end: the picture shows the last synced commit live and why approval
// waits; approval is refused (409) until the repository is synced and
// while a sync is queued; the approved run's job carries the commit, the
// job API shows it, a later sync changes neither the approved picture nor
// the claim, and only a worker that checks out pinned commits claims it,
// with the commit in its repository payload.
func TestApprovedRunRunsTheCommitItsApproverSaw(t *testing.T) {
	f := newRunRequestFixture(t, "rr-commit")
	repo := f.repoTemplate(t)
	created := f.request(t, map[string]any{"reason": "deploy the web tier"})
	id := created.Request.ID

	if c := commitOf(t, created); c == nil || c.SHA != "" || len(c.Blocked) != 1 || c.Blocked[0] != repos.CommitNeverSynced {
		t.Fatalf("commit before any sync = %+v", c)
	}
	resp := f.approve(t, f.adminC, id, created.Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "never been synced") {
		t.Fatalf("approve before any sync = %d %s, want 409", resp.StatusCode, body)
	}

	f.recordSync(t, repo, syncedA)
	d := f.detail(t, id)
	if c := commitOf(t, d); c == nil || c.SHA != syncedA || len(c.Blocked) != 0 {
		t.Fatalf("commit after the sync = %+v, want %s", c, syncedA)
	}

	// A sync queued: the picture says so and the approval waits for it.
	sync := postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/repos/"+repo.ID.String()+"/sync", nil)
	var synced struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if sync.StatusCode != http.StatusAccepted && sync.StatusCode != http.StatusOK && sync.StatusCode != http.StatusCreated {
		t.Fatalf("sync = %d %s", sync.StatusCode, mustReadBody(t, sync))
	}
	decodeInto(t, sync, &synced)
	d = f.detail(t, id)
	if c := commitOf(t, d); c == nil || c.SHA != syncedA || len(c.Blocked) != 1 || c.Blocked[0] != "sync_running" {
		t.Fatalf("commit while a sync is queued = %+v", c)
	}
	resp = f.approve(t, f.adminC, id, d.Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "queued or running") {
		t.Fatalf("approve while a sync is queued = %d %s, want 409", resp.StatusCode, body)
	}
	if _, err := f.s.DB.Pool().Exec(context.Background(),
		`UPDATE jobs SET status = 'cancelled', finished_at = now() WHERE id = $1`, synced.Job.ID); err != nil {
		t.Fatal(err)
	}

	d = f.detail(t, id)
	resp = f.approve(t, f.adminC, id, d.Fingerprint)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var approved struct {
		Request runRequestDTO `json:"request"`
	}
	decodeInto(t, resp, &approved)
	listResp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?group="+approved.Request.RunGroupID, nil)
	var list struct {
		Jobs []struct {
			ID           string `json:"id"`
			PinnedCommit string `json:"pinned_commit"`
		} `json:"jobs"`
	}
	decodeInto(t, listResp, &list)
	if len(list.Jobs) != 1 || list.Jobs[0].PinnedCommit != syncedA {
		t.Fatalf("enqueued jobs = %+v, want one pinned to %s", list.Jobs, syncedA)
	}
	if summary := f.approvalSummary(t, id); !strings.Contains(summary, "; commit "+syncedA) {
		t.Fatalf("approval audit summary = %q, want the commit", summary)
	}
	detailResp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+list.Jobs[0].ID, nil)
	var detail struct {
		Job struct {
			PinnedCommit string `json:"pinned_commit"`
		} `json:"job"`
	}
	decodeInto(t, detailResp, &detail)
	if detail.Job.PinnedCommit != syncedA {
		t.Fatalf("job detail pinned_commit = %q, want %s", detail.Job.PinnedCommit, syncedA)
	}

	// A newer sync after the approval: the approved picture keeps its
	// commit, so the claim guard still passes.
	f.recordSync(t, repo, syncedB)
	if c := commitOf(t, f.detail(t, id)); c == nil || c.SHA != syncedA || len(c.Blocked) != 0 {
		t.Fatalf("approved commit after a newer sync = %+v, want %s", c, syncedA)
	}

	worker := uuid.Must(uuid.NewV7())
	if _, err := f.s.DB.Pool().Exec(context.Background(),
		`INSERT INTO workers (id, org_id, zone_id, name, cert_serial) VALUES ($1, $2, $3, $4, $5)`,
		worker, f.orgID, f.zoneA, "w-pinned", "serial-"+worker.String()); err != nil {
		t.Fatal(err)
	}
	old, err := f.s.Queue.ClaimFor(context.Background(), worker, f.orgID, []uuid.UUID{f.zoneA}, "v1", jobs.ClaimCaps{SupportsCheckMode: true})
	if err != nil || old != nil {
		t.Fatalf("an older worker's claim = %+v %v, want nothing", old, err)
	}
	claim, err := f.s.Queue.ClaimFor(context.Background(), worker, f.orgID, []uuid.UUID{f.zoneA}, "v1", jobs.ClaimCaps{SupportsPinnedCommit: true})
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v %v", claim, err)
	}
	var payload struct {
		Repo struct {
			Commit string `json:"commit"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(claim.Job.GetKindPayload()), &payload); err != nil {
		t.Fatal(err)
	}
	if claim.Job.GetId() != list.Jobs[0].ID || payload.Repo.Commit != syncedA {
		t.Fatalf("claimed job %s with commit %q, want %s with %s", claim.Job.GetId(), payload.Repo.Commit, list.Jobs[0].ID, syncedA)
	}
}

// TestRunRequestWaitsForASyncAfterTheRepositoryChanged: a branch changed
// after the last sync makes the commit stale until the repository is
// synced again; an editor playbook has no commit at all.
func TestRunRequestWaitsForASyncAfterTheRepositoryChanged(t *testing.T) {
	f := newRunRequestFixture(t, "rr-commit-changed")
	created := f.request(t, map[string]any{"reason": "editor playbook"})
	if c := commitOf(t, created); c != nil {
		t.Fatalf("an editor playbook's picture has commit %+v", c)
	}

	repo := f.repoTemplate(t)
	f.recordSync(t, repo, syncedA)
	repo, err := f.s.Deps.Repos.Update(f.scoped, f.orgID, repo.ID, repos.UpdateInput{Name: repo.Name, URL: repo.URL, Branch: "release"})
	if err != nil {
		t.Fatal(err)
	}
	d := f.request(t, map[string]any{"reason": "release"})
	if c := commitOf(t, d); c == nil || c.SHA != syncedA || len(c.Blocked) != 1 || c.Blocked[0] != repos.CommitRepoChanged {
		t.Fatalf("commit after the branch changed = %+v", c)
	}
	resp := f.approve(t, f.adminC, d.Request.ID, d.Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "changed after its last sync") {
		t.Fatalf("approve after the branch changed = %d %s, want 409", resp.StatusCode, body)
	}
	f.recordSync(t, repo, syncedB)
	if c := commitOf(t, f.detail(t, d.Request.ID)); c == nil || c.SHA != syncedB || len(c.Blocked) != 0 {
		t.Fatalf("commit after a new sync = %+v, want %s", c, syncedB)
	}
}
