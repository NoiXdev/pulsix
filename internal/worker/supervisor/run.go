package supervisor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// The stop phase's poll interval and its bound on SIGKILL passes. 50 ms keeps
// a cancel snappy without turning the /proc walk into a busy loop; 100 passes
// is five seconds of KILLing, far beyond any tree that can still be stopped.
const (
	stopPoll      = 50 * time.Millisecond
	maxKillPasses = 100
)

// reaped is one child collected by wait4: its pid and how it ended.
type reaped struct {
	Pid    int
	Status syscall.WaitStatus
}

// system is everything supervise needs from the operating system. It is an
// interface so the loop is tested with scripted fakes on any OS; the Linux
// implementation is in system_linux.go.
type system interface {
	procTable
	// SetSubreaper makes the supervisor a child subreaper, so every
	// orphaned descendant is reparented to it instead of to PID 1.
	SetSubreaper() error
	// Start starts argv (argv[0] is an absolute path) as a child with the
	// supervisor's stdin, stdout, stderr and environment, in the
	// supervisor's own process group, and returns its pid. It must not
	// wait for the child: Reap is the only thing that collects children.
	Start(argv []string) (pid int, err error)
	// DropStdout points the supervisor's own fd 1 at /dev/null.
	DropStdout() error
	// Reap collects every child that has ended, without blocking, and
	// returns them. It returns an empty slice when nothing is reapable.
	Reap() ([]reaped, error)
}

// errRefused marks the failures that happen before the runner is started:
// the supervisor then ran nothing, and must exit non-zero without a status
// to pass through. Its text is what follows ErrPrefix in RefusedPrefix, so a
// refusal's line starts with RefusedPrefix and nothing else's does.
var errRefused = errors.New("refused")

// supervise runs argv as the supervisor's child and returns its wait status.
//
// It becomes a subreaper first, so that nothing the runner starts can escape
// to PID 1, and refuses to start the runner at all if it cannot: a play the
// supervisor cannot follow is a play it cannot stop. Once the runner is
// running it drops its own stdout, because the worker reads the runner's
// event stream from that pipe until EOF and a supervisor holding a copy
// open would keep the stream open for as long as it lives.
//
// The loop then waits on events, which must carry SIGCHLD and SIGTERM. It
// never polls while the play runs, so a quiet play costs nothing:
//   - SIGCHLD reaps whatever has ended, orphans included, so nothing of the
//     run lingers as a zombie, and notes the runner's status when the runner
//     is among them.
//   - SIGTERM (a cancel, a broken event stream, a worker shutdown) stops the
//     whole tree.
//
// Once the runner's status is known, or on SIGTERM, it stops the tree with
// stopTree, so that whatever the runner left behind is TERMed and KILLed
// too, and then reaps, polling at stopPoll, until the runner's status is
// known. That last poll is what bounds a wedged reap path: a runner that
// does not die even of SIGKILL keeps the supervisor alive, but asleep,
// until the worker's own backstop KILLs it.
//
// The returned error wraps errRefused exactly when the runner never
// started. Otherwise the status is the runner's, always, and any error
// beside it (a tree that would not drain, a failed signal, a stdout that
// could not be dropped) is reported alongside it but never replaces it: the
// worker must still read the run's real outcome.
func supervise(sys system, clk clock, events <-chan os.Signal, grace time.Duration, argv []string) (syscall.WaitStatus, error) {
	if err := sys.SetSubreaper(); err != nil {
		return 0, fmt.Errorf("%w: become a child subreaper: %w", errRefused, err)
	}
	pid, err := sys.Start(argv)
	if err != nil {
		return 0, fmt.Errorf("%w: start %s: %w", errRefused, argv[0], err)
	}

	var (
		status  syscall.WaitStatus
		known   bool
		errs    []error
		reapErr error // the first failed Reap; a wedged one repeats itself
	)
	if err := sys.DropStdout(); err != nil {
		errs = append(errs, fmt.Errorf("drop stdout: %w", err))
	}
	// reap collects what has ended and reports whether it worked.
	reap := func() bool {
		rs, err := sys.Reap()
		for _, r := range rs {
			if r.Pid == pid {
				status, known = r.Status, true
			}
		}
		if err != nil && reapErr == nil {
			reapErr = fmt.Errorf("reap: %w", err)
		}
		return err == nil
	}

wait:
	for !known {
		sig, ok := <-events
		if !ok {
			// Nothing will wake the loop again; waiting on would spin on
			// the closed channel, and ending without a stop would leave
			// the play unsupervised.
			break wait
		}
		switch sig {
		case syscall.SIGCHLD:
			// A supervisor that cannot reap cannot learn that the runner
			// ended, nor keep the tree free of zombies; stopping the play
			// is the only safe answer.
			if !reap() {
				break wait
			}
		case syscall.SIGTERM:
			break wait
		}
	}

	if err := stopTree(sys, clk, grace, stopPoll, maxKillPasses); err != nil {
		errs = append(errs, fmt.Errorf("stop the play: %w", err))
	}
	// Reap at least once even when the runner's status is already known,
	// to collect whatever the stop just ended.
	for {
		reap()
		if known {
			break
		}
		clk.Sleep(stopPoll)
	}
	return status, errors.Join(append(errs, reapErr)...)
}

