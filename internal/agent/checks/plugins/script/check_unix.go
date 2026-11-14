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

// judgeFile judges resolved, a regular file, and its directory.
func judgeFile(resolved string, fi fs.FileInfo) error {
	file, err := unixStat(fi)
	if err != nil {
		return err
	}
	dirInfo, err := os.Stat(filepath.Dir(resolved))
	if err != nil {
		return fmt.Errorf("check the directory of %s: %w", resolved, err)
	}
	dir, err := unixStat(dirInfo)
	if err != nil {
		return err
	}
	return judgeUnix(file, dir)
}

func unixStat(fi fs.FileInfo) (unixFile, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return unixFile{}, errors.New("no owner in the file's status")
	}
	return unixFile{uid: st.Uid, mode: fi.Mode()}, nil
}

// judgeUnix is the Unix rule: the file owned by root, writable by nobody
// else (neither group nor others) and executable; its directory owned by
// root and writable by nobody else. The executable bit is no protection
// but it is asked of a script given to an interpreter as of the program,
// so one rule covers both. A refusal says why without the path.
func judgeUnix(file, dir unixFile) error {
	switch {
	case file.uid != 0:
		return refuse(fmt.Sprintf("owned by uid %d, not by root", file.uid))
	case file.mode.Perm()&0o022 != 0:
		return refuse("writable by group or others")
	case file.mode.Perm()&0o111 == 0:
		return refuse("not executable")
	case dir.uid != 0:
		return refuse(fmt.Sprintf("its directory is owned by uid %d, not by root", dir.uid))
	case dir.mode.Perm()&0o022 != 0:
		return refuse("its directory is writable by group or others")
	}
	return nil
}

// namesNoFile reports whether err says the path names nothing: it, or a
// directory on the way, does not exist, a component on the way is no
// directory, or the name is too long to be a file's.
func namesNoFile(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG)
}
