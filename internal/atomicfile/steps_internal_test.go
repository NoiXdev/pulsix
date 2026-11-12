package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// failingTemp is a real temporary file whose chosen step fails.
type failingTemp struct {
	*os.File
	fail string
}

var errStep = errors.New("injected failure")

func (f failingTemp) Chmod(mode os.FileMode) error {
	if f.fail == "chmod" {
		return errStep
	}
	return f.File.Chmod(mode)
}

func (f failingTemp) Write(p []byte) (int, error) {
	if f.fail == "write" {
		return 0, errStep
	}
	return f.File.Write(p)
}

func (f failingTemp) Sync() error {
	if f.fail == "sync" {
		return errStep
	}
	return f.File.Sync()
}

func (f failingTemp) Close() error {
	if f.fail == "close" {
		_ = f.File.Close()
		return errStep
	}
	return f.File.Close()
}

// TestWriteStopsAtTheFirstFailingStep: whichever step of the temporary
// file fails — permissions, content, flush or close — the error comes
// back, the target is never created, and the temporary file is gone.
func TestWriteStopsAtTheFirstFailingStep(t *testing.T) {
	for _, step := range []string{"chmod", "write", "sync", "close"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			err := write(dir, "f", []byte("x"), 0o600, func(dir, pattern string) (tempFile, error) {
				f, err := os.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return failingTemp{File: f, fail: step}, nil
			})
			if !errors.Is(err, errStep) {
				t.Fatalf("write with a failing %s = %v, want the failure", step, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "f")); !os.IsNotExist(err) {
				t.Fatalf("the target exists after a failed %s: %v", step, err)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("a failed %s left %v behind", step, entries)
			}
		})
	}
}

// fakeDir is a directory handle whose flush fails or succeeds on demand,
// and which records that it was closed.
type fakeDir struct {
	syncErr error
	closed  bool
}

func (d *fakeDir) Sync() error  { return d.syncErr }
func (d *fakeDir) Close() error { d.closed = true; return nil }

// TestSyncDirIsANoOpOnWindows: a directory handle on Windows is read-only,
// and flushing one fails with access denied — after the rename has already
// put the file in place. Windows must not even open the directory.
func TestSyncDirIsANoOpOnWindows(t *testing.T) {
	err := syncDirOn("windows", t.TempDir(), func(string) (dirHandle, error) {
		t.Fatal("the directory was opened on Windows")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("syncDirOn(windows) = %v, want nil", err)
	}
}

// TestSyncDirFlushesElsewhere: on Unix the directory is flushed, a failed
// flush is reported, and the handle is closed either way; a directory that
// cannot be opened is not an error.
func TestSyncDirFlushesElsewhere(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		t.Run(goos, func(t *testing.T) {
			ok := &fakeDir{}
			if err := syncDirOn(goos, "d", func(string) (dirHandle, error) { return ok, nil }); err != nil {
				t.Fatalf("a good flush = %v, want nil", err)
			}
			if !ok.closed {
				t.Error("the directory handle was not closed")
			}
			bad := &fakeDir{syncErr: errStep}
			if err := syncDirOn(goos, "d", func(string) (dirHandle, error) { return bad, nil }); !errors.Is(err, errStep) {
				t.Fatalf("a failed flush = %v, want the failure", err)
			}
			if !bad.closed {
				t.Error("the directory handle was not closed after a failed flush")
			}
			if err := syncDirOn(goos, "d", func(string) (dirHandle, error) { return nil, os.ErrPermission }); err != nil {
				t.Fatalf("an unopenable directory = %v, want nil", err)
			}
		})
	}
}
