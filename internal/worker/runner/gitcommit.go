// A pinned run's commit (spec 2026-11-25 A) and the commit a clone is at:
// the checkout of exactly the commit the approver saw, and the commit id a
// sync reports.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/noixdev/pulsix/internal/worker"
)

// checkoutCommit puts the clone at target on a pinned run's commit (spec
// 2026-11-25 A): the commit the approver saw, which the last sync fetched
// from the branch, so the branch fetch normally brought it. One no longer
// on the branch (a force-push) is asked for by id; a server that no
// longer has it, or refuses to hand it out, fails the run rather than
// running anything else.
func (r *Runner) checkoutCommit(ctx context.Context, gitBin, target string, spec worker.RepoSpec, env []string) error {
	has := func() bool {
		return runGit(ctx, gitBin, target, env, spec.URL, "cat-file", "-e", spec.Commit+"^{commit}") == nil
	}
	if !has() {
		if ctx.Err() != nil {
			return fmt.Errorf("git cat-file stopped: %w", ctx.Err())
		}
		fetchErr := runGit(ctx, gitBin, target, env, spec.URL, "fetch", "--quiet", "--", spec.URL, spec.Commit)
		if ctx.Err() != nil {
			return fetchErr
		}
		if fetchErr != nil || !has() {
			return fmt.Errorf("commit %s is no longer in the repository", spec.Commit)
		}
	}
	return runGit(ctx, gitBin, target, env, spec.URL, "reset", "--hard", spec.Commit)
}

// HeadCommit is the commit the clone at dir is at (git rev-parse HEAD in
// the clone's own .git: a directory that is no clone has none, even below
// another repository). Its failure carries the same redacted diagnostic as
// the fetch's. A cancel ends it with an error wrapping the context's own,
// which installRequirements and the worker loop map to cancelled.
func (r *Runner) HeadCommit(ctx context.Context, dir string) (string, error) {
	gitBin := r.GitBin
	if gitBin == "" {
		gitBin = "git"
	}
	env := append(stripPrivateEnv(os.Environ()), "GIT_DIR="+filepath.Join(dir, ".git"))
	var out bytes.Buffer
	if err := runGitOut(ctx, gitBin, dir, env, "", &out, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return "", err
	}
	commit := strings.TrimSpace(out.String())
	if !commitPattern.MatchString(commit) {
		return "", fmt.Errorf("git rev-parse: not a commit id: %q", truncate(commit, 100))
	}
	return commit, nil
}

// commitPattern is a full commit id, SHA-1 or SHA-256.
var commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
