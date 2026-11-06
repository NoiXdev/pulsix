package requirements

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// lockFile is the flock target inside a repo's cache directory.
const lockFile = ".lock"

// lockPoll is how often a waiting lock tries again (Ruling 11).
const lockPoll = 20 * time.Millisecond

// errLockNotFile refuses a .lock that is not a regular file (a symlink, a
// directory): it is not something the worker put there.
var errLockNotFile = errors.New(".lock is not a regular file")

// lock takes the exclusive lock of the cache directory that root is opened
// on: flock(LOCK_EX|LOCK_NB) on its .lock file (created 0600; a symlink there
// is refused, not followed), retried every lockPoll until it is held or ctx
// ends. A wait is therefore cancellable and bounded by ctx. unlock releases
// the lock and closes the file; it is called once.
func lock(ctx context.Context, root *os.Root) (unlock func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("lock requirements cache: %w", err)
	}
	f, err := openLockFile(root)
	if err != nil {
		return nil, fmt.Errorf("lock requirements cache: %w", err)
	}
	ticker := time.NewTicker(lockPoll)
	defer ticker.Stop()
	for {
		held, err := tryFlock(f, syscall.LOCK_EX|syscall.LOCK_NB)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock requirements cache: %w", err)
		}
		if held {
			return func() {
				_, _ = tryFlock(f, syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("lock requirements cache: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// openLockFile opens .lock, creating it 0600 when it is missing. The create
// is O_EXCL and the open that follows has no O_CREATE: through os.Root on
// darwin, concurrent O_CREATE opens of one new file fail with ENOENT now and
// then, which the exclusive create does not. An os.Root resolves a symlink
// inside the root, so the opened file is checked against an Lstat of the name:
// it must be a regular file and the very file opened. A mode other than 0600
// is set back to 0600 on the opened file (the worker owns the directory).
func openLockFile(root *os.Root) (*os.File, error) {
	f, err := root.OpenFile(lockFile, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		f, err = root.OpenFile(lockFile, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, err
	}
	named, err := root.Lstat(lockFile)
	if err == nil {
		var opened fs.FileInfo
		opened, err = f.Stat()
		if err == nil && (!named.Mode().IsRegular() || !os.SameFile(named, opened)) {
			err = errLockNotFile
		}
		if err == nil && named.Mode().Perm() != 0o600 {
			err = f.Chmod(0o600)
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// tryFlock applies how to f. It reports false, and no error, when a
// non-blocking request found the lock held elsewhere.
func tryFlock(f *os.File, how int) (bool, error) {
	conn, err := f.SyscallConn()
	if err != nil {
		return false, err
	}
	var flockErr error
	if err := conn.Control(func(fd uintptr) {
		for {
			flockErr = syscall.Flock(int(fd), how)
			if !errors.Is(flockErr, syscall.EINTR) {
				return
			}
		}
	}); err != nil {
		return false, err
	}
	if errors.Is(flockErr, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return flockErr == nil, flockErr
}
