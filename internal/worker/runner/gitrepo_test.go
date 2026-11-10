// gitrepo fetches a run's playbook repository into the worker's persistent
// repo cache: clone into RepoCacheDir/<org>/<repo> when absent, fetch+reset
// to the branch tip when present. Credential material (git key or token)
// lives only in 0600 files under the run's work dir and is wiped when the
// fetch ends. Tests drive a local `git init` fixture and inspect the
// environment the runner passes to git through a wrapper binary.
package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/credentials"
)

// gitEnv returns a PATH that reaches the git binary tests use for fixtures.
func gitOnPATH(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("no git binary for the fixture: %v", err)
	}
	return path
}

// gitFixture prepares a local git repository whose branch main holds one
// playbook file, and returns its file:// URL.
func gitFixture(t *testing.T) string {
	t.Helper()
	git := gitOnPATH(t)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=pulsix", "GIT_AUTHOR_EMAIL=pulsix@example.org",
			"GIT_COMMITTER_NAME=pulsix", "GIT_COMMITTER_EMAIL=pulsix@example.org")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(dir, "playbooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "playbooks", "site.yml"),
		[]byte("- hosts: all\n  tasks:\n    - name: ping\n      ansible.builtin.ping:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "site")
	return dir
}

func gitCommit(t *testing.T, dir, content string) {
	t.Helper()
	git := gitOnPATH(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=pulsix", "GIT_AUTHOR_EMAIL=pulsix@example.org",
			"GIT_COMMITTER_NAME=pulsix", "GIT_COMMITTER_EMAIL=pulsix@example.org")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "playbooks", "site.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "update")
}

func TestFetchRepoClonesIntoOrgScopedCache(t *testing.T) {
	source := gitFixture(t)
	cacheRoot, workDir := t.TempDir(), t.TempDir()
	r := &Runner{GitBin: gitOnPATH(t)}

	spec := gitRepoSpec{
		OrgID: orgID, RepoID: repoID,
		URL: source, Branch: "main", Subpath: "playbooks/site.yml",
	}
	playbook, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, workDir)
	if err != nil {
		t.Fatalf("fetchRepo: %v", err)
	}
	want := filepath.Join(cacheRoot, orgID.String(), repoID.String(), "playbooks", "site.yml")
	if playbook != want {
		t.Errorf("playbook = %q, want the clone's subpath %q", playbook, want)
	}
	raw, err := os.ReadFile(playbook)
	if err != nil || !strings.Contains(string(raw), "ping") {
		t.Fatalf("playbook content = %q, %v", raw, err)
	}
	// The clone is owner-private: its source holds the organization's
	// playbooks.
	clone := filepath.Join(cacheRoot, orgID.String(), repoID.String())
	if fi, err := os.Stat(clone); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("clone mode = %v, must be owner-only", fi.Mode())
	}
}

func TestHeadCommit(t *testing.T) {
	source := gitFixture(t)
	r := &Runner{GitBin: gitOnPATH(t)}
	out, err := exec.Command(gitOnPATH(t), "-C", source, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.HeadCommit(context.Background(), source)
	if want := strings.TrimSpace(string(out)); err != nil || got != want {
		t.Fatalf("HeadCommit = %q, %v; want %q", got, err, want)
	}
	// A directory that is no repository has no commit, even below one.
	notRepo := filepath.Join(source, "playbooks")
	if got, err := r.HeadCommit(context.Background(), notRepo); err == nil {
		t.Fatalf("HeadCommit(not a repo) = %q, want an error", got)
	}
	if got, err := r.HeadCommit(context.Background(), t.TempDir()); err == nil {
		t.Fatalf("HeadCommit(empty dir) = %q, want an error", got)
	}
}

// orgID and repoID are stable fixtures for the org-scoped cache layout.
var (
	orgID  = uuid.Must(uuid.NewV7())
	repoID = uuid.Must(uuid.NewV7())
)

func TestFetchRepoUpdatesExistingClone(t *testing.T) {
	source := gitFixture(t)
	cacheRoot, workDir := t.TempDir(), t.TempDir()
	r := &Runner{GitBin: gitOnPATH(t)}
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: source, Branch: "main", Subpath: "playbooks/site.yml"}

	if _, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, workDir); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	gitCommit(t, source, "- hosts: all\n  tasks:\n    - name: updated\n")
	playbook, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, workDir)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	raw, err := os.ReadFile(playbook)
	if err != nil || !strings.Contains(string(raw), "updated") {
		t.Fatalf("playbook = %q, %v; the clone must follow the branch tip", raw, err)
	}
}

