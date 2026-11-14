package script

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	// waitDelay bounds how long Wait waits for the output pipe after the
	// process ended or was killed (a descendant holding it open).
	waitDelay = 2 * time.Second
	// defaultReapGrace is how long Run waits, after its context ended and
	// the process tree was killed, for the process to actually go away:
	// longer than waitDelay, after which exec closes the output pipe a
	// descendant still holds, so a killed process is not given up on
	// merely because its pipe was slow to close.
	defaultReapGrace = waitDelay + time.Second
)

// execRunner runs the command's argv directly, never through a shell,
// with a built environment (env.go), the root directory as working
// directory, standard input and standard error on the null device and
// standard output read up to maxOutput. The process and everything it
// starts form one tree (a process group on Unix, a job object on Windows,
// exec_unix.go and exec_windows.go): a timeout kills the whole tree, and
// whatever the program leaves behind when it exits is killed with it.
type execRunner struct {
	// busy is shared by every run of one plugin (New makes one runner for
	// every check key): a process the kill could not stop (in the kernel,
	// waiting on a hung mount) marks its argv when Run gives up on it,
	// until it exits, so each round does not pile another stuck process
	// onto the same command. Runs that are merely concurrent (two checks
	// listing the same command) never mark it.
	busy *inflight
	// reapGrace overrides defaultReapGrace; zero means the default.
	reapGrace time.Duration
	// noKill makes the context's end not kill the process (tests: a
	// process the kill does not stop).
	noKill bool
}

func newExecRunner() execRunner { return execRunner{busy: newInflight()} }

func (r execRunner) Run(ctx context.Context, argv []string) (runResult, error) {
	if len(argv) == 0 || argv[0] == "" {
		return runResult{}, errors.New("run: no program")
	}
	path := argv[0]
	key := strings.Join(argv, "\x00")
	if r.busy.stuck(key) {
		return runResult{}, errBusy
	}
	if err := checkProgramName(path); err != nil {
		return runResult{}, err
	}
	dir, err := workDir()
	if err != nil {
		return runResult{}, fmt.Errorf("start the program: %w", err)
	}
	tr, err := newTree()
	if err != nil {
		return runResult{}, fmt.Errorf("start the program: %w", err)
	}
	out := &cappedWriter{max: maxOutput}
	// The file run is the path the command gives, the one the checker
	// passed, and the process sees it as its name.
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Env = commandEnv(runtime.GOOS, os.LookupEnv)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.WaitDelay = waitDelay
	tr.prepare(cmd)
	cmd.Cancel = func() error { return tr.kill(cmd) }
	if r.noKill {
		cmd.Cancel = func() error { return nil }
		cmd.WaitDelay = 0
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		tr.release(nil)
		// exec's start errors name cmd.Path, the program, and no argument.
		return runResult{}, fmt.Errorf("start the program: %w", err)
	}
	if err := tr.attach(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		tr.release(cmd)
		return runResult{}, fmt.Errorf("start the program: %w", err)
	}
	st := &runState{}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		tr.release(cmd)
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
			return runResult{duration: time.Since(start)}, stopped(ctx)
		}
	}
	res := runResult{duration: time.Since(start)}
	if ctx.Err() != nil {
		return res, stopped(ctx)
	}
	ps := cmd.ProcessState
	if ps == nil {
		return res, fmt.Errorf("run the program: %w", werr)
	}
	// werr is an *exec.ExitError for a non-zero exit, or ErrWaitDelay when
	// a descendant held the output open after the program exited; the
	// process state says all the result needs either way.
	res.stdout, res.truncated = out.buf, out.overflow
	if ps.Exited() {
		res.exited, res.exitCode = true, ps.ExitCode()
	}
	return res, nil
}

// stopped is Run's error once ctx ended: errTimeout for its deadline,
// else ctx's own error (the agent stopping).
func stopped(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errTimeout
	}
	return ctx.Err()
}

// cappedWriter keeps the first max bytes written and notes whether more
// came. It never fails a write, so the program is not cut off mid-output
// by a broken pipe; the run's deadline bounds a process that writes
// forever.
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
