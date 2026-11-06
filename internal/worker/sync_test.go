// The sync_repo executor's tests: a fake fetcher for the executor's own
// contract (the claim, the fetch, the result event). Its requirements step
// is in sync_requirements_test.go; the real runner fetch against a local
// git fixture lives in system_e2e_test.go.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// fakeFetcher is the RepoFetcher fake: it returns a canned directory and
// observed key or error, recording the spec it received, and a canned
// commit (commitWaits: only once the context it is handed has ended).
type fakeFetcher struct {
	dir         string
	observed    *GitHostKey
	err         error
	calls       int
	specs       []RepoSpec
	commit      string
	commitErr   error
	commitWaits bool
	onCommit    func()
	commitDirs  []string
}

func (f *fakeFetcher) FetchRepoDir(_ context.Context, spec RepoSpec, _, _, _ string) (string, *GitHostKey, error) {
	f.calls++
	f.specs = append(f.specs, spec)
	return f.dir, f.observed, f.err
}

func (f *fakeFetcher) HeadCommit(ctx context.Context, dir string) (string, error) {
	f.commitDirs = append(f.commitDirs, dir)
	if f.onCommit != nil {
		f.onCommit()
	}
	if f.commitWaits {
		<-ctx.Done()
	}
	return f.commit, f.commitErr
}

// syncClaim is the claim the manager's enriched sync branch ships.
func syncClaim(t *testing.T, repoID, url, branch, subpath, gitSecret string) *workerpb.Job {
	t.Helper()
	job := sysJob(t, "sync_repo", sysPayload(t, map[string]any{
		"repo": map[string]any{"id": repoID, "url": url, "branch": branch, "subpath": subpath},
	}), nil)
	if gitSecret != "" {
		job.Credentials = map[string]string{"git:" + repoID: gitSecret}
	}
	return job
}

func TestSyncExecutorWithoutRequirementsSucceeds(t *testing.T) {
	clone := t.TempDir()
	f := &fakeFetcher{dir: clone}
	c := &evCollector{}
	status, stats, err := (&SyncExecutor{Fetcher: f}).Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "", ""), RunEnv{WorkDir: t.TempDir()}, c.sink)
	if err != nil || status != StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}
	if stats["repo_id"] != "01900000-0000-7000-8000-000000000123" || stats["requirements_installed"] != false {
		t.Fatalf("stats = %v", stats)
	}
	// The one ok-event carries the same report for the run view.
	if len(c.events) != 1 || c.events[0].GetState() != "ok" {
		t.Fatalf("events = %+v", c.events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(c.events[0].GetPayload()), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["repo_id"] != stats["repo_id"] || payload["requirements_installed"] != false {
		t.Fatalf("event payload = %v", payload)
	}
}

func TestSyncExecutorFetchFailureFailsTheJob(t *testing.T) {
	f := &fakeFetcher{err: errors.New("git clone: exit 128: [repo url]")}
	status, stats, err := (&SyncExecutor{Fetcher: f}).Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "", ""), RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err == nil || status != StatusError {
		t.Fatalf("status = %q err = %v", status, err)
	}
	if !strings.Contains(err.Error(), "fetch repository") {
		t.Errorf("error = %v", err)
	}
	// A failed fetch read nothing: no sources, so the manager keeps the
	// previous record.
	if _, sent := stats[VarSourcesStatsKey]; sent {
		t.Errorf("stats = %v, want no %s", stats, VarSourcesStatsKey)
	}
}

func TestSyncExecutorBrokenPayloadsFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{"not json", `{oops`, "kind payload"},
		{"no repo", `{}`, "no repository"},
		{"no url", `{"repo":{"id":"x","branch":"main"}}`, "no repository"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, _, err := (&SyncExecutor{Fetcher: &fakeFetcher{}}).Run(context.Background(),
				sysJob(t, "sync_repo", c.payload, nil), RunEnv{}, (&evCollector{}).sink)
			if err == nil || status != StatusError {
				t.Fatalf("status = %q err = %v", status, err)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %v, want it to name %q", err, c.wantErr)
			}
		})
	}
}

