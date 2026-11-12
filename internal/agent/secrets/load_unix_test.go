//go:build !windows

package secrets

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestOpenFileDoesNotBlockOnAFIFO: a FIFO swapped in for the file between
// Load's first look and its open must not hang the agent waiting for a
// writer; the open returns at once and Load's same-file check refuses it.
func TestOpenFileDoesNotBlockOnAFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// Whether the open succeeds does not matter here, only that it returns.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if f, err := openFile(path); err == nil {
			_ = f.Close()
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// Release the blocked open so the goroutine ends with the test.
		if w, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil { // #nosec G304 -- test file
			_ = w.Close()
		}
		t.Fatal("opening a FIFO blocked")
	}
}

// TestOpenFileRefusesASymlink: the open itself does not follow a link.
func TestOpenFileRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	writeFile(t, target, "secrets: {}\n")
	link := filepath.Join(dir, "agent.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f, err := openFile(link)
	if err == nil {
		_ = f.Close()
		t.Fatal("openFile followed a symlink")
	}
}
