package runrequests_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/templates"
)

// fakeCommits answers the repository's last synced commit and records the
// repository it was asked about.
type fakeCommits struct {
	synced repos.SyncedCommit
	err    error
	asked  []repos.Repo
}

func (f *fakeCommits) SyncedCommit(_ context.Context, _ uuid.UUID, repo repos.Repo) (repos.SyncedCommit, error) {
	f.asked = append(f.asked, repo)
	return f.synced, f.err
}

// fakeSyncJobs answers whether a sync runs and the jobs' pins.
type fakeSyncJobs struct {
	active  bool
	pins    map[uuid.UUID]string
	pinErr  error
	asked   []uuid.UUID // repositories HasActiveSync was asked about
	pinCall int
}

func (f *fakeSyncJobs) HasActiveSync(_ context.Context, _, repoID uuid.UUID) (bool, error) {
	f.asked = append(f.asked, repoID)
	return f.active, nil
}

func (f *fakeSyncJobs) PinnedCommit(_ context.Context, _, jobID uuid.UUID) (string, error) {
	f.pinCall++
	if f.pinErr != nil {
		return "", f.pinErr
	}
	return f.pins[jobID], nil
}

// TestPictureShowsTheLiveCommitWhilePending (spec 2026-11-25 A, D3): a
// pending request's picture shows the commit of the repository's last
// successful sync, with every reason an approval is refused now.
func TestPictureShowsTheLiveCommitWhilePending(t *testing.T) {
	for name, tc := range map[string]struct {
		synced repos.SyncedCommit
		active bool
		want   runrequests.PictureCommit
	}{
		"pinnable":     {repos.SyncedCommit{Commit: commitX}, false, runrequests.PictureCommit{SHA: commitX}},
		"never synced": {repos.SyncedCommit{Stale: repos.CommitNeverSynced}, false, runrequests.PictureCommit{Blocked: []string{repos.CommitNeverSynced}}},
		"sync running": {repos.SyncedCommit{Commit: commitX}, true, runrequests.PictureCommit{SHA: commitX, Blocked: []string{runrequests.BlockedSyncRunning}}},
		"changed and running": {repos.SyncedCommit{Commit: commitX, Stale: repos.CommitRepoChanged}, true,
			runrequests.PictureCommit{SHA: commitX, Blocked: []string{repos.CommitRepoChanged, runrequests.BlockedSyncRunning}}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			w.commits.synced, w.syncs.active = tc.synced, tc.active
			p := w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x, Status: runrequests.StatusPending})
			if p.Commit == nil || p.Commit.SHA != tc.want.SHA || !slices.Equal(p.Commit.Blocked, tc.want.Blocked) {
				t.Fatalf("commit = %+v, want %+v", p.Commit, tc.want)
			}
			// The commit is checked against the repository as the picture
			// shows it.
			if len(w.commits.asked) != 1 || w.commits.asked[0].ID != w.repo || w.commits.asked[0].Branch != "main" ||
				len(w.syncs.asked) != 1 || w.syncs.asked[0] != w.repo {
				t.Fatalf("asked about %+v and %v, want the template's repository", w.commits.asked, w.syncs.asked)
			}
		})
	}
}

// TestPictureOfAnApprovedRequestShowsItsStoredCommit: once approved, the
// picture shows the commit the approval stored, whatever the repository
// synced since or is syncing now, so a later sync does not fail the
// approved run. A request approved before pins existed shows none, and
// its picture keeps the fingerprint it was approved with.
func TestPictureOfAnApprovedRequestShowsItsStoredCommit(t *testing.T) {
	w := newWorld()
	w.commits.synced, w.syncs.active = repos.SyncedCommit{Commit: commitY, Stale: repos.CommitRepoChanged}, true
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x, Status: runrequests.StatusApproved, PinnedCommit: commitX}
	p := w.picture(t, req)
	if p.Commit == nil || p.Commit.SHA != commitX || len(p.Commit.Blocked) != 0 {
		t.Fatalf("commit = %+v, want the stored %s and nothing blocking", p.Commit, commitX)
	}
	if len(w.commits.asked) != 0 || len(w.syncs.asked) != 0 {
		t.Fatal("an approved request's picture looked up the live commit")
	}

	legacy := req
	legacy.PinnedCommit = ""
	p = w.picture(t, legacy)
	if p.Commit != nil {
		t.Fatalf("a request approved before pins shows commit %+v", p.Commit)
	}
	doc, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), `"commit"`) {
		t.Fatalf("a picture without a commit writes one: %s", doc)
	}
}