// TestSyncExecutorUnwiredFetcherFailsClosed pins the wiring-bug posture.
func TestSyncExecutorUnwiredFetcherFailsClosed(t *testing.T) {
	_, _, err := (&SyncExecutor{}).Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://x", "main", "", ""), RunEnv{}, (&evCollector{}).sink)
	if err == nil || !strings.Contains(err.Error(), "no repo fetcher wired") {
		t.Fatalf("err = %v", err)
	}
}

// TestSyncExecutorUnknownKindIsAnError mirrors every other executor.
func TestSyncExecutorUnknownKindIsAnError(t *testing.T) {
	status, _, err := (&SyncExecutor{Fetcher: &fakeFetcher{}}).Run(context.Background(),
		sysJob(t, "run", `{}`, nil), RunEnv{}, (&evCollector{}).sink)
	if err == nil || status != StatusError {
		t.Fatalf("status = %q err = %v", status, err)
	}
}

// TestSyncExecutorShipsTheGitServerPin proves the claim's pin reaches the
// fetcher's spec (backlog 2.41) and the observed key reaches the result —
// the manager's TOFU input — only for an unpinned repository.
func TestSyncExecutorShipsTheGitServerPin(t *testing.T) {
	pinnedClaim := sysJob(t, "sync_repo", sysPayload(t, map[string]any{
		"repo": map[string]any{"id": "01900000-0000-7000-8000-000000000123",
			"url": "ssh://git.example.com/x.git", "branch": "main",
			"host_key": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "host_public_key": "ssh-ed25519 AAAA pinned"},
	}), nil)

	// Pinned: the spec carries the pin and the pinned-and-verified report
	// names no key (the fetch enforced it itself).
	f := &fakeFetcher{dir: t.TempDir()}
	c := &evCollector{}
	stats, err := runSync(t, f, pinnedClaim, c)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.specs) != 1 || f.specs[0].HostKey != "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" ||
		f.specs[0].HostPublicKey != "ssh-ed25519 AAAA pinned" {
		t.Fatalf("fetcher specs = %+v", f.specs)
	}
	if _, carried := stats["host_key"]; carried {
		t.Fatalf("a pinned-and-verified sync reports no host key: %v", stats)
	}
	// ... and the ok event's payload is empty of it too.
	payload := c.events[0].GetPayload()
	if strings.Contains(payload, "host_key") {
		t.Fatalf("event payload = %s", payload)
	}

	// Unpinned with an observation: the observed key is the report.
	observed := &GitHostKey{Fingerprint: "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", PublicKey: "ssh-ed25519 AAAA observed"}
	f = &fakeFetcher{dir: t.TempDir(), observed: observed}
	c = &evCollector{}
	stats, err = runSync(t, f, sysJob(t, "sync_repo", sysPayload(t, map[string]any{
		"repo": map[string]any{"id": "01900000-0000-7000-8000-000000000123", "url": "https://git.example.com/x.git", "branch": "main"},
	}), nil), c)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats["host_key"] != observed.Fingerprint || stats["host_public_key"] != observed.PublicKey {
		t.Fatalf("stats = %v", stats)
	}
	if !strings.Contains(c.events[0].GetPayload(), `"host_key":"`+observed.Fingerprint+`"`) {
		t.Fatalf("event payload = %s", c.events[0].GetPayload())
	}
}

// runSync is the executor's one-shot: Run with the collector's sink.
func runSync(t *testing.T, f *fakeFetcher, job *workerpb.Job, c *evCollector) (map[string]any, error) {
	t.Helper()
	status, stats, err := (&SyncExecutor{Fetcher: f}).Run(context.Background(), job, RunEnv{WorkDir: t.TempDir()}, c.sink)
	if err != nil || status != StatusSucceeded {
		return stats, fmt.Errorf("Run = %q, %v", status, err)
	}
	return stats, nil
}
