// The repository subpath a repo run reads its requirements under (spec goal
// 2, the plan's Ruling 1): the claim's repo.repo_subpath, or — from a
// manager that does not send it yet (spec goal 8) — derived from the
// claim's subpath, which is the repository subpath joined with the
// playbook path.
package runner

import (
	"fmt"
	"path"
	"strings"
)

// repoSubpath returns the repository's subpath, normalised ("" for none).
// claimed is the claim's repo_subpath (nil when the manager sent none);
// subpath and playbookPath are the claim's subpath and the playbook
// reference's path. Both sides are compared normalised, because the
// repository settings store forms like "ansible/", "/ansible" and ".", so a
// claim's subpath may read "ansible//site.yml". A subpath that does not end
// with the playbook path fails closed — it would mean the manager and the
// worker disagree on what the run executes — and so does a repository
// subpath that leads out of the clone.
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
	repoSub, raw := derived, derived
	if claimed != nil {
		repoSub, raw = cleanRepoPath(*claimed), *claimed
	}
	if repoSub == ".." || strings.HasPrefix(repoSub, "../") {
		return "", fmt.Errorf("the repository subpath %q leads out of the repository", raw)
	}
	return repoSub, nil
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