// TestPictureOfAnEditorPlaybookHasNoCommit: nothing to pin, nothing to
// sync first.
func TestPictureOfAnEditorPlaybookHasNoCommit(t *testing.T) {
	w := newWorld()
	inline := "- hosts: all"
	pb := w.pbs[w.pb]
	pb.Source, pb.RepoID, pb.Path, pb.Inline = "editor", nil, nil, &inline
	w.pbs[w.pb] = pb
	p := w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x, Status: runrequests.StatusPending})
	if p.Commit != nil || len(w.commits.asked) != 0 {
		t.Fatalf("commit = %+v, want none", p.Commit)
	}
}

// TestPictureOfARetryShowsTheOriginalJobsCommit: a retry runs its original
// job's commit and shows it, nothing blocking; a retry of a job without
// one shows the live commit with its reasons.
func TestPictureOfARetryShowsTheOriginalJobsCommit(t *testing.T) {
	job := id()
	retry := runrequests.Request{Kind: runrequests.KindRetryFailed, TemplateID: id(), JobID: &job, Status: runrequests.StatusPending}

	w := newWorld()
	retry.TemplateID = w.x
	w.syncs.pins[job], w.syncs.active = commitY, true
	if p := w.picture(t, retry); p.Commit == nil || p.Commit.SHA != commitY || len(p.Commit.Blocked) != 0 ||
		p.Commit.Source != runrequests.CommitFromOriginalRun {
		t.Fatalf("a pinned job's retry shows %+v, want %s from the original run", p.Commit, commitY)
	}
	// Approved, it shows the same, so the claim's picture matches.
	approved := retry
	approved.Status, approved.PinnedCommit = runrequests.StatusApproved, commitY
	if p := w.picture(t, approved); p.Commit == nil || p.Commit.SHA != commitY || p.Commit.Source != runrequests.CommitFromOriginalRun {
		t.Fatalf("an approved retry of a pinned job shows %+v", p.Commit)
	}

	w = newWorld()
	retry.TemplateID = w.x
	w.syncs.active = true
	if p := w.picture(t, retry); p.Commit == nil || p.Commit.SHA != commitX || !slices.Equal(p.Commit.Blocked, []string{runrequests.BlockedSyncRunning}) ||
		p.Commit.Source != "" {
		t.Fatalf("an unpinned job's retry shows %+v, want the live commit", p.Commit)
	}

	// A job that is gone shows the live commit too; another failure fails.
	w = newWorld()
	retry.TemplateID = w.x
	w.syncs.pinErr = jobs.ErrNotFound
	if p := w.picture(t, retry); p.Commit == nil || p.Commit.SHA != commitX {
		t.Fatalf("a gone job's retry shows %+v, want the live commit", p.Commit)
	}
	boom := errors.New("boom")
	w.syncs.pinErr = boom
	if _, err := w.resolver().Picture(context.Background(), id(), retry); !errors.Is(err, boom) {
		t.Fatalf("Picture = %v, want the lookup's failure", err)
	}
}

// TestFingerprintCoversTheCommit: another commit, or a reason more, is
// another picture.
func TestFingerprintCoversTheCommit(t *testing.T) {
	req := id()
	base := runrequests.Picture{TemplateID: "t", Commit: &runrequests.PictureCommit{SHA: commitX}}
	other := base
	other.Commit = &runrequests.PictureCommit{SHA: commitY}
	blocked := base
	blocked.Commit = &runrequests.PictureCommit{SHA: commitX, Blocked: []string{runrequests.BlockedSyncRunning}}
	none := base
	none.Commit = nil
	fps := map[string]bool{}
	for _, p := range []runrequests.Picture{base, other, blocked, none} {
		fps[runrequests.Fingerprint(req, p)] = true
	}
	if len(fps) != 4 {
		t.Fatalf("%d distinct fingerprints of 4 pictures", len(fps))
	}
}