// Main is the supervisor's whole life: args is everything after Arg, and the
// return value is the code to exit with. Every failure is written to stderr
// as one line starting with ErrPrefix; a refusal (the runner never started)
// starts with RefusedPrefix, so the worker can tell a supervisor that ran no
// play from one that reports beside the runner's status.
//
// When the runner exited, Main returns its exit code. When a signal killed
// the runner, Main resets that signal to its default disposition and raises
// it on itself, so the worker's ProcessState reports the same signal it
// would have seen from the runner; it returns 128+sig only if it is still
// alive afterwards. When the supervisor refused to run the play, it returns
// 1.
func Main(args []string) int {
	return run(args, mainDeps{
		stderr:    os.Stderr,
		newSystem: newSystem,
		clock:     realClock{},
		notify:    notifySignals,
		raise:     raise,
	})
}

// mainDeps is what Main takes from the process, injected so run is tested
// without a real process tree, real signals, or dying.
type mainDeps struct {
	stderr    io.Writer
	newSystem func() (system, error)
	clock     clock
	notify    func() <-chan os.Signal
	raise     func(syscall.Signal) error
}

func run(args []string, d mainDeps) int {
	report := func(err error) {
		// errors.Join separates with newlines; the worker reads one line.
		msg := strings.ReplaceAll(err.Error(), "\n", "; ")
		_, _ = fmt.Fprintln(d.stderr, ErrPrefix+msg)
	}

	grace, argv, err := parseArgs(args)
	if err != nil {
		report(fmt.Errorf("%w: parse arguments: %w", errRefused, err))
		return 1
	}
	sys, err := d.newSystem()
	if err != nil {
		report(fmt.Errorf("%w: %w", errRefused, err))
		return 1
	}
	// notify must be in place before the runner starts, or a runner that
	// ends at once would end unnoticed.
	ws, err := supervise(sys, d.clock, d.notify(), grace, argv)
	if err != nil {
		report(err)
		if errors.Is(err, errRefused) {
			return 1
		}
	}

	code, sig := exitFor(ws)
	if sig == 0 {
		return code
	}
	if err := d.raise(sig); err != nil {
		report(fmt.Errorf("re-raise %v: %w", sig, err))
	}
	return 128 + int(sig)
}

// notifySignals delivers SIGCHLD and SIGTERM on one channel without ever
// losing a SIGTERM. os/signal drops a signal when the channel is full, so a
// single shared channel could fill with SIGCHLDs during a busy stretch and
// drop the cancel. Each signal therefore gets a one-slot channel of its
// own: a pending SIGCHLD already means "reap", so dropping another one loses
// nothing, and a pending SIGTERM is all a cancel needs. The goroutine merges
// the two; it lives as long as the process, which Main ends.
func notifySignals() <-chan os.Signal {
	chld := make(chan os.Signal, 1)
	term := make(chan os.Signal, 1)
	signal.Notify(chld, syscall.SIGCHLD)
	signal.Notify(term, syscall.SIGTERM)
	events := make(chan os.Signal)
	go func() {
		for {
			select {
			case s := <-chld:
				events <- s
			case s := <-term:
				events <- s
			}
		}
	}()
	return events
}

// realClock is the wall clock.
type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }
