// The file system the read of the variable sources sees (backlog 7.75,
// Review Focus 5): the clone's subpath through an os.Root, with every
// symlink resolved here, component by component, so that
//   - a link that leaves the read root (an absolute target, or ".." above
//     the root) is refused;
//   - no path, written or reached through a link, enters git's own .git
//     directory (any letter case: a case-insensitive file system opens
//     .GIT too);
//   - a relative link that stays inside the root is followed (at most
//     maxLinkHops links per path);
//   - only regular files and directories are opened: a FIFO or a device
//     would block the open or never end the read.
//
// The roles installed from the requirements files (the roles/ of the
// commit's entry in the requirements cache next to the clone) are a second
// read root of their own, under the same rules.
//
// The os.Root underneath refuses an escape again on its own.
package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// maxLinkHops bounds the links one path resolves through (a loop ends
// there), as the kernel's own limit does.
const maxLinkHops = 40

var (
	// errOutsideRead: the path leads outside the read root or into git's
	// own files.
	errOutsideRead = errors.New("outside the repository's files")
	// errSpecialFile: neither a regular file nor a directory.
	errSpecialFile = errors.New("not a regular file")
)

// cloneFS is an fs.FS (with Stat) over the read root.
type cloneFS struct {
	root *os.Root
}

// openCloneFS opens the clone at dir with subpath (resolved like any
// other path, so it must not be or lead into .git) as the read root.
func openCloneFS(dir, subpath string) (*cloneFS, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open clone: %w", err)
	}
	c := &cloneFS{root: root}
	if subpath == "" {
		return c, nil
	}
	defer func() { _ = root.Close() }()
	sub := strings.TrimRight(filepath.ToSlash(subpath), "/")
	resolved, err := c.resolve(sub)
	if err != nil {
		return nil, fmt.Errorf("open subpath: %w", err)
	}
	inner, err := root.OpenRoot(resolved)
	if err != nil {
		return nil, fmt.Errorf("open subpath: %w", err)
	}
	return &cloneFS{root: inner}, nil
}

// ReadRoot is a confined read root that must be closed.
type ReadRoot interface {
	fs.StatFS
	Close() error
}

// OpenCloneFS opens the whole clone at cloneDir as a read root under the
// rules above. The root also reads links itself (fs.ReadLinkFS, confined
// the same way), which the requirements check needs to tell a "../" that
// stays inside the clone from one that leaves it.
func OpenCloneFS(cloneDir string) (ReadRoot, error) {
	c, err := openCloneFS(cloneDir, "")
	if err != nil {
		return nil, err
	}
	return c, nil
}

// installedFS is the installed requirements entry's read roots: its roles/
// and its collections/ansible_collections (backlog 7.96), each with the
// clone's rules (a link inside it may not leave it). A root that could not
// be opened is nil, with the reason in its error; an entry that installed
// no collection has neither.
type installedFS struct {
	roles, collections       *cloneFS
	rolesErr, collectionsErr error
}

// Close closes the roots that were opened.
func (in installedFS) Close() {
	for _, c := range []*cloneFS{in.roles, in.collections} {
		if c != nil {
			_ = c.Close()
		}
	}
}

// openInstalledFS opens the installed requirements entry's read roots;
// nothing for the zero Entry: the commit installs nothing. The entry is
// opened through requirements.OpenDir, the one verified open of the cache
// (spec goal 1a): an entry that is a symlink, no directory or gone is the
// error, naming the path and the reason, as it is for a run.
func openInstalledFS(installed requirements.Entry) (installedFS, error) {
	var in installedFS
	if installed.IsZero() {
		return in, nil
	}
	entry, err := requirements.OpenDir(installed.Dir)
	if err != nil {
		return in, fmt.Errorf("open installed requirements: %w", err)
	}
	defer func() { _ = entry.Close() }()
	if roles, err := entry.OpenRoot("roles"); err != nil {
		in.rolesErr = fmt.Errorf("open installed roles: %w", err)
	} else {
		in.roles = &cloneFS{root: roles}
	}
	colls, err := entry.OpenRoot(path.Join("collections", "ansible_collections"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		in.collectionsErr = fmt.Errorf("open installed collections: %w", err)
	default:
		in.collections = &cloneFS{root: colls}
	}
	return in, nil
}

func (c *cloneFS) Close() error { return c.root.Close() }

// Open opens a regular file or a directory of the read root.
func (c *cloneFS) Open(name string) (fs.File, error) {
	p, err := c.resolve(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	info, err := c.root.Stat(p)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return nil, &fs.PathError{Op: "open", Path: name, Err: errSpecialFile}
	}
	return c.root.Open(p)
}

// Stat describes a path of the read root without opening it.
func (c *cloneFS) Stat(name string) (fs.FileInfo, error) {
	p, err := c.resolve(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return c.root.Stat(p)
}

// Lstat describes a path of the read root without following a link at its
// last component; the components above it are resolved like any path.
func (c *cloneFS) Lstat(name string) (fs.FileInfo, error) {
	p, err := c.resolveParent(name)
	if err != nil {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: err}
	}
	return c.root.Lstat(p)
}

// ReadLink returns the target of the link at name as it is written (a
// slash path); the components above name are resolved like any path. The
// caller decides what the target means: it is not resolved here.
func (c *cloneFS) ReadLink(name string) (string, error) {
	p, err := c.resolveParent(name)
	if err != nil {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: err}
	}
	target, err := c.root.Readlink(p)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(target), nil
}

// resolveParent is name with every link above its last component resolved
// (resolve), the last component kept as it is: errOutsideRead when that
// component is git's directory.
func (c *cloneFS) resolveParent(name string) (string, error) {
	if !fs.ValidPath(name) {
		return "", fs.ErrInvalid
	}
	if name == "." {
		return ".", nil
	}
	dir, base := path.Split(name)
	if strings.EqualFold(base, ".git") {
		return "", errOutsideRead
	}
	parent, err := c.resolve(orDot(strings.TrimSuffix(dir, "/")))
	if err != nil {
		return "", err
	}
	return path.Join(parent, base), nil
}

// orDot is p, or "." for the read root.
func orDot(p string) string {
	if p == "" {
		return "."
	}
	return p
}

// resolve is name with every symlink on it resolved, as a path of the read
// root that holds no link: errOutsideRead when it leaves the root or
// enters .git, the lookup's error when a component does not exist.
func (c *cloneFS) resolve(name string) (string, error) {
	if !fs.ValidPath(name) {
		return "", fs.ErrInvalid
	}
	var done []string
	todo := pathParts(name)
	hops := 0
	for len(todo) > 0 {
		part := todo[0]
		todo = todo[1:]
		if part == ".." {
			if len(done) == 0 {
				return "", errOutsideRead
			}
			done = done[:len(done)-1]
			continue
		}
		if strings.EqualFold(part, ".git") {
			return "", errOutsideRead
		}
		cur := path.Join(path.Join(done...), part)
		info, err := c.root.Lstat(cur)
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			done = append(done, part)
			continue
		}
		if hops++; hops > maxLinkHops {
			return "", errOutsideRead
		}
		target, err := c.root.Readlink(cur)
		if err != nil {
			return "", err
		}
		target = filepath.ToSlash(target)
		if path.IsAbs(target) || filepath.IsAbs(target) {
			return "", errOutsideRead
		}
		todo = append(pathParts(target), todo...)
	}
	if len(done) == 0 {
		return ".", nil
	}
	return path.Join(done...), nil
}

// pathParts splits a slash path into its components, without empty and
// "." ones.
func pathParts(p string) []string {
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			parts = append(parts, s)
		}
	}
	return parts
}
