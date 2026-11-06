// A repo run's requirements (spec 2026-11-06-requirements-cache, goals 2,
// 5 and 6): the repository subpath they are looked for under (the plan's
// Ruling 1) — the claim's repo.repo_subpath, or, from a manager that does
// not send it yet (spec goal 8), derived from the claim's subpath, which is
// the repository subpath joined with the playbook path — and the step that
// installs them for the run's commit before the play.
package runner

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// repoSubpath returns the repository's subpath, normalised ("" for none).
// claimed is the claim's repo_subpath (nil when the manager sent none);
// subpath and playbookPath are the claim's subpath and the playbook
// reference's path. Both sides are compared normalised, because the
// repository settings store forms like "ansible/", "/ansible" and ".", so a
// claim's subpath may read "ansible//site.yml". A subpath that does not end
// with the playbook path fails closed — it would mean the manager and the
// worker disagree on what the run executes — and so do a claimed
// repository subpath that is not the one the subpath derives (the manager
// always sends subpath = repo subpath + "/" + playbook path) and a
// repository subpath that leads out of the clone.
func repoSubpath(claimed *string, subpath, playbookPath string) (string, error) {
	sub, playbook := cleanRepoPath(subpath), cleanRepoPath(playbookPath)
	var derived string
	switch {
	case playbook == "":
		return "", fmt.Errorf("the claim's subpath %q does not end with the playbook path %q", subpath, playbookPath)
	case sub == playbook:
		derived = ""
	case strings.HasSuffix(sub, "/"+playbook):
		derived = strings.TrimSuffix(sub, "/"+playbook)
	default:
		return "", fmt.Errorf("the claim's subpath %q does not end with the playbook path %q", subpath, playbookPath)
	}
	if claimed != nil && cleanRepoPath(*claimed) != derived {
		return "", fmt.Errorf("the claim's repo subpath %q does not match its subpath %q", *claimed, subpath)
	}
	raw := derived
	if claimed != nil {
		raw = *claimed
	}
	if derived == ".." || strings.HasPrefix(derived, "../") {
		return "", fmt.Errorf("the repository subpath %q leads out of the repository", raw)
	}
	return derived, nil
}

// cleanRepoPath normalises a clone-relative slash path: no leading "/",
// path.Clean semantics, "." as "".
func cleanRepoPath(p string) string {
	p = path.Clean(strings.TrimLeft(p, "/"))
	if p == "." {
		return ""
	}
	return p
}

// installRequirements is Run's requirements step, right after the playbook
// is materialized and before any other run file is written (the plan's
// Ruling 6): the step's lines stream through red as stdout events, ahead
// of run_start. A step that fails ends the run with
// "install requirements: <reason>", sent as the last line
// "Requirements: <that text>" first; a cancelled one sends no failure
// line. A line that cannot be sent stops the install and ends the run with
// "send requirements log: <reason>": the run can no longer be observed.
func (r *Runner) installRequirements(ctx context.Context, env worker.RunEnv, c *repoCheckout, red *redactor, sink func(*workerpb.RunEvent) error) (requirements.Entry, error) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	var sendErr error
	send := func(text string) {
		if sendErr != nil {
			return
		}
		if err := sink(runEvent(evStateStdout, "", "", red.textPayload(text))); err != nil {
			sendErr = fmt.Errorf("send requirements log: %w", err)
			stop()
		}
	}
	entry, err := r.ensureRequirements(ctx, env, c, send)
	if sendErr != nil {
		return requirements.Entry{}, sendErr
	}
	if err == nil {
		return entry, nil
	}
	err = fmt.Errorf("install requirements: %w", err)
	if !errors.Is(err, context.Canceled) {
		send("Requirements: " + err.Error())
		if sendErr != nil {
			return requirements.Entry{}, sendErr
		}
	}
	return requirements.Entry{}, err
}

// ensureRequirements installs the requirements of the checkout's commit,
// or reuses this worker's install of them, and returns the entry the run's
// ansible.cfg names (spec goal 2). No checkout (not a repo playbook) or no
// counted requirements file: the zero Entry, and the cache is not touched.
// Files are found through the clone's confined root, so a counted path
// that leads out of the clone fails the step. With a play uid the entry —
// and the path down to it — is opened read-only to the play's group on
// every run, like the clone (the plan's Ruling 5).
func (r *Runner) ensureRequirements(ctx context.Context, env worker.RunEnv, c *repoCheckout, line func(string)) (requirements.Entry, error) {
	if c == nil {
		return requirements.Entry{}, nil
	}
	clone, err := worker.OpenCloneFS(c.Clone)
	if err != nil {
		return requirements.Entry{}, err
	}
	defer func() { _ = clone.Close() }()
	files, err := requirements.Find(clone, c.RepoSubpath)
	if err != nil {
		return requirements.Entry{}, fmt.Errorf("find requirements: %w", err)
	}
	if files.Empty() {
		return requirements.Entry{}, nil
	}
	if r.Requirements == nil {
		return requirements.Entry{}, errors.New("no requirements installer wired")
	}
	commit, err := r.HeadCommit(ctx, c.Clone)
	if err != nil {
		return requirements.Entry{}, err
	}
	entry, err := r.Requirements.Ensure(ctx, requirements.Request{
		CloneDir:     c.Clone,
		Clone:        clone,
		Files:        files,
		Commit:       commit,
		RepoCacheDir: env.RepoCacheDir,
		Line:         line,
	})
	if err != nil || entry.IsZero() {
		return entry, err
	}
	if env.Config.PlayUID != 0 {
		if err := ShareRepoRead(env.RepoCacheDir, entry.Dir); err != nil {
			return requirements.Entry{}, fmt.Errorf("share the installed requirements with the play uid: %w", err)
		}
	}
	return entry, nil
}
