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
	type result struct{ ca, cert error }
	done := make(chan result, 1)
	go func() {
		_, caErr := files(fifo, "", "").Load()
		_, certErr := files("", fifo, fifo).Load()
		done <- result{caErr, certErr}
	}()
	select {
	case r := <-done:
		for _, c := range []struct {
			err  error
			want string
		}{
			{r.ca, "secret field `tls_ca` is not a regular file"},
			{r.cert, "secret field `tls_cert` is not a regular file"},
		} {
			if c.err == nil || c.err.Error() != c.want {
				t.Errorf("err = %v, want %q", c.err, c.want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loading a FIFO hung")
	}
}
