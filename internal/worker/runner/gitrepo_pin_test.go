// A pinned run (spec 2026-11-25 A): the claim names the commit the
// approver saw, and the worker checks out exactly that commit after its
// fetch, not the branch tip; a commit the repository no longer has fails
// the run.
package runner

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func playbookText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestFetchRepoChecksOutThePinnedCommit: with two commits on the branch,
// a run pinned to the older one runs the older one, from a fresh clone and
// from a cached clone at the tip; the next unpinned run is back on the tip.
func TestFetchRepoChecksOutThePinnedCommit(t *testing.T) {
	source := gitFixture(t)
	older := headOf(t, source)
	gitCommit(t, source, "- hosts: all\n  tasks: []\n# newer\n")
	newer := headOf(t, source)
	r := &Runner{GitBin: gitOnPATH(t)}
	spec := func(commit string) gitRepoSpec {
		return gitRepoSpec{OrgID: orgID, RepoID: uuid.Must(uuid.NewV7()), URL: source, Branch: "main", Subpath: "playbooks/site.yml", Commit: commit}
	}

	t.Run("fresh clone", func(t *testing.T) {
		s := spec(older)
		path, err := r.fetchRepo(context.Background(), s, "", t.TempDir(), t.TempDir())
		if err != nil {
			t.Fatalf("fetchRepo: %v", err)
		}
		if text := playbookText(t, path); !strings.Contains(text, "ping") || strings.Contains(text, "newer") {
			t.Fatalf("playbook = %q, want the older commit's", text)
		}
		if got, err := r.HeadCommit(context.Background(), strings.TrimSuffix(path, "/playbooks/site.yml")); err != nil || got != older {
			t.Fatalf("HeadCommit = %q, %v; want %s", got, err, older)
		}
	})
	t.Run("cached clone", func(t *testing.T) {
		cacheRoot := t.TempDir()
		s := spec("")
		if path, err := r.fetchRepo(context.Background(), s, "", cacheRoot, t.TempDir()); err != nil || !strings.Contains(playbookText(t, path), "newer") {
			t.Fatalf("unpinned fetch: %v", err)
		}
		s.Commit = older
		path, err := r.fetchRepo(context.Background(), s, "", cacheRoot, t.TempDir())
		if err != nil {
			t.Fatalf("pinned fetch: %v", err)
		}
		clone := strings.TrimSuffix(path, "/playbooks/site.yml")
		if got := headOf(t, clone); got != older || strings.Contains(playbookText(t, path), "newer") {
			t.Fatalf("pinned clone at %s, want %s", got, older)
		}
		s.Commit = ""
		if _, err := r.fetchRepo(context.Background(), s, "", cacheRoot, t.TempDir()); err != nil {
			t.Fatalf("unpinned fetch again: %v", err)
		}
		if got := headOf(t, clone); got != newer {
			t.Fatalf("unpinned clone at %s, want the tip %s", got, newer)
		}
	})
}

// TestFetchRepoFailsForACommitTheRepositoryLacks: the run fails with a
// clear message rather than running the tip.
func TestFetchRepoFailsForACommitTheRepositoryLacks(t *testing.T) {
	source := gitFixture(t)
	r := &Runner{GitBin: gitOnPATH(t)}
	gone := "0123456789abcdef0123456789abcdef01234567"
	s := gitRepoSpec{OrgID: orgID, RepoID: uuid.Must(uuid.NewV7()), URL: source, Branch: "main", Subpath: "playbooks/site.yml", Commit: gone}
	_, err := r.fetchRepo(context.Background(), s, "", t.TempDir(), t.TempDir())
	if err == nil || err.Error() != "commit "+gone+" is no longer in the repository" {
		t.Fatalf("fetchRepo = %v, want %q", err, "commit "+gone+" is no longer in the repository")
	}

	// Not a commit id at all: refused before git sees it.
	s.Commit = "--upload-pack=touch /tmp/x"
	if _, err := r.fetchRepo(context.Background(), s, "", t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a commit id") {
		t.Fatalf("fetchRepo(bad pin) = %v, want a refusal", err)
	}
}

// TestRepoSpecReadsThePinnedCommit: the claim's repo.commit reaches the
// fetch; a claim without one fetches the tip.
func TestRepoSpecReadsThePinnedCommit(t *testing.T) {
	repo := uuid.Must(uuid.NewV7())
	pin := "0123456789abcdef0123456789abcdef01234567"
	for name, tc := range map[string]struct{ payload, want string }{
		"pinned":   {`{"repo":{"id":"` + repo.String() + `","url":"file:///x","branch":"main","subpath":"site.yml","commit":"` + pin + `"}}`, pin},
		"unpinned": {`{"repo":{"id":"` + repo.String() + `","url":"file:///x","branch":"main","subpath":"site.yml"}}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			job := runJob(t)
			job.KindPayload = tc.payload
			spec, _, err := repoSpec(job, repo, "site.yml")
			if err != nil || spec.Commit != tc.want {
				t.Fatalf("Commit = %q, %v; want %q", spec.Commit, err, tc.want)
			}
		})
	}
}
