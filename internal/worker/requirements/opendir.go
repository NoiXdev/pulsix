package requirements

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// The named refusals of OpenDir. Each comes wrapped with the path:
// "open requirements cache <path>: <reason>".
var (
	// ErrCacheSymlink is reported for a symlink at the path, a dangling one too.
	ErrCacheSymlink = errors.New("a symlink, which is not followed")
	// ErrCacheNotDirectory is reported for a non-directory that is not a symlink.
	ErrCacheNotDirectory = errors.New("not a directory")
	// ErrCacheReplaced is reported when the directory at the path is not the
	// one that was opened.
	ErrCacheReplaced = errors.New("replaced while it was opened")
)

// OpenDir opens the requirements cache directory or one of its entries at path
// and verifies that it is a real directory: a symlink or a file there is
// refused with a named error and left alone, a path that does not exist passes
// fs.ErrNotExist through, and any other failure (permissions, I/O) is returned
// as it is and never mistaken for "missing". The returned root is the only
// handle later operations on the directory use. The caller closes it.
func OpenDir(path string) (*os.Root, error) {
	return openDirWith(path, nil)
}

// openDirWith is OpenDir with a hook that runs after the open and before the
// verification, so a test can swap the path in the window between the two.
func openDirWith(path string, afterOpen func()) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, wrapOpen(path, classifyOpenFailure(path, err))
	}
	if afterOpen != nil {
		afterOpen()
	}
	if err := verifyOpened(path, root); err != nil {
		_ = root.Close()
		return nil, wrapOpen(path, err)
	}
	return root, nil
}

// classifyOpenFailure names why os.OpenRoot failed. The Lstat of the path
// decides: a dangling symlink reads as a symlink and not as "does not exist".
func classifyOpenFailure(path string, openErr error) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return openErr
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return ErrCacheSymlink
	case !fi.IsDir():
		return ErrCacheNotDirectory
	default:
		return openErr
	}
}

// verifyOpened checks that path is still a real directory and that it is the
// directory root was opened on.
func verifyOpened(path string, root *os.Root) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return ErrCacheSymlink
	case !fi.IsDir():
		return ErrCacheNotDirectory
	}
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(fi, opened) {
		return ErrCacheReplaced
	}
	return nil
}

func wrapOpen(path string, reason error) error {
	return fmt.Errorf("open requirements cache %s: %w", path, reason)
}