func TestFetchRepoCarriesGitKeyThroughGitSSHCommand(t *testing.T) {
	source := gitFixture(t)
	cacheRoot, workDir := t.TempDir(), t.TempDir()
	envDump := filepath.Join(t.TempDir(), "env")

	// The runner's git is a wrapper: record the environment, then run the
	// real git — so the test sees exactly what the fetch passed.
	wrapper := filepath.Join(workDir, "git-wrapper")
	script := "#!/bin/sh\n" +
		"env >> \"" + envDump + "\"\n" +
		"exec git \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &Runner{GitBin: wrapper}
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----\n"
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: source, Branch: "main", Subpath: "playbooks/site.yml"}

	playbook, err := r.fetchRepo(context.Background(), spec, secretJSON(t, credentials.Secret{PrivateKey: key}), cacheRoot, workDir)
	if err != nil {
		t.Fatalf("fetchRepo: %v", err)
	}
	if _, err := os.Stat(playbook); err != nil {
		t.Fatal(err)
	}
	envText, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatal(err)
	}
	// The key travels as a file via GIT_SSH_COMMAND -i, never inline.
	line := ""
	for _, l := range strings.Split(string(envText), "\n") {
		if strings.HasPrefix(l, "GIT_SSH_COMMAND=") {
			line = l
		}
	}
	if !strings.Contains(line, "-i ") {
		t.Errorf("GIT_SSH_COMMAND = %q, must point ssh at the key file", line)
	}
	if strings.Contains(line, key) {
		t.Error("the private key material must never be in GIT_SSH_COMMAND")
	}
	// The key file itself is wiped after the fetch.
	if files, _ := os.ReadDir(filepath.Join(workDir, "gitkeys")); len(files) != 0 {
		t.Errorf("key files survived: %v", files)
	}
}

func TestFetchRepoCarriesGitTokenViaAskpass(t *testing.T) {
	source := gitFixture(t)
	cacheRoot, workDir := t.TempDir(), t.TempDir()
	envDump := filepath.Join(t.TempDir(), "env")
	wrapper := filepath.Join(workDir, "git")
	script := "#!/bin/sh\nenv >> \"" + envDump + "\"\nexec git \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &Runner{GitBin: wrapper}
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: source, Branch: "main", Subpath: "playbooks/site.yml"}

	if _, err := r.fetchRepo(context.Background(), spec,
		secretJSON(t, credentials.Secret{Token: "ghp_test"}), cacheRoot, workDir); err != nil {
		t.Fatalf("fetchRepo: %v", err)
	}
	envText, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envText), "GIT_ASKPASS=") {
		t.Error("a token credential must fetch through GIT_ASKPASS")
	}
	if strings.Contains(string(envText), "ghp_test") {
		t.Error("the token must never appear in the git environment")
	}
}

func TestFetchRepoWipesKeyEvenOnFailure(t *testing.T) {
	r := &Runner{GitBin: gitOnPATH(t)}
	cacheRoot, workDir := t.TempDir(), t.TempDir()
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\nbad\n-----END OPENSSH PRIVATE KEY-----\n"
	// A repo that cannot exist: a URL with no host and no local path.
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: "ssh://nonexistent.invalid/x.git", Branch: "main", Subpath: "playbooks/site.yml"}
	if _, err := r.fetchRepo(context.Background(), spec, secretJSON(t, credentials.Secret{PrivateKey: key}), cacheRoot, workDir); err == nil {
		t.Fatal("an unclonable repo must fail")
	}
	if files, _ := os.ReadDir(filepath.Join(workDir, "gitkeys")); len(files) != 0 {
		t.Errorf("key files survived the failed fetch: %v", files)
	}
}

