package requirements

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// The bounds of a take-over. A large install (dozens of collections) holds
// some tens of thousands of files and a few hundred MiB, so the bounds leave
// room while still ending a crafted tree. Lookups are the path parts the
// symlink check visits, across all links of the install.
const (
	maxTakeOverEntries = 500000
	maxTakeOverBytes   = 8 << 30
	maxTakeOverLookups = 1000000
)

// The reasons a take-over refuses an entry.
var (
	errTakeOverHardLink  = errors.New("a hard link")
	errTakeOverLinkOut   = errors.New("a symlink leaving the install")
	errTakeOverOddType   = errors.New("not a regular file, directory or symlink")
	errTakeOverChanged   = errors.New("changed while it was copied")
	errTakeOverLinkChain = fmt.Errorf("a chain of more than %d symlinks", maxLinkHops)
)

// takeOverTrees are the directories of a temp entry that a take-over copies.
var takeOverTrees = []string{"roles", "collections"}

type takeOverLimits struct {
	entries, depth int
	bytes          int64
	lookups        int
}

func defaultTakeOverLimits() takeOverLimits {
	return takeOverLimits{entries: maxTakeOverEntries, depth: maxTreeDepth, bytes: maxTakeOverBytes, lookups: maxTakeOverLookups}
}

// takeOverError names the refused path, relative to the temp entry.
type takeOverError struct {
	path string
	err  error
}

func (e *takeOverError) Error() string {
	return fmt.Sprintf("take over the install: %s: %v", displayPath(e.path), e.err)
}

func (e *takeOverError) Unwrap() error { return e.err }

// displayPath is p as an error shows it: quoted when it holds a byte that
// could pass for something else in a log line.
func displayPath(p string) string {
	odd := func(r rune) bool { return r == '"' || !unicode.IsPrint(r) }
	if !utf8.ValidString(p) || strings.ContainsFunc(p, odd) {
		return strconv.Quote(p)
	}
	return p
}

// takeOver copies roles/ and collections/ of the temp entry src (written by
// galaxy, as the play uid when one is set) into a fresh worker-owned temp
// entry and returns that one's name.
//
// The copy checks every entry: a directory becomes 0700, a regular file 0600
// (0700 when any execute bit was set), so setuid, setgid, sticky and group or
// world bits are dropped. A regular file must have one link and is opened
// without following links through src's root, and must be the file that was
// listed. A symlink is recreated with the same target only when the target
// is relative and stays inside the install, lexically and when followed the
// way the kernel follows it. Anything else (a device, a FIFO, a socket, a
// link leading out, a file the worker cannot read) refuses the take-over,
// naming the path. Nothing but regular files and directories is ever opened,
// so a FIFO cannot block it. On any error the partial copy is removed and no
// name is returned.
//
// The caller guarantees that no process of the install still runs, so the
// tree does not change while it is copied; what does is refused, not
// trusted.
func takeOver(cache *os.Root, src string) (string, error) {
	return takeOverWith(cache, src, defaultTakeOverLimits())
}

// takeOverWith is takeOver within the bounds lim.
func takeOverWith(cache *os.Root, src string, lim takeOverLimits) (string, error) {
	from, err := cache.OpenRoot(src)
	if err != nil {
		return "", fmt.Errorf("take over the install: %w", err)
	}
	defer func() { _ = from.Close() }()
	dst, err := mkTemp(cache)
	if err != nil {
		return "", fmt.Errorf("take over the install: %w", err)
	}
	to, err := cache.OpenRoot(dst)
	if err == nil {
		c := &copier{from: from, to: to, lim: lim}
		err = c.copyTrees()
		if cerr := to.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("take over the install: %w", cerr)
		}
	} else {
		err = fmt.Errorf("take over the install: %w", err)
	}
	if err != nil {
		if rerr := cache.RemoveAll(dst); rerr != nil {
			err = errors.Join(err, fmt.Errorf("remove %s: %w", dst, rerr))
		}
		return "", err
	}
	return dst, nil
}

// copier copies one install, spending its bounds.
type copier struct {
	from, to *os.Root
	lim      takeOverLimits
	entries  int
	bytes    int64
	lookups  int
}

// copyTrees copies each of takeOverTrees that exists, depth first, a
// directory's entries in name order.
func (c *copier) copyTrees() error {
	type item struct {
		path  string
		depth int
	}
	var stack []item
	for _, top := range slices.Backward(takeOverTrees) {
		if _, err := c.from.Lstat(top); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		stack = append(stack, item{top, 0})
	}
	c.entries = len(stack)
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		children, err := c.copyOne(it.path, it.depth)
		if err != nil {
			return &takeOverError{path: it.path, err: err}
		}
		for _, name := range slices.Backward(children) {
			stack = append(stack, item{path.Join(it.path, name), it.depth + 1})
		}
	}
	return nil
}

