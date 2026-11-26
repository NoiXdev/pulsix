//go:build !windows

package dbcheck

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO in place of a file is refused at once: opening it for reading
// must not wait for a writer.
func TestTLSFilesFIFODoesNotHang(t *testing.T) {
	dir := tlsDir(t)
	fifo := filepath.Join(dir, "ca.pem")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		mustLoadErr(t, files(fifo, "", ""), "secret field `tls_ca` is not a regular file")
		mustLoadErr(t, files("", fifo, fifo), "secret field `tls_cert` is not a regular file")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loading a FIFO hung")
	}
}
