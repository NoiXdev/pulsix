package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TestRepoShowsTheLastSyncedCommit: the repository reads back the commit
// and time of its last successful sync (from the variable sources record),
// and neither is there while no sync has recorded one.
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
	if _, ok := before["last_synced_commit"]; ok {
		t.Fatalf("last_synced_commit before any sync: %v", before)
	}
	if _, ok := before["last_synced_at"]; ok {
		t.Fatalf("last_synced_at before any sync: %v", before)
	}

	start := time.Now().Add(-time.Second)
	f.recordSync(t, repo, syncedA)
	after := read()
	if after["last_synced_commit"] != syncedA {
		t.Fatalf("last_synced_commit = %v, want %s", after["last_synced_commit"], syncedA)
	}
	at, err := time.Parse(time.RFC3339Nano, after["last_synced_at"].(string))
	if err != nil || at.Before(start) {
		t.Fatalf("last_synced_at = %v (%v), want after %v", after["last_synced_at"], err, start)
	}
}