// copyOne copies the entry p and returns a directory's entry names.
func (c *copier) copyOne(p string, depth int) ([]string, error) {
	fi, err := c.from.Lstat(p)
	if err != nil {
		return nil, bare(err)
	}
	switch mode := fi.Mode(); {
	case mode.IsDir():
		if depth > c.lim.depth {
			return nil, fmt.Errorf("nested deeper than %d directories", c.lim.depth)
		}
		return c.copyDir(p, fi)
	case mode.IsRegular():
		return nil, c.copyFile(p, fi)
	case mode&fs.ModeSymlink != 0:
		return nil, c.copyLink(p)
	}
	return nil, errTakeOverOddType
}

// copyDir makes the directory p and lists the entries of the original.
func (c *copier) copyDir(p string, fi fs.FileInfo) ([]string, error) {
	f, err := c.from.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, bare(err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, bare(err)
	}
	if !os.SameFile(fi, st) {
		return nil, errTakeOverChanged
	}
	var names []string
	for {
		list, err := f.ReadDir(1024)
		if c.entries += len(list); c.entries > c.lim.entries {
			return nil, fmt.Errorf("more than %d files and directories", c.lim.entries)
		}
		for _, e := range list {
			names = append(names, e.Name())
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, bare(err)
		}
	}
	slices.Sort(names)
	if err := c.to.Mkdir(p, 0o700); err != nil {
		return nil, bare(err)
	}
	return names, nil
}

// copyFile copies the regular file p.
func (c *copier) copyFile(p string, fi fs.FileInfo) error {
	if links(fi) != 1 {
		return errTakeOverHardLink
	}
	f, err := c.from.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return bare(err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return bare(err)
	}
	if !os.SameFile(fi, st) || !st.Mode().IsRegular() || links(st) != 1 {
		return errTakeOverChanged
	}
	if st.Size() > c.lim.bytes-c.bytes {
		return fmt.Errorf("more than %d bytes of files", c.lim.bytes)
	}
	mode := os.FileMode(0o600)
	if fi.Mode()&0o111 != 0 {
		mode = 0o700
	}
	out, err := c.to.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return bare(err)
	}
	n, err := io.Copy(out, io.LimitReader(f, st.Size()+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return bare(err)
	}
	c.bytes += n
	after, err := f.Stat()
	if err != nil {
		return bare(err)
	}
	if n != st.Size() || after.Size() != st.Size() || !after.ModTime().Equal(st.ModTime()) {
		return errTakeOverChanged
	}
	return nil
}

// copyLink recreates the symlink p when its target stays inside the
// install.
func (c *copier) copyLink(p string) error {
	target, err := c.from.Readlink(p)
	if err != nil {
		return bare(err)
	}
	if target == "" || path.IsAbs(target) {
		return errTakeOverLinkOut
	}
	if j := path.Join(path.Dir(p), target); j == ".." || strings.HasPrefix(j, "../") {
		return errTakeOverLinkOut
	}
	if err := c.followLink(p, target); err != nil {
		return err
	}
	return bare(c.to.Symlink(target, p))
}

// followLink follows the link p to target as the kernel would in the copy,
// where only takeOverTrees exist at the top: part by part, a ".." going to
// the real parent of what was reached and a further link spliced in. Every
// directory p is in was walked as a real directory. Reaching a part that
// does not exist, or a part below a file, ends the walk without error: the
// kernel stops there as well, and the copy holds nothing the original does
// not. Going above the install, or an absolute link on the way, is
// errTakeOverLinkOut.
func (c *copier) followLink(p, target string) error {
	var done []string
	if d := path.Dir(p); d != "." {
		done = strings.Split(d, "/")
	}
	todo := strings.Split(target, "/")
	hops := 1
	for len(todo) > 0 {
		if c.lookups++; c.lookups > c.lim.lookups {
			return fmt.Errorf("more than %d lookups to check its symlinks", c.lim.lookups)
		}
		name := todo[0]
		todo = todo[1:]
		switch {
		case name == "" || name == ".":
			continue
		case name == "..":
			if len(done) == 0 {
				return errTakeOverLinkOut
			}
			done = done[:len(done)-1]
			continue
		case len(done) == 0 && !slices.Contains(takeOverTrees, name):
			return nil // not in the copy
		}
		cur := path.Join(path.Join(done...), name)
		fi, err := c.from.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return bare(err)
		case fi.Mode()&fs.ModeSymlink != 0:
			if hops++; hops > maxLinkHops {
				return errTakeOverLinkChain
			}
			next, err := c.from.Readlink(cur)
			if err != nil {
				return bare(err)
			}
			if next == "" || path.IsAbs(next) {
				return errTakeOverLinkOut
			}
			todo = append(strings.Split(next, "/"), todo...)
			continue
		case !fi.IsDir():
			return nil // nothing is opened below a file
		}
		done = append(done, name)
	}
	return nil
}

// links is the link count of a file.
func links(fi fs.FileInfo) uint64 {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(st.Nlink)
}

// bare drops the path a *fs.PathError repeats (the take-over error names
// it), keeping the operation and the reason.
func bare(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return err
}
