// Package logfile is a small size-rotated log file writer for the agent. It
// keeps the current file plus one rotated generation (path + ".1").
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DefaultMaxBytes is the size at which the agent log rotates.
const DefaultMaxBytes = 10 << 20

// Writer appends to a log file and rotates it once it would exceed maxBytes.
// It is safe for concurrent use.
type Writer struct {
	path     string
	maxBytes int64
	openFile func(path string) (*os.File, error)

	mu   sync.Mutex
	f    *os.File // nil while closed or after a failed reopen
	size int64
	done bool // set by Close; later writes fail instead of reopening
}

// Open creates the parent directory (0o700), opens path in append mode and
// counts its existing size.
func Open(path string, maxBytes int64) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &Writer{path: path, maxBytes: maxBytes, openFile: openAppend}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func (w *Writer) open() error {
	f, err := w.openFile(w.path)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.f, w.size = f, info.Size()
	return nil
}

// Write appends p, rotating first when the file would grow past the limit. A
// write larger than the limit still lands whole in a fresh file.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return 0, os.ErrClosed
	}
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate closes the file before renaming it, because Windows cannot rename an
// open file. On failure w.f stays nil so the next Write reopens.
func (w *Writer) rotate() error {
	err := w.f.Close()
	w.f = nil
	if err != nil {
		return fmt.Errorf("close log file: %w", err)
	}
	rotated := w.path + ".1"
	if err := os.Remove(rotated); err != nil && !os.IsNotExist(err) {
		_ = w.open() // keep logging to the old file; the error below is reported
		return fmt.Errorf("remove rotated log: %w", err)
	}
	if err := os.Rename(w.path, rotated); err != nil {
		_ = w.open()
		return fmt.Errorf("rotate log: %w", err)
	}
	return w.open()
}

// Close closes the file. Writes after Close fail with os.ErrClosed.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
