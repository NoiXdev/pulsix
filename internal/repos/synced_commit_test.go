package repos_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

// TestSyncedCommitSaysWhetherARunMayPinIt (spec 2026-11-25 A, D3): the
// commit of the repository's last successful sync, and why a run cannot
// be pinned to it: never synced, synced by a worker that reports no
// commit, synced before the sync recorded what it fetched, or the URL,
// branch or subpath changed after the sync.
func TestSyncedCommitSaysWhetherARunMayPinIt(t *testing.T) {
	d := testdb.Open(t)
	svc := repos.NewService(repos.NewSQLStore(d), nil, nil)
	org := seedOrg(t, gen.New(d.Pool()), "synced-commit-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedSourcesRepo(t, d, org, "infra")
	repo, err := svc.Get(scoped, org, repoID)
	if err != nil {
		t.Fatal(err)
	}
	ref := jobs.SyncedRef(repo.URL, repo.Branch, repo.Subpath)
	at := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	report := func(commit string, failed bool) []byte {
		return encodeReport(t, varsource.Report{Commit: commit, Failed: failed, Playbooks: []varsource.Playbook{playbookSource("site.yml")}, Notes: []varsource.Note{}})
	}
	record := func(raw []byte, syncedRef string) {
		t.Helper()
		at = at.Add(time.Minute)
		if _, err := svc.RecordVarSources(scoped, org, repoID, at, raw, nil, syncedRef, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(step string, want repos.SyncedCommit) {
		t.Helper()
		current, err := svc.Get(scoped, org, repoID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.SyncedCommit(scoped, org, current)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if got != want {
			t.Fatalf("%s: SyncedCommit = %+v, want %+v", step, got, want)
		}
	}
	setBranch := func(branch string) {
		t.Helper()
		if _, err := d.Exec(scoped, `UPDATE git_repos SET branch = $2 WHERE id = $1`, repoID, branch); err != nil {
			t.Fatal(err)
		}
	}

	expect("never synced", repos.SyncedCommit{Stale: repos.CommitNeverSynced})

	record(report(commitA, false), "")
	expect("synced before the record said what it fetched", repos.SyncedCommit{Commit: commitA, Stale: repos.CommitSyncUnrecorded})

	record(report(commitA, false), ref)
	expect("synced", repos.SyncedCommit{Commit: commitA})

	setBranch("dev")
	expect("branch changed after the sync", repos.SyncedCommit{Commit: commitA, Stale: repos.CommitRepoChanged})
	setBranch(repo.Branch)

	// A read that kept the previous commit keeps what that commit was
	// fetched from: a failed read, and one without a commit.
	other := jobs.SyncedRef(repo.URL, "dev", repo.Subpath)
	record(report(commitB, true), other)
	expect("after a failed read", repos.SyncedCommit{Commit: commitA})
	record(report("", false), other)
	expect("after a read without a commit", repos.SyncedCommit{Commit: commitA})

	record(report(commitB, false), ref)
	expect("synced again", repos.SyncedCommit{Commit: commitB})

	// A worker that reads no sources reports no commit: the record's is an
	// older sync's.
	record(nil, ref)
	expect("synced by an older worker", repos.SyncedCommit{Commit: commitB, Stale: repos.CommitNotReported})
}
