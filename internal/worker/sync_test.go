// The sync_repo executor's tests: a fake fetcher for the executor's own
// contract (requirements discovery, the galaxy invocation, the result
// event). The real runner fetch against a local git fixture lives in
// system_e2e_test.go.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// fakeFetcher is the RepoFetcher fake: it returns a canned directory and
// observed key or error, recording the spec it received.
type fakeFetcher struct {
	dir      string
	observed *GitHostKey
	err      error
	calls    int
	specs    []RepoSpec
}

func (f *fakeFetcher) FetchRepoDir(_ context.Context, spec RepoSpec, _, _, _ string) (string, *GitHostKey, error) {
	f.calls++
	f.specs = append(f.specs, spec)
	return f.dir, f.observed, f.err
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

func TestSyncExecutorInstallsRequirementsWhenPresent(t *testing.T) {
	clone := t.TempDir()
	// requirements.yml in the repo's subpath, and the root's absent: the
	// subpath's file wins.
	if err := os.MkdirAll(clone+"/ansible", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clone+"/ansible/requirements.yml", []byte("- src: community.general\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeFetcher{dir: clone}
	c := &evCollector{}
	status, stats, err := (&SyncExecutor{Fetcher: f, GalaxyBin: fakeGalaxyOK(t)}).Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "ansible", ""), RunEnv{WorkDir: t.TempDir()}, c.sink)
	if err != nil || status != StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}
	if stats["requirements_installed"] != true {
		t.Fatalf("stats = %v", stats)
	}
}

// fakeGalaxyOK writes a galaxy binary that exits 0 without network.
func fakeGalaxyOK(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/ansible-galaxy"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSyncExecutorGalaxyInvocationIsPinned(t *testing.T) {
	clone := t.TempDir()
	if err := os.WriteFile(clone+"/requirements.yml", []byte("- src: community.general\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeFetcher{dir: clone}
	// The fake galaxy dumps its argv next to itself (the temp dir trick
	// above writes into its own TempDir; record via a fixed file here).
	argsPath := t.TempDir() + "/galaxy-args"
	galaxy := t.TempDir() + "/ansible-galaxy"
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\nexit 0\n"
	if err := os.WriteFile(galaxy, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	c := &evCollector{}
	if _, _, err := (&SyncExecutor{Fetcher: f, GalaxyBin: galaxy}).Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "", ""), RunEnv{WorkDir: t.TempDir()}, c.sink); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"install", "-r", clone + "/requirements.yml", "-p", clone + ".galaxy"}
	var got []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			got = append(got, l)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("galaxy args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("galaxy arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSyncExecutorGalaxyKeepsSSHStateInTheRunDir is v0.2c final re-review
// out-of-scope 2 for galaxy's git sources: ssh ignores $HOME, so the git
// galaxy starts gets a GIT_SSH_COMMAND naming the run's own known_hosts,
// and galaxy's HOME is the run's too.
func TestSyncExecutorGalaxyKeepsSSHStateInTheRunDir(t *testing.T) {
	clone := t.TempDir()
	if err := os.WriteFile(clone+"/requirements.yml", []byte("- src: git+ssh://git@git.example.org/r.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	envPath := t.TempDir() + "/galaxy-env"
	galaxy := t.TempDir() + "/ansible-galaxy"
	if err := os.WriteFile(galaxy, []byte("#!/bin/sh\nenv > \""+envPath+"\"\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	if _, _, err := (&SyncExecutor{Fetcher: &fakeFetcher{dir: clone}, GalaxyBin: galaxy}).Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "", ""), RunEnv{WorkDir: workDir}, (&evCollector{}).sink); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, l := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			env[k] = v
		}
	}
	knownHosts := filepath.Join(workDir, "home", ".ssh", "known_hosts")
	if !strings.Contains(env["GIT_SSH_COMMAND"], "UserKnownHostsFile='"+knownHosts+"'") {
		t.Errorf("GIT_SSH_COMMAND = %q, want the run's known_hosts %s", env["GIT_SSH_COMMAND"], knownHosts)
	}
	if env["HOME"] != filepath.Join(workDir, "home") {
		t.Errorf("HOME = %q, want the run's home", env["HOME"])
	}
	if fi, err := os.Stat(filepath.Dir(knownHosts)); err != nil || !fi.IsDir() {
		t.Errorf("the run's ssh dir must exist for ssh to remember a key: %v", err)
	}
}

func TestSyncExecutorGalaxyFailureFailsTheJob(t *testing.T) {
	clone := t.TempDir()
	if err := os.WriteFile(clone+"/requirements.yml", []byte("- src: nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeFetcher{dir: clone}
	galaxy := t.TempDir() + "/ansible-galaxy"
	if err := os.WriteFile(galaxy, []byte("#!/bin/sh\necho 'cannot resolve' >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	status, _, err := (&SyncExecutor{Fetcher: f, GalaxyBin: galaxy}).Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "", ""), RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err == nil || status != StatusError {
		t.Fatalf("status = %q err = %v", status, err)
	}
	if !strings.Contains(err.Error(), "galaxy") {
		t.Errorf("error names the galaxy failure: %v", err)
	}
}

func TestSyncExecutorFetchFailureFailsTheJob(t *testing.T) {
	f := &fakeFetcher{err: errors.New("git clone: exit 128: [repo url]")}
	status, _, err := (&SyncExecutor{Fetcher: f}).Run(context.Background(),
		syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "", ""), RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err == nil || status != StatusError {
		t.Fatalf("status = %q err = %v", status, err)
	}
	if !strings.Contains(err.Error(), "fetch repository") {
		t.Errorf("error = %v", err)
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
