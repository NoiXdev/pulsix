//go:build !windows

package script

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

// unixFile is what the Unix rules read from a file or directory.
type unixFile struct {
	uid  uint32
	mode fs.FileMode
}

// osFS is the walk's file system: lstat, readlink, owner and mode bits.
type osFS struct{}

// judgeSyntax refuses nothing on Unix: every absolute path is one to walk.
func judgeSyntax(string) error { return nil }

// split splits an absolute path as the kernel walks it: nothing is
// cleaned, a ".." after a link climbs from where the link leads.
func (osFS) split(path string) (string, []string, error) {
	if !strings.HasPrefix(path, "/") {
		return "", nil, errors.New("not an absolute path")
	}
	return "/", unixNames(path), nil
}

func unixNames(p string) []string {
	var names []string
	for _, name := range strings.Split(p, "/") {
		if name != "" && name != "." {
			names = append(names, name)
		}
	}
	return names
}

func (osFS) join(root string, names []string) string { return root + strings.Join(names, "/") }

func (osFS) lookup(p string) (entryKind, error) {
	fi, err := os.Lstat(p)
	switch {
	case err != nil && namesNoFile(err):
		return 0, errNotFile
	case err != nil:
		return 0, err
	}
	switch m := fi.Mode(); {
	case m&fs.ModeSymlink != 0:
		return entryLink, nil
	case m.IsDir():
		kernel, err := onKernelFS(p)
		if err != nil {
			return 0, err
		}
		if kernel {
			return entryKernel, nil
		}
		return entryDir, nil
	case m.IsRegular():
		return entryFile, nil
	}
	return entryOther, nil
}

// follow reads the link at p. A target that is no path but a kernel
// object's name (Linux: socket:[4711] behind /proc/self/fd) names no
// file, and neither does an empty one.
func (osFS) follow(p, _ string) (string, []string, error) {
	target, err := os.Readlink(p)
	switch {
	case err != nil:
		return "", nil, err
	case target == "":
		return "", nil, errNotFile
	case strings.HasPrefix(target, "/"):
		return "/", unixNames(target), nil
	case strings.Contains(target, ":["):
		return "", nil, errNotFile
	}
	return "", unixNames(target), nil
}

// judgeDir applies one rule for every use: whoever may write a directory
// can rename an entry in it, or create the name that is missing.
func (osFS) judgeDir(dir string, _ dirUse, which string) error {
	d, err := unixStat(dir)
	if err != nil {
		return err
	}
	return judgeUnixDir(d, which)
}

// judgeLink has nothing to judge: a symlink's target cannot be changed in
// place, only by replacing it, which its directory decides.
func (osFS) judgeLink(string) error { return nil }

func (osFS) judgeFile(p string) error {
	f, err := unixStat(p)
	if err != nil {
		return err
	}
	return judgeUnixFile(f)
}

func unixStat(path string) (unixFile, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return unixFile{}, fmt.Errorf("check %s: %w", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return unixFile{}, fmt.Errorf("check %s: no owner in its status", path)
	}
	return unixFile{uid: st.Uid, mode: fi.Mode()}, nil
}

// judgeUnixFile is the Unix rule for the file: owned by root and writable
// by nobody else (neither group nor others). The executable bit is no
// protection and not asked for: a program without it does not start
// (permission denied), a script given to an interpreter needs none.
//
// Only owner and mode bits are read, here and for directories. An access
// control list that grants another user write access (macOS ACLs, Linux
// POSIX ACLs) is not seen: the standard library offers no portable way to
// read one.
//
// A refusal says why without a path.
func judgeUnixFile(file unixFile) error {
	switch {
	case file.uid != 0:
		return refuse(fmt.Sprintf("owned by uid %d, not by root", file.uid))
	case file.mode.Perm()&0o022 != 0:
		return refuse("writable by group or others")
	}
	return nil
}

// judgeUnixDir is the Unix rule for a directory on the way: owned by root
// and writable by nobody else, a sticky world-writable one like /tmp
// included: whoever can write it can rename an entry and put another in
// its place. which names the directory in a refusal.
func judgeUnixDir(dir unixFile, which string) error {
	switch {
	case dir.uid != 0:
		return refuse(fmt.Sprintf("%s is owned by uid %d, not by root", which, dir.uid))
	case dir.mode.Perm()&0o022 != 0:
		return refuse(which + " is writable by group or others")
	}
	return nil
}

// namesNoFile reports whether err says the path names nothing: it, or a
// directory on the way, does not exist, a component on the way is no
// directory, or the name is too long to be a file's.
func namesNoFile(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG)
}
