package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

// recordSyncAt stores what a successful sync of repo read (commit "" for a
// read that found none), claimed at claimed.
func (f runRequestFixture) recordSyncAt(t *testing.T, repo repos.Repo, commit string, claimed time.Time) {
	t.Helper()
	raw, err := json.Marshal(varsource.Report{Commit: commit, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Repos.RecordVarSources(f.scoped, f.orgID, repo.ID, time.Now(), raw, nil,
		jobs.SyncedRef(repo.URL, repo.Branch, repo.Subpath), claimed); err != nil {
		t.Fatal(err)
	}
}

// TestRepoShowsTheLastSyncedCommit: the repository reads back the commit of
// its last successful sync and the time of the sync that fetched it, says
// whether a run can be pinned to it, and shows nothing while no sync has
// recorded a commit.
func TestRepoShowsTheLastSyncedCommit(t *testing.T) {
	f := newRunRequestFixture(t, "repo-synced")
	repo := f.repoTemplate(t)
	url := f.s.HTTP.URL + f.orgPath() + "/repos/" + repo.ID.String()
	read := func() map[string]any {
		t.Helper()
		resp := requestJSON(t, f.adminC, http.MethodGet, url, nil)
		body := mustReadBody(t, resp)
		var out struct {
			Repo map[string]any `json:"repo"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil {
			t.Fatalf("get repo = %d %s", resp.StatusCode, body)
		}
		return out.Repo
	}
	before := read()
	for _, k := range []string{"last_synced_commit", "last_synced_at", "last_synced_commit_current", "last_synced_commit_stale"} {
		if _, ok := before[k]; ok {
			t.Fatalf("%s before any sync: %v", k, before)
		}
	}

	first := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	f.recordSyncAt(t, repo, syncedA, first)
	after := read()
	if after["last_synced_commit"] != syncedA || after["last_synced_commit_current"] != true {
		t.Fatalf("after the sync = %v, want %s, current", after, syncedA)
	}
	if _, ok := after["last_synced_commit_stale"]; ok {
		t.Fatalf("a current commit carries a stale reason: %v", after)
	}
	if at, err := time.Parse(time.RFC3339Nano, after["last_synced_at"].(string)); err != nil || !at.Equal(first) {
		t.Fatalf("last_synced_at = %v (%v), want %v", after["last_synced_at"], err, first)
	}

	// A later sync that read no commit keeps the commit, and the time stays
	// that of the sync that fetched it.
	f.recordSyncAt(t, repo, "", first.Add(30*time.Minute))
	kept := read()
	if kept["last_synced_commit"] != syncedA {
		t.Fatalf("commit after a read without one = %v", kept["last_synced_commit"])
	}
	if at, err := time.Parse(time.RFC3339Nano, kept["last_synced_at"].(string)); err != nil || !at.Equal(first) {
		t.Fatalf("last_synced_at after a later read = %v (%v), want still %v", kept["last_synced_at"], err, first)
	}

	// Pointing the repository elsewhere: the commit no longer stands for it.
	resp := requestJSON(t, f.adminC, http.MethodPut, url, map[string]any{"name": repo.Name, "url": "https://git.example.com/acme/other.git", "branch": repo.Branch})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update repo = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	stale := read()
	if stale["last_synced_commit"] != syncedA || stale["last_synced_commit_current"] != false || stale["last_synced_commit_stale"] != repos.CommitRepoChanged {
		t.Fatalf("after the URL changed = %v, want %s not current (%s)", stale, syncedA, repos.CommitRepoChanged)
	}
}
