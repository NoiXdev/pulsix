package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const partialSuffix = ".partial"

// dirStorage keeps backups as files in one folder.
type dirStorage struct {
	dir string
}

// NewDirStorage stores backups in dir, creating it on first write. A file is
// written as <name>.partial and renamed once complete, so a crash never
// leaves a half file that looks like a backup.
func NewDirStorage(dir string) Storage {
	return &dirStorage{dir: dir}
}

func (d *dirStorage) Describe() string { return d.dir }

func (d *dirStorage) Put(ctx context.Context, name string, r io.Reader) (n int64, err error) {
	if err := validName(name); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(d.dir, 0o750); err != nil {
		return 0, fmt.Errorf("create backup folder: %w", err)
	}

	partial := filepath.Join(d.dir, name+partialSuffix)
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", name, err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(partial)
		}
	}()

	n, err = io.Copy(f, ctxReader{ctx: ctx, r: r})
	if err != nil {
		return 0, fmt.Errorf("write %s: %w", name, err)
	}
	if err = f.Sync(); err != nil {
		return 0, fmt.Errorf("sync %s: %w", name, err)
	}
	if err = f.Close(); err != nil {
		return 0, fmt.Errorf("close %s: %w", name, err)
	}
	if err = os.Rename(partial, filepath.Join(d.dir, name)); err != nil {
		return 0, fmt.Errorf("publish %s: %w", name, err)
	}
	// Make the rename durable; not every platform can sync a directory.
	if dir, derr := os.Open(d.dir); derr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return n, nil
}

func (d *dirStorage) Open(_ context.Context, name string) (io.ReadCloser, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	f, err := openRegular(filepath.Join(d.dir, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("open %s: %w", name, ErrObjectNotFound)
		}
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	return f, nil
}

// errNotRegular marks a storage entry that is a FIFO, device, socket or
// symbolic link rather than a file.
var errNotRegular = errors.New("not a regular file")

// openRegular opens path for reading only when it is a regular file. A symbolic
// link is refused (O_NOFOLLOW), and the open never waits for a writer to
// appear on a FIFO (O_NONBLOCK), so a stray special file cannot hang the
// caller; the type is checked on the open descriptor, not on the path, so it
// cannot be swapped in between.
func openRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, errNotRegular
	}
	return f, nil
}

// List describes every regular *.pxbak file in the folder. Entries of any
// other type are ignored, and an entry that cannot be read is listed as
// damaged; only a failure to read the folder itself fails the listing.
func (d *dirStorage) List(ctx context.Context) ([]Object, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list backup folder: %w", err)
	}
	var objs []Object
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// The entry type comes from the directory itself (lstat semantics),
		// so a symlink or FIFO is never opened here.
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), objectSuffix) {
			continue
		}
		o, ok := d.describe(e)
		if !ok {
			continue
		}
		objs = append(objs, o)
	}
	sortNewestFirst(objs)
	return objs, nil
}

// describe reads the header of one entry. ok is false for an entry that
// vanished or turned into something other than a file since ReadDir. Any
// other failure is reported on the returned object as its HeaderErr.
func (d *dirStorage) describe(e fs.DirEntry) (o Object, ok bool) {
	name := e.Name()
	o = Object{Name: name}
	if info, err := e.Info(); err == nil {
		o.Size = info.Size()
	}
	f, err := openRegular(filepath.Join(d.dir, name))
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, errNotRegular):
		return Object{}, false
	default:
		o.HeaderErr = fmt.Errorf("open %s: %w", name, err)
		return o, true
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil {
		o.Size = st.Size()
	}
	o.Header, o.HeaderErr = ReadHeader(f)
	return o, true
}

// CleanPartials removes the <name>.pxbak.partial files a stopped backup left
// in the folder. Only that exact suffix qualifies, so a *.partial file some
// other program keeps in the same folder survives. Only regular files directly
// in the folder qualify: the entry
// type comes from the directory itself (lstat semantics), so a symbolic link,
// a directory or a special file that merely carries the suffix is left alone
// and nothing below the folder is visited. A file that cannot be removed does
// not stop the others; the first such failure is returned with the count of
// the files that did go.
func (d *dirStorage) CleanPartials(ctx context.Context) (int, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("list backup folder: %w", err)
	}
	removed := 0
	var firstErr error
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), objectSuffix+partialSuffix) {
			continue
		}
		err := os.Remove(filepath.Join(d.dir, e.Name()))
		switch {
		case err == nil:
			removed++
		case errors.Is(err, fs.ErrNotExist):
		case firstErr == nil:
			firstErr = fmt.Errorf("remove %s: %w", e.Name(), err)
		}
	}
	return removed, firstErr
}

func (d *dirStorage) Delete(_ context.Context, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(d.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	return nil
}
