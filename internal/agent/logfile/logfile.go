// Package logfile is a small size-rotated log file writer for the agent. It
// keeps the current file plus one rotated generation (path + ".1").
package logfile

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultMaxBytes is the size at which the agent log rotates.
const DefaultMaxBytes = 10 << 20

// rotationRetryInterval is how long the writer goes on appending to the
// current file after a failed rotation before it tries to rotate again. A
// rotation fails while something holds the log open without sharing
// deletion — on Windows, a log tailer such as Get-Content -Wait — and
// retrying on every line would only repeat the failure for as long as the
// tailer runs.
const rotationRetryInterval = time.Minute

// Writer appends to a log file and rotates it once it would exceed maxBytes.
// It is safe for concurrent use.
type Writer struct {
	path     string
	maxBytes int64
	openFile func(path string) (*os.File, error)
	now      func() time.Time

	mu   sync.Mutex
	f    *os.File // nil while closed or after a failed reopen
	size int64
	done bool // set by Close; later writes fail instead of reopening
	// retryRotationAt is when a rotation may be tried again after one
	// failed; zero while none has.
	retryRotationAt time.Time
}

// Open creates the parent directory (0o700), opens path in append mode and
// counts its existing size.
func Open(path string, maxBytes int64) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &Writer{path: path, maxBytes: maxBytes, openFile: openAppend, now: time.Now}
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
//
// A failed rotation does not lose p: it goes to the current file, past the
// limit, after a warning that says why, and the rotation is tried again
// once rotationRetryInterval has passed. Write fails only when no file can
// be opened at all.
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
	var rotateErr error
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes && !w.now().Before(w.retryRotationAt) {
		rotateErr = w.rotate()
		if rotateErr != nil {
			w.retryRotationAt = w.now().Add(rotationRetryInterval)
		}
		if w.f == nil { // rotate could not reopen; one more try
			if err := w.open(); err != nil {
				return 0, err
			}
		}
	}
	if rotateErr != nil {
		w.warnRotationFailed(rotateErr)
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate closes the file before renaming it, because Windows cannot rename an
// open file. When the rename fails it reopens the old file; w.f is nil only
// when no file could be reopened.
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

// warnRotationFailed writes a warning line in the agent's own log format
// into the current file, so an operator sees why it grows past its limit.
func (w *Writer) warnRotationFailed(err error) {
	r := slog.NewRecord(w.now(), slog.LevelWarn, "log rotation failed; writing on to the current file", 0)
	r.AddAttrs(slog.String("err", err.Error()), slog.Duration("retry_in", rotationRetryInterval))
	var line bytes.Buffer
	_ = slog.NewTextHandler(&line, nil).Handle(context.Background(), r)
	n, _ := w.f.Write(line.Bytes())
	w.size += int64(n)
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