// TestRepoPlaybookIsReadableByThePlayUID: a `repo:<id>:<subpath>` playbook
// is executed where it was cloned — the worker's 0700 repo cache — and is
// never copied into the work dir, so sharing the work dir does nothing for
// it. With a play uid set, every directory from the cache root down to the
// playbook has to be group-traversable and the playbook group-readable, or
// the run dies on a file the worker can see and the play cannot. Nothing
// on that path may become group-WRITABLE: one job must not be able to
// rewrite the next job's checkout.
func TestRepoPlaybookIsReadableByThePlayUID(t *testing.T) {
	source := gitFixture(t)
	r := &Runner{GitBin: gitOnPATH(t)}

	job := runJob(t)
	repo := uuid.Must(uuid.NewV7())
	job.Playbook = "repo:" + repo.String() + ":playbooks/site.yml"
	job.KindPayload = `{"repo":{"id":"` + repo.String() + `","url":"` + source + `","branch":"main","subpath":"playbooks/site.yml"}}`

	env := runEnv(t)
	env.Config.PlayUID = 12345
	playbook, _, err := r.materializePlaybook(context.Background(), job, env)
	if err != nil {
		t.Fatalf("materializePlaybook: %v", err)
	}

	for p := playbook; ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		perm := info.Mode().Perm()
		want := os.FileMode(0o040) // group read
		if info.IsDir() {
			want |= 0o010 // and traverse
		}
		if perm&want != want {
			t.Errorf("%s: mode = %v, the play uid cannot reach the playbook through it", p, perm)
		}
		if perm&0o020 != 0 {
			t.Errorf("%s: mode = %v is group-writable; the repo cache is read-only to the play", p, perm)
		}
		if p == env.RepoCacheDir {
			break
		}
	}
}

// TestFetchGitUpdateUsesClaimURL (backlog 7.101): a worker that already holds
// a clone fetches from the URL the claim carries, not from the origin the
// clone stored when it was made. The first source is made unreadable, so any
// fetch that still contacts it fails.
func TestFetchGitUpdateUsesClaimURL(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a chmod 000 directory; the old source cannot be made unreadable")
	}
	sourceA := gitFixture(t)
	sourceB := gitFixture(t)
	gitCommit(t, sourceB, "- hosts: all\n  tasks:\n    - name: from-b\n")
	cacheRoot, workDir := t.TempDir(), t.TempDir()
	r := &Runner{GitBin: gitOnPATH(t)}
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: sourceA, Branch: "main", Subpath: "playbooks/site.yml"}

	if _, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, workDir); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if err := os.Chmod(sourceA, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sourceA, 0o755) })

	spec.URL = sourceB
	playbook, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, workDir)
	if err != nil {
		t.Fatalf("second fetch under the new URL: %v", err)
	}
	raw, err := os.ReadFile(playbook)
	if err != nil || !strings.Contains(string(raw), "from-b") {
		t.Fatalf("playbook = %q, %v; the clone must follow the claim's URL", raw, err)
	}
	// Later tooling reading the clone's config agrees with the claim.
	clone := filepath.Join(cacheRoot, orgID.String(), repoID.String())
	out, err := exec.Command(gitOnPATH(t), "-C", clone, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		t.Fatalf("read remote.origin.url: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != sourceB {
		t.Errorf("remote.origin.url = %q, want the claim's URL %q", got, sourceB)
	}
}

// TestFetchGitUpdateSameURLStaysIncremental: an unchanged URL keeps updating
// the existing clone in place, with no re-clone.
func TestFetchGitUpdateSameURLStaysIncremental(t *testing.T) {
	source := gitFixture(t)
	cacheRoot, workDir := t.TempDir(), t.TempDir()
	r := &Runner{GitBin: gitOnPATH(t)}
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: source, Branch: "main", Subpath: "playbooks/site.yml"}

	if _, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, workDir); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	gitDir := filepath.Join(cacheRoot, orgID.String(), repoID.String(), ".git")
	before, err := os.Stat(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	gitCommit(t, source, "- hosts: all\n  tasks:\n    - name: updated\n")
	playbook, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, workDir)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	after, err := os.Stat(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error(".git was replaced; an unchanged URL must update the clone in place")
	}
	raw, err := os.ReadFile(playbook)
	if err != nil || !strings.Contains(string(raw), "updated") {
		t.Fatalf("playbook = %q, %v; the clone must follow the branch tip", raw, err)
	}
}
