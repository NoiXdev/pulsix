package worker

import (
	"path"
	"strings"
)

// CleanRepoPath is a repository path as a clone-relative slash path: no
// leading "/", path.Clean semantics, "." as "". The settings store forms
// like "ansible/", "/ansible" and ".", and the runner and the sync both
// normalise them with this. A path that leads out of the clone stays so
// ("../x", ".."); callers refuse it.
func CleanRepoPath(p string) string {
	p = path.Clean(strings.TrimLeft(p, "/"))
	if p == "." {
		return ""
	}
	return p
}
