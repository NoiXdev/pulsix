package requirements

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// The bounds of a file's source check. A vendored collection holds a few
// thousand files (community.general about 4 000), so 50 000 entries per
// directory source leave room. Lookups (every path part, link hop and
// directory entry the check visits, across all sources of the file) are
// bounded so that a crafted tree of links cannot make the check slow: 50 000
// entries at a typical depth below 20 parts stay under 1 000 000, and a
// million lookups take about a second or two on a local disk.
const (
	maxTreeEntries = 50000
	maxTreeDepth   = 64
	maxLookups     = 1000000
)

var (
	errTooManyEntries = errors.New("has more than 50000 files and directories to check")
	errTooDeep        = errors.New("is nested deeper than 64 directories")
	errTooManyLookups = errors.New("needs more than 1000000 lookups to check")
)

type treeLimits struct{ entries, depth, lookups int }

func defaultLimits() treeLimits {
	return treeLimits{entries: maxTreeEntries, depth: maxTreeDepth, lookups: maxLookups}
}

// linkOutsideError: a link inside a directory source leads outside the
// clone (or into .git). link is clone-relative.
type linkOutsideError struct{ link string }

func (e *linkOutsideError) Error() string {
	return fmt.Sprintf("the link %q leads outside the repository", e.link)
}

func (e *linkOutsideError) Unwrap() error { return errOutside }

// walkTree checks every link in the directory root (clone-relative,
// without links) and below it: galaxy reads a directory source's files and
// follows links in it. A collection of type dir copies a file link's
// target (galaxy/collection/__init__.py:1335-1351, verified: a link to a
// file outside the clone ended up in the install) and a subdirs collection
// installs every child directory, a link to one outside the clone too
// (dependency_resolution/dataclasses.py:137-149, verified). Each link must
// resolve inside the clone (resolve); the directory a link leads to is
// walked as well, once per link target (seen), so a loop ends. A .git
// directory is not walked: galaxy skips it (collection/__init__.py:1203)
// and the clone's own .git is refused by resolve. The walk is bounded by
// lim, its entries spent from the budget.
func (c *sourceCheck) walkTree(root string) error {
	type dir struct {
		path  string
		depth int
	}
	stack := []dir{{root, 0}}
	seen := map[string]bool{root: true}
	entries := 0
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if d.depth > c.lim.depth {
			return errTooDeep
		}
		if err := c.spend(); err != nil {
			return err
		}
		list, err := fs.ReadDir(c.clone, orDot(d.path))
		if err != nil {
			return fmt.Errorf("read %s: %w", orDot(d.path), err)
		}
		for _, e := range list {
			if entries++; entries > c.lim.entries {
				return errTooManyEntries
			}
			if err := c.spend(); err != nil {
				return err
			}
			child := path.Join(d.path, e.Name())
			switch {
			case strings.EqualFold(e.Name(), ".git"):
			case e.Type()&fs.ModeSymlink != 0:
				r, err := c.resolve(nil, parts(child, false))
				switch {
				case errors.Is(err, errOutside):
					return &linkOutsideError{link: child}
				case err != nil:
					return err
				}
				if r.dir && !seen[r.path] {
					seen[r.path] = true
					stack = append(stack, dir{r.path, d.depth + 1})
				}
			case e.IsDir():
				stack = append(stack, dir{child, d.depth + 1})
			}
		}
	}
	return nil
}

func orDot(p string) string {
	if p == "" {
		return "."
	}
	return p
}

// optionLike reports a source git could read as an option: galaxy runs
// git clone --depth=1 <source> <dir> for a collection of type git without
// "--" (galaxy/collection/concrete_artifact_manager.py:440-443; verified:
// a name --upload-pack=… reached git's argv). The source is read as galaxy
// splits it, generously: stripped, after an "scm+" prefix (requirement.py
// :84-85, concrete_artifact_manager.py:403-406), and each comma part (the
// ",version" of parse_scm, :399-400, goes to git checkout).
func optionLike(src string) bool {
	candidates := []string{src}
	if _, after, ok := strings.Cut(src, "+"); ok {
		candidates = append(candidates, after)
	}
	for _, c := range candidates {
		for _, part := range strings.Split(c, ",") {
			if strings.HasPrefix(pyStrip(part), "-") {
				return true
			}
		}
	}
	return false
}

// sourceError is checkSource's refusal of src as the step reports it,
// with a link's path relative to the requirements root.
func sourceError(name, root, src string, err error) error {
	var link *linkOutsideError
	switch {
	case errors.As(err, &link):
		return &refusal{
			msg: fmt.Sprintf("%s: the source %q holds the link %q, which leads outside the repository", name, src, relTo(root, link.link)),
			err: errOutside,
		}
	case errors.Is(err, errOutside), errors.Is(err, errTooManyEntries), errors.Is(err, errTooDeep), errors.Is(err, errTooManyLookups):
		return fmt.Errorf("%s: the source %q %w", name, src, err)
	}
	return fmt.Errorf("%s: the source %q: %w", name, src, err)
}

// requirementsRoot is the requirements root (clone-relative) of a file
// named name relative to it, in the clone-relative directory fileDir.
func requirementsRoot(fileDir, name string) string {
	d := path.Dir(name)
	if d == "." {
		return fileDir
	}
	return strings.TrimSuffix(strings.TrimSuffix(fileDir, d), "/")
}

// relTo is the clone-relative path p relative to root.
func relTo(root, p string) string {
	if root == "" {
		return p
	}
	r, ps := strings.Split(root, "/"), strings.Split(p, "/")
	for len(r) > 0 && len(ps) > 0 && r[0] == ps[0] {
		r, ps = r[1:], ps[1:]
	}
	rel := path.Join(append(slices.Repeat([]string{".."}, len(r)), ps...)...)
	if rel == "" {
		return "."
	}
	return rel
}

// refusal is a message of its own that still matches its sentinel.
type refusal struct {
	msg string
	err error
}

func (r *refusal) Error() string { return r.msg }
func (r *refusal) Unwrap() error { return r.err }
