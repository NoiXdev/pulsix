package supervisor

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// procTable is the supervisor's window onto its own process tree. It is an
// interface so the stop policy below is tested with scripted fakes on any OS;
// the Linux implementation reads /proc and signals through pidfds.
type procTable interface {
	// Descendants returns the pids of the supervisor's live descendants.
	// Zombies are excluded: a zombie cannot be signalled to death, only
	// reaped, and counting one would make a drained tree look like a
	// survivor.
	Descendants() ([]int, error)
	// Signal sends sig to pid and returns nil for a pid that is gone or is
	// no longer a descendant. A pid can be recycled between Descendants and
	// Signal, so the implementation re-checks descent at signalling time;
	// "not mine any more" is therefore success, not an error.
	Signal(pid int, sig syscall.Signal) error
}

// clock is time as stopTree needs it, injected so the policy's waits are
// verified without sleeping.
type clock interface {
	Now() time.Time
	Sleep(time.Duration)
}

// ErrTreeDidNotDrain reports that descendants were still alive after every
// SIGKILL pass. It is the supervisor's only way to say "I could not stop the
// play", and the supervisor must then say so rather than exit as if it had.
var ErrTreeDidNotDrain = errors.New("descendants survived the kill passes")

// stopTree stops every descendant of the supervisor: SIGTERM, a grace period,
// then SIGKILL.
//
// The TERM phase re-lists descendants on every pass rather than signalling a
// snapshot, because a process that gets TERM may fork on its way out (a
// handler that spawns a cleanup, a shell that starts its next command). Each
// pid is TERMed once; repeating TERM at a process that is busy shutting down
// only restarts its handler. The phase ends when the tree is empty, or when
// grace has passed since the call began.
//
// The KILL phase then lists and SIGKILLs up to maxKillPasses times, polling
// between passes, since KILL can race the same late forks and a pass that
// leaves a new child behind needs another. A tree that is still not empty
// after the last pass is reported as ErrTreeDidNotDrain, naming the pids.
//
// A failing Signal never ends the stop early: nothing of the play may
// survive, and one process the supervisor cannot signal (or one failed TERM)
// must not spare its siblings or skip the KILL phase. Every signal error is
// collected, wrapped with its pid and signal, and returned together with the
// outcome once the tree has drained, or joined with ErrTreeDidNotDrain when
// the kill passes run out.
func stopTree(pt procTable, clk clock, grace time.Duration, poll time.Duration, maxKillPasses int) error {
	deadline := clk.Now().Add(grace)
	termed := make(map[int]bool)
	var sigErrs []error
	// finish returns the collected signal errors joined with the outcome
	// (nil for a drained tree), so every exit reports them.
	finish := func(outcome error) error {
		return errors.Join(append(sigErrs, outcome)...)
	}

	for {
		pids, err := pt.Descendants()
		if err != nil {
			return finish(fmt.Errorf("list descendants: %w", err))
		}
		if len(pids) == 0 {
			return finish(nil)
		}
		var fresh []int
		for _, pid := range pids {
			if !termed[pid] {
				termed[pid] = true
				fresh = append(fresh, pid)
			}
		}
		sigErrs = append(sigErrs, signalAll(pt, fresh, syscall.SIGTERM)...)
		if !clk.Now().Before(deadline) {
			break
		}
		clk.Sleep(poll)
	}

	for range maxKillPasses {
		pids, err := pt.Descendants()
		if err != nil {
			return finish(fmt.Errorf("list descendants: %w", err))
		}
		if len(pids) == 0 {
			return finish(nil)
		}
		sigErrs = append(sigErrs, signalAll(pt, pids, syscall.SIGKILL)...)
		clk.Sleep(poll)
	}

	// The last pass's KILLs had a poll interval to land; look once more
	// before calling the tree undrainable.
	pids, err := pt.Descendants()
	if err != nil {
		return finish(fmt.Errorf("list descendants: %w", err))
	}
	if len(pids) == 0 {
		return finish(nil)
	}
	return finish(fmt.Errorf("%w: pids %v still alive after %d kill passes", ErrTreeDidNotDrain, pids, maxKillPasses))
}

// signalAll sends sig to every pid, continuing past failures, and returns
// each failure wrapped with its pid and signal.
func signalAll(pt procTable, pids []int, sig syscall.Signal) []error {
	var errs []error
	for _, pid := range pids {
		if err := pt.Signal(pid, sig); err != nil {
			errs = append(errs, fmt.Errorf("signal %d with %v: %w", pid, sig, err))
		}
	}
	return errs
}
