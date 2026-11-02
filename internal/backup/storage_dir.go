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
	f, err := os.Open(filepath.Join(d.dir, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("open %s: %w", name, ErrObjectNotFound)
		}
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	return f, nil
}

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
		if e.IsDir() || !strings.HasSuffix(e.Name(), objectSuffix) {
			continue
		}
		o, err := d.describe(e.Name())
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted since ReadDir
		}
		if err != nil {
			return nil, err
		}
		objs = append(objs, o)
	}
	sortNewestFirst(objs)
	return objs, nil
}

func (d *dirStorage) describe(name string) (Object, error) {
	f, err := os.Open(filepath.Join(d.dir, name))
	if err != nil {
		return Object{}, fmt.Errorf("open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Object{}, fmt.Errorf("stat %s: %w", name, err)
	}
	return headerObject(name, st.Size(), f), nil
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
