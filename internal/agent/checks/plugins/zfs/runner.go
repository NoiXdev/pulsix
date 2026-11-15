package zfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// runner runs one of the ZFS programs (zpool, zfs) with an argument list
// and returns its standard output and standard error. It must return once
// ctx is done, whatever the process does. A non-zero exit status is an
// exitStatus error, returned with both outputs; other errors mean the run
// produced nothing usable: the program is not installed (errNotFound),
// did not start, was stopped by ctx, wrote too much, or an earlier run
// with the same arguments that was given up on has not exited yet.
type runner interface {
	Run(ctx context.Context, name string, args []string) (stdout, stderr []byte, err error)
}

// exitStatus is a run that ended with a non-zero exit status.
type exitStatus int

func (s exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(s)) }

var (
	errOutputTooLarge = errors.New("output exceeds 1 MiB")
	errStillRunning   = errors.New("an earlier run that was stopped has not exited yet")
)

const (
	// maxOutput caps the standard output read per run: zpool status for a
	// few hundred disks is tens of KiB.
	maxOutput = 1 << 20
	// maxStderr caps the standard error kept per run; only its first line
	// is ever quoted.
	maxStderr = 4 << 10
	// waitDelay bounds how long Wait waits for the output pipes after the
	// process ended or was killed (a descendant holding them open).
	waitDelay = 2 * time.Second
	// defaultReapGrace is how long Run waits, after its context ended and
	// the process was killed, for the process to actually go away.
	defaultReapGrace = 2 * time.Second
)

// execRunner executes the program found by find directly — never through
// a shell, every argument one argv element — with the agent's environment
// plus LC_ALL=C, so dates and numbers come in one format.
type execRunner struct {
	// find returns the program's absolute path, or an error wrapping
	// errNotFound. It is asked on every run, so installing ZFS takes
	// effect without restarting the agent.
	find func(name string) (string, error)
	// busy is shared by every run of one plugin: a zpool the kill could
	// not stop (in the kernel, waiting on a suspended pool) marks its
	// arguments when Run gives up on it, until it exits, so each round
	// does not pile another stuck process onto the same pool. Runs that
	// are merely concurrent (two checks) never mark it.
	busy *inflight
	// reapGrace overrides defaultReapGrace; zero means the default.
	reapGrace time.Duration
	// noKill makes the context's end not kill the process (tests: a
	// process the kill does not stop).
	noKill bool
}

func (r execRunner) Run(ctx context.Context, name string, args []string) (stdout, stderr []byte, err error) {
	path, err := r.find(name)
	if err != nil {
		return nil, nil, err
	}
	key := name + "\x00" + strings.Join(args, "\x00")
	if r.busy.stuck(key) {
		return nil, nil, errStillRunning
	}
	out := &cappedWriter{max: maxOutput}
	errOut := &cappedWriter{max: maxStderr}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C") // the last value of a key wins
	cmd.Stdout = out
	cmd.Stderr = errOut
	cmd.WaitDelay = waitDelay
	if r.noKill {
		cmd.Cancel = func() error { return nil }
		cmd.WaitDelay = 0
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s: %w", name, err)
	}
	st := &runState{}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		r.busy.exited(key, st)
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
			r.busy.giveUp(key, st)
			return nil, nil, fmt.Errorf("%s did not exit when stopped: %w", name, ctx.Err())
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if out.overflow {
		return nil, nil, errOutputTooLarge
	}
	if werr != nil {
		var ee *exec.ExitError
		if !errors.As(werr, &ee) || ee.ExitCode() < 0 {
			return nil, nil, fmt.Errorf("run %s: %w", name, werr)
		}
		return out.buf, errOut.buf, exitStatus(ee.ExitCode())
	}
	return out.buf, errOut.buf, nil
}

// cappedWriter keeps the first max bytes written and notes whether more
// came. It never fails a write, so the program is not cut off mid-output by
// a broken pipe; the run's deadline bounds a process that writes forever.
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

// inflight counts, per program and argument list, the processes Run gave
// up on that have not exited yet.
type inflight struct {
	mu      sync.Mutex
	running map[string]int
}

// runState is one run's standing in its inflight set, guarded by its mutex.
type runState struct {
	exited, givenUp bool
}

func newInflight() *inflight { return &inflight{running: map[string]int{}} }

// stuck reports whether a process Run gave up on still runs with key.
func (g *inflight) stuck(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running[key] > 0
}

// giveUp marks key while the run's process has not exited.
func (g *inflight) giveUp(key string, st *runState) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !st.exited && !st.givenUp {
		st.givenUp = true
		g.running[key]++
	}
}

// exited records that the run's process is gone and unmarks key if the
// run had been given up on.
func (g *inflight) exited(key string, st *runState) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st.exited = true
	if st.givenUp {
		if g.running[key]--; g.running[key] <= 0 {
			delete(g.running, key)
		}
	}
}
