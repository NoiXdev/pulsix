package repos

import (
	"fmt"
	"strings"
)

// SubpathRule says which subpaths CanonicalSubpath accepts, for an
// ErrInvalidInput message.
const SubpathRule = "may be empty or contain letters, digits, dots, underscores, slashes and hyphens (up to 200 characters), no \"..\""

// CanonicalSubpath returns the one stored form of a repository subpath
// (backlog 7.94): a clone-relative slash path with no empty or "."
// segments, so no leading, trailing or doubled "/" and no "./"; "" for the
// repository root ("/", "." and "" alike). The worker refuses a playbook
// path with a leading "/" and cannot open "./a" or "a//b" as a read root,
// so a run and a sync both need this form. The refusals are the ones a
// subpath always had, judged on what was sent: characters outside
// letters, digits, ".", "_", "/" and "-", any "..", more than 200
// characters — ErrInvalidInput.
func CanonicalSubpath(raw string) (string, error) {
	if strings.Contains(raw, "..") || !subpathPattern.MatchString(raw) {
		return "", fmt.Errorf("%w: invalid subpath: %s", ErrInvalidInput, SubpathRule)
	}
	var kept []string
	for _, seg := range strings.Split(raw, "/") {
		if seg != "" && seg != "." {
			kept = append(kept, seg)
		}
	}
	return strings.Join(kept, "/"), nil
}
