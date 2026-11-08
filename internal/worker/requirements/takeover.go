package requirements

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// The bounds of a take-over. A large install (dozens of collections) holds
// some tens of thousands of files and a few hundred MiB, so the bounds leave
// room while still ending a crafted tree. The copy is not sparse: a file
// counts and costs its full size. Lookups are the path parts the symlink
// check visits, across all links of the install. Names are the bytes of
// names and directory paths a walk holds at once (see walker).
const (
	maxTakeOverEntries = 500000
	maxTakeOverBytes   = 2 << 30
	maxTakeOverLookups = 1000000
	maxTakeOverNames   = 64 << 20
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
	names          int
}

func defaultTakeOverLimits() takeOverLimits {
	return takeOverLimits{
		entries: maxTakeOverEntries, depth: maxTreeDepth, bytes: maxTakeOverBytes,
		lookups: maxTakeOverLookups, names: maxTakeOverNames,
	}
}

// takeOverHooks are test seams: beforeOpen runs once an entry was listed
// and its Lstat succeeded, before anything opens or reads it; afterRead
// runs once a file's content was copied, before it is checked again;
// afterCopy runs after each entry was copied; and peak receives the most
// name bytes a walk held at once.
type takeOverHooks struct {
	beforeOpen func(p string)
	afterRead  func(p string)
	afterCopy  func(p string)
	peak       *int
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

// displayPath is p as an error shows it: quoted when it holds a byte or a
// separator that could pass for something else in a log line.
func displayPath(p string) string {
	odd := func(r rune) bool { return r == '"' || !unicode.IsPrint(r) }
	if !utf8.ValidString(p) || strings.ContainsFunc(p, odd) || strings.Contains(p, ": ") {
		return strconv.Quote(p)
	}
	return p
}

// takeOver copies roles/ and collections/ of the temp entry src (written by
// galaxy, as the play uid when one is set) into a fresh worker-owned temp
// entry and returns that one's name.
//
// The copy checks every entry as it copies it: a directory becomes 0700, a
// regular file 0600 (0700 when any execute bit was set), so setuid, setgid,
// sticky and group or world bits are dropped. A regular file must have one
// link and is opened without following links through src's root, and must
// be the file that was listed. A symlink is recreated with the same target
// when the target is relative and lexically inside the install. Anything
// else (a device, a FIFO, a socket, a file the worker cannot read) refuses
// the take-over, naming the path. Nothing but regular files and directories
// is ever opened, so a FIFO cannot block it. Once the copy is complete,
// every link in it is followed the way the kernel follows it and must stay
// inside; that check runs on the copy, which only the worker can reach.
// On any error the partial copy is removed and no name is returned.
//
// Safety does not rely on src staying unchanged: each entry is checked as
// it is copied, and the links, which depend on other entries, are checked
// where nothing changes them any more. The caller's guarantee that no
// process of the install still runs only makes the copy a consistent
// snapshot of what galaxy wrote.
//
// ctx ends the take-over between two entries (the step's timeout or a
// cancel): its bounds alone would still allow minutes of copying.
func takeOver(ctx context.Context, cache *os.Root, src string) (string, error) {
	return takeOverWith(ctx, cache, src, defaultTakeOverLimits(), takeOverHooks{})
}

// takeOverWith is takeOver within the bounds lim.
func takeOverWith(ctx context.Context, cache *os.Root, src string, lim takeOverLimits, hooks takeOverHooks) (string, error) {
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
		c := &copier{ctx: ctx, from: from, to: to, lim: lim, hooks: hooks}
		err = c.run()
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
	ctx      context.Context
	from, to *os.Root
	lim      takeOverLimits
	hooks    takeOverHooks
	bytes    int64
	lookups  int
}

// run copies the install, then checks the links of the copy.
func (c *copier) run() error {
	copying := &walker{ctx: c.ctx, lim: c.lim}
	err := copying.walk(takeOverTrees, c.copyOne)
	checking := &walker{ctx: c.ctx, lim: c.lim}
	if err == nil {
		err = checking.walk(takeOverTrees, c.checkOne)
	}
	if c.hooks.peak != nil {
		*c.hooks.peak = max(copying.peak, checking.peak)
	}
	return err
}

// copyOne copies the entry p; for a directory it returns the original
// opened, to be listed.
func (c *copier) copyOne(p string, depth int) (*os.File, error) {
	fi, err := c.from.Lstat(p)
	if depth == 0 && errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, bare(err)
	}
	if c.hooks.beforeOpen != nil {
		c.hooks.beforeOpen(p)
	}
	var dir *os.File
	switch mode := fi.Mode(); {
	case mode.IsDir():
		if depth > c.lim.depth {
			return nil, fmt.Errorf("nested deeper than %d directories", c.lim.depth)
		}
		dir, err = c.copyDir(p, fi)
	case mode.IsRegular():
		err = c.copyFile(p, fi)
	case mode&fs.ModeSymlink != 0:
		err = c.copyLink(p)
	default:
		err = errTakeOverOddType
	}
	if err != nil {
		return nil, err
	}
	if c.hooks.afterCopy != nil {
		c.hooks.afterCopy(p)
	}
	return dir, nil
}

// copyDir makes the directory p and returns the original opened.
func (c *copier) copyDir(p string, fi fs.FileInfo) (*os.File, error) {
	f, err := c.from.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, bare(err)
	}
	st, err := f.Stat()
	switch {
	case err != nil:
		err = bare(err)
	case !os.SameFile(fi, st):
		err = errTakeOverChanged
	default:
		err = bare(c.to.Mkdir(p, 0o700))
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
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
	if c.hooks.afterRead != nil {
		c.hooks.afterRead(p)
	}
	after, err := f.Stat()
	if err != nil {
		return bare(err)
	}
	if n != st.Size() || after.Size() != st.Size() || !after.ModTime().Equal(st.ModTime()) {
		return errTakeOverChanged
	}
	return nil
}

// copyLink recreates the symlink p when its target is relative and
// lexically inside the install. Where it really leads is checked in the
// copy (checkOne).
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
	return bare(c.to.Symlink(target, p))
}

// checkOne checks the entry p of the complete copy: a link must stay
// inside. For a directory it returns the directory opened, to be listed.
func (c *copier) checkOne(p string, depth int) (*os.File, error) {
	fi, err := c.to.Lstat(p)
	if depth == 0 && errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, bare(err)
	}
	switch {
	case fi.IsDir():
		f, err := c.to.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
		return f, bare(err)
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, c.followLink(c.to, p)
	}
	return nil, nil
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
