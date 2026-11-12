package smart

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// runResult is one finished smartctl run: its standard output (at most
// maxOutput bytes) and its exit status, a bit mask the plugin interprets
// (report.go).
type runResult struct {
	stdout   []byte
	exitCode int
}

// runner runs smartctl with an argument list. It must return once ctx is
// done, whatever the process does; a non-zero exit status is a result, not
// an error. Errors are reserved for a run that produced no usable result:
// the binary did not start, the deadline stopped it, its output was too
// large, or an earlier run on the same arguments has not exited yet.
type runner interface {
	Run(ctx context.Context, args []string) (runResult, error)
}

var (
	errOutputTooLarge = errors.New("smartctl output exceeds 1 MiB")
	errStillRunning   = errors.New("an earlier smartctl run on this device has not exited yet")
)

const (
	// maxOutput caps the standard output read per run; smartctl -a --json
	// for one disk is a few tens of KiB.
	maxOutput = 1 << 20
	// waitDelay bounds how long Wait waits for the output pipe after the
	// process ended or was killed (a descendant holding it open).
	waitDelay = 2 * time.Second
	// defaultReapGrace is how long Run waits, after its context ended and
	// the process was killed, for the process to actually go away.
	defaultReapGrace = 2 * time.Second
)

// execRunner executes the smartctl binary at path directly — never through
// a shell, every argument one argv element. Standard error is discarded:
// with --json smartctl puts its diagnostics into the JSON messages.
type execRunner struct {
	path string
	// busy is shared by every run of one plugin instance: a smartctl the
	// kill could not stop (in the kernel, waiting on a hung disk) keeps its
	// arguments marked until it exits, so each tick does not pile another
	// stuck process onto the same device.
	busy *inflight
	// reapGrace overrides defaultReapGrace; zero means the default.
	reapGrace time.Duration
	// noKill makes the context's end not kill the process (tests: a
	// process the kill does not stop).
	noKill bool
}

func (r execRunner) Run(ctx context.Context, args []string) (runResult, error) {
	key := strings.Join(args, "\x00")
	if !r.busy.acquire(key) {
		return runResult{}, errStillRunning
	}
	out := &cappedWriter{max: maxOutput}
	cmd := exec.CommandContext(ctx, r.path, args...)
	cmd.Stdout = out
	cmd.WaitDelay = waitDelay
	if r.noKill {
		cmd.Cancel = func() error { return nil }
		cmd.WaitDelay = 0
	}
	if err := cmd.Start(); err != nil {
		r.busy.release(key)
		return runResult{}, fmt.Errorf("start smartctl: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		r.busy.release(key)
		done <- err
	}()

	var werr error
	select {
	case werr = <-done:
	case <-ctx.Done():
		grace := r.reapGrace
		if grace <= 0 {
			grace = defaultReapGrace
		}
		select {
		case werr = <-done:
		case <-time.After(grace):
			return runResult{}, fmt.Errorf("smartctl did not exit when stopped: %w", ctx.Err())
		}
	}
	if err := ctx.Err(); err != nil {
		return runResult{}, err
	}
	if out.overflow {
		return runResult{}, errOutputTooLarge
	}
	code := 0
	if werr != nil {
		var ee *exec.ExitError
		if !errors.As(werr, &ee) || ee.ExitCode() < 0 {
			return runResult{}, fmt.Errorf("run smartctl: %w", werr)
		}
		code = ee.ExitCode()
	}
	return runResult{stdout: out.buf, exitCode: code}, nil
}

// cappedWriter keeps the first max bytes written and notes whether more
// came. It never fails a write, so smartctl is not cut off mid-output by a
// broken pipe; the run's deadline bounds a process that writes forever.
type cappedWriter struct {
	max      int
	buf      []byte
	overflow bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.buf); len(p) > room {
		w.buf = append(w.buf, p[:max(room, 0)]...)
		w.overflow = true
	} else {
		w.buf = append(w.buf, p...)
	}
	return len(p), nil
}

// inflight is the set of argument lists with a smartctl still running.
type inflight struct {
	mu      sync.Mutex
	running map[string]bool
}

func newInflight() *inflight { return &inflight{running: map[string]bool{}} }

func (g *inflight) acquire(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[key] {
		return false
	}
	g.running[key] = true
	return true
}

func (g *inflight) release(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.running, key)
}
