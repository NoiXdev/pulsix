//go:build unix

package worker

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCloneFSNeverOpensSpecialFiles(t *testing.T) {
	clone := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(clone, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	symlinks(t, clone, map[string]string{"p.yml": "pipe"})
	c, err := openCloneFS(clone, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	done := make(chan error, 1)
	go func() {
		f, err := c.Open("p.yml")
		if err == nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the FIFO was opened")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the open blocked on a FIFO")
	}
}
