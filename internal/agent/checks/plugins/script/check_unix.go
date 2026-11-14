//go:build !windows

package script

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// unixFile is what the Unix rules read from a file or directory.
type unixFile struct {
	uid  uint32
	mode fs.FileMode
}

// resolveFile resolves every symlink in path and returns the regular file
// it names; a path that names none is errNotFile.
func resolveFile(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		var fi fs.FileInfo
		if fi, err = os.Lstat(resolved); err == nil && !fi.Mode().IsRegular() {
			return "", errNotFile
		}
	}
	switch {
	case err == nil:
		return resolved, nil
	case namesNoFile(err):
		return "", errNotFile
	}
	return "", err
}

// resolveDir resolves every symlink in path and returns the directory it
// names; a path that names none is errNotFile.
func resolveDir(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		var fi fs.FileInfo
		if fi, err = os.Lstat(resolved); err == nil && !fi.IsDir() {
			return "", errNotFile
		}
	}
	switch {
	case err == nil:
		return resolved, nil
	case namesNoFile(err):
		return "", errNotFile
	}
	return "", err
}

// judgeSyntax refuses nothing on Unix: every absolute path is one to stat.
func judgeSyntax(string) error { return nil }

// judgePlace judges dir, a directory without links on its way, and every
// directory above it by the rule for a file's directories (judgeDirs);
// own says whether dir is the path's own directory.
func judgePlace(dir string, own bool) error {
	dirs, err := dirChain(dir)
	if err != nil {
		return err
	}
	first := "a directory above it"
	if own {
		first = "its directory"
	}
	return judgeDirs(dirs, first)
}

// judgeFile judges resolved, a regular file without links on its way, and
// every directory above it up to the root.
func judgeFile(resolved string) error {
	file, dirs, err := statChain(resolved)
	if err != nil {
		return err
	}
	return judgeUnix(file, dirs)
}

// statChain reads the file at resolved and each directory above it, its
// own first, the root last.
func statChain(resolved string) (unixFile, []unixFile, error) {
	file, err := unixStat(resolved)
	if err != nil {
		return unixFile{}, nil, err
	}
	dirs, err := dirChain(filepath.Dir(resolved))
	if err != nil {
		return unixFile{}, nil, err
	}
	return file, dirs, nil
}

// dirChain reads dir and each directory above it, the root last.
func dirChain(dir string) ([]unixFile, error) {
	var dirs []unixFile
	for ; ; dir = filepath.Dir(dir) {
		d, err := unixStat(dir)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, d)
		if filepath.Dir(dir) == dir {
			return dirs, nil
		}
	}
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

// judgeUnix is the Unix rule: the file owned by root and writable by
// nobody else (neither group nor others); its directory (dirs[0]) and
// every directory above it (the rest, up to the root) owned by root and
// writable by nobody else, a sticky world-writable one like /tmp
// included: whoever can write a directory on the way can rename an entry
// and put another file in the checked one's place. The executable bit is
// no protection and not asked for: a program without it does not start
// (permission denied), a script given to an interpreter needs none.
//
// Only owner and mode bits are read. An access control list that grants
// another user write access (macOS ACLs, Linux POSIX ACLs) is not seen:
// the standard library offers no portable way to read one.
//
// A refusal says why without a path.
func judgeUnix(file unixFile, dirs []unixFile) error {
	switch {
	case file.uid != 0:
		return refuse(fmt.Sprintf("owned by uid %d, not by root", file.uid))
	case file.mode.Perm()&0o022 != 0:
		return refuse("writable by group or others")
	}
	return judgeDirs(dirs, "its directory")
}

// judgeDirs is judgeUnix's rule for directories, dirs[0] named first in a
// refusal, the rest "a directory above it".
func judgeDirs(dirs []unixFile, first string) error {
	for i, dir := range dirs {
		which := "a directory above it"
		if i == 0 {
			which = first
		}
		switch {
		case dir.uid != 0:
			return refuse(fmt.Sprintf("%s is owned by uid %d, not by root", which, dir.uid))
		case dir.mode.Perm()&0o022 != 0:
			return refuse(which + " is writable by group or others")
		}
	}
	return nil
}

// namesNoFile reports whether err says the path names nothing: it, or a
// directory on the way, does not exist, a component on the way is no
// directory, or the name is too long to be a file's.
func namesNoFile(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG)
}