// TestRunAndRetryEnqueueThePin: the approval's commit reaches the enqueue
// of a run and of a retry.
func TestRunAndRetryEnqueueThePin(t *testing.T) {
	ctx, org, actor, group := context.Background(), id(), id(), id()
	w := newWorld()
	rec := &enqueueRecorder{targetRunner: w.jobs}
	res := runrequests.NewResolver(runrequests.Sources{
		Templates: w.tmpls, Playbooks: w.pbs, Repos: w.repos, Jobs: rec, Hosts: w.hosts, SSH: w.ssh,
		Credentials: w.creds, Vars: w.inv, VarsKey: w.key, Settings: w.settings, FollowUpRuns: w.rows, Overrides: w.ovr,
		Commits: w.commits, SyncJobs: w.syncs,
	})
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x, PinnedCommit: commitX}
	if err := res.Run(ctx, org, req, actor, "k", group, []uuid.UUID{}); err != nil {
		t.Fatal(err)
	}
	if rec.enqueued == nil || rec.enqueued.PinnedCommit != commitX {
		t.Fatalf("enqueued = %+v, want the pin %s", rec.enqueued, commitX)
	}

	fx := newResolverFx(fakeTemplates{}, false, nil, nil)
	if err := fx.res.RetryFailed(ctx, fx.org, id(), actor, "k", id(), nil, commitY); err != nil {
		t.Fatal(err)
	}
	if fx.jobs.retryPin != commitY {
		t.Fatalf("retry pinned to %q, want %s", fx.jobs.retryPin, commitY)
	}
}

// TestPictureSaysWhatEachFollowUpRuns (controller ruling, fix round 1): with
// a pinned run, a follow-up whose playbook comes from the same repository
// runs the same commit, one of another repository runs that repository's
// state when it starts, and an editor playbook runs as saved; a follow-up
// below one that is not pinned is not pinned either. A picture without a
// commit says nothing of it, so its fingerprint is unchanged.
func TestPictureSaysWhatEachFollowUpRuns(t *testing.T) {
	w := newWorld()
	w.settings.on = true
	z, editor, otherRepo := id(), id(), id()
	pbSame, pbOther, pbEditor := id(), id(), id()
	path, inline := "db.yml", "- hosts: all"
	w.pbs[pbSame] = playbooks.Playbook{ID: pbSame, Source: "repo", Path: &path, RepoID: &w.repo}
	w.pbs[pbOther] = playbooks.Playbook{ID: pbOther, Source: "repo", Path: &path, RepoID: &otherRepo}
	w.pbs[pbEditor] = playbooks.Playbook{ID: pbEditor, Source: "editor", Inline: &inline}
	w.repos[otherRepo] = repos.Repo{ID: otherRepo, Name: "apps"}
	// X → Y (same repository) → Z (another repository) → Y' (same as X, but
	// below Z), and X → E (editor).
	y2 := id()
	w.tmpls.fakeTemplates[w.y] = templates.Template{ID: w.y, Name: "Y", Revision: 5, PlaybookID: pbSame,
		FollowUps: []templates.FollowUp{{TemplateID: z, Name: "Z"}}}
	w.tmpls.fakeTemplates[z] = templates.Template{ID: z, Name: "Z", Revision: 1, PlaybookID: pbOther,
		FollowUps: []templates.FollowUp{{TemplateID: y2, Name: "Y2"}}}
	w.tmpls.fakeTemplates[y2] = templates.Template{ID: y2, Name: "Y2", Revision: 1, PlaybookID: pbSame}
	w.tmpls.fakeTemplates[editor] = templates.Template{ID: editor, Name: "E", Revision: 1, PlaybookID: pbEditor}
	x := w.tmpls.fakeTemplates[w.x]
	x.FollowUps = append(x.FollowUps, templates.FollowUp{TemplateID: editor, Name: "E"})
	w.tmpls.fakeTemplates[w.x] = x

	p := w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x, Status: runrequests.StatusPending})
	if p.FollowUps == nil || len(p.FollowUps.Templates) != 2 {
		t.Fatalf("follow-ups = %+v", p.FollowUps)
	}
	y, e := p.FollowUps.Templates[0], p.FollowUps.Templates[1]
	if y.Commit != runrequests.FollowUpPinned || y.Repo != "" {
		t.Fatalf("Y = %+v, want the pinned commit", y)
	}
	if e.Commit != runrequests.FollowUpEditor {
		t.Fatalf("E = %+v, want an editor playbook", e)
	}
	if len(y.FollowUps) != 1 || y.FollowUps[0].Commit != runrequests.FollowUpCurrent || y.FollowUps[0].Repo != "apps" {
		t.Fatalf("Z = %+v, want the current state of apps", y.FollowUps)
	}
	if zz := y.FollowUps[0].FollowUps; len(zz) != 1 || zz[0].Commit != runrequests.FollowUpCurrent || zz[0].Repo != "infra" {
		t.Fatalf("Y2 = %+v, want the current state of infra", zz)
	}

	// Approved before pins: no commit, no follow-up annotations.
	legacy := w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x, Status: runrequests.StatusApproved})
	doc, err := json.Marshal(legacy.FollowUps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), `"commit"`) || strings.Contains(string(doc), `"repo"`) {
		t.Fatalf("an unpinned picture's follow-ups say what they run: %s", doc)
	}
}
