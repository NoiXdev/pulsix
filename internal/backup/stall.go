package backup

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// stallTimeout is how long a backup may go without a byte reaching its
// destination before it is cancelled (backlog 7.48.1).
const stallTimeout = 10 * time.Minute

// stalledError is the error of a backup cancelled for making no progress.
type stalledError struct{ after time.Duration }

func (e stalledError) Error() string {
	return fmt.Sprintf("stalled: no progress for %v", e.after)
}

// watchdog calls stop once no progress was reported for its timeout.
type watchdog struct {
	timeout time.Duration
	timer   *time.Timer
	fired   atomic.Bool
}

// startWatchdog arms a watchdog that calls stop after timeout without
// progress. stop runs on its own goroutine and must be safe to call more
// than once.
func startWatchdog(timeout time.Duration, stop func()) *watchdog {
	w := &watchdog{timeout: timeout}
	w.timer = time.AfterFunc(timeout, func() {
		w.fired.Store(true)
		stop()
	})
	return w
}

// progress re-arms the watchdog. Once it has fired it stays fired.
func (w *watchdog) progress() {
	if !w.fired.Load() {
		w.timer.Reset(w.timeout)
	}
}

// halt disarms the watchdog; a stop already running may still finish.
func (w *watchdog) halt() { w.timer.Stop() }

// progressReader reports every read that returns bytes as progress: the
// destination took them.
type progressReader struct {
	r  io.Reader
	wd *watchdog
}

func (p progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.wd.progress()
	}
	return n, err
}
