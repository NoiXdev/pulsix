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
