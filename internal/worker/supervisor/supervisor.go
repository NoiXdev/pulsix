// Package supervisor is the play supervisor: a small process the worker puts
// between itself and ansible-runner so that cancelling a run stops the play,
// not just the runner (backlog 2.107; the leaked-process half of it is 2.108).
//
// Why it exists. The runner's own cancel used to signal ansible-runner's
// process group, and ansible-runner puts the play in sessions and groups of
// its own, so the TERM never reached it. A cancelled run reported "cancelled"
// while the play kept running, reparented to PID 1. A process cannot name
// "everything my run started" by group or session, because any descendant may
// leave both; it can only name it by ancestry. The supervisor therefore makes
// itself a child subreaper: every orphaned descendant, however deep and
// however many times it double-forked, is reparented to the supervisor rather
// than to PID 1. Its descendants are then exactly the play, and stopping the
// play is a matter of walking that tree, which this package does.
//
// This file is the part of the contract that is the same on every OS: the
// argv the worker builds and the supervisor parses, and the mapping from the
// runner's wait status to the supervisor's own exit. stop.go holds the stop
// policy behind a small interface; the Linux implementation (prctl, pidfd,
// wait4) and the supervising loop sit in their own files.
//
// The design, and why each choice was made, is in
// docs/superpowers/specs/2026-10-29-cancel-stops-the-play-design.md.
package supervisor

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"time"
)

// Arg is the reserved first argument that puts the worker binary into
// supervisor mode instead of its normal startup. It is spelled so that no
// real subcommand or flag can collide with it.
const Arg = "__pulsix-play-supervisor"

// ErrPrefix starts every line the supervisor writes to stderr. The runner
// recognises the setuid helper's failures by its own prefix; this one lets a
// job's error name the supervisor when the supervisor itself could not do its
// job, instead of passing for the play's output.
const ErrPrefix = "pulsix-play-supervisor: "

// RefusedPrefix starts the one line the supervisor writes when it refused to
// run the play at all, before the runner started. Every other line it writes
// reports something that went wrong after the runner started (a stop that did
// not drain, a failed signal or reap), and comes beside the runner's own
// status. The worker must tell the two apart: only a refusal means the exit
// is the supervisor's and not the play's, so only a refusal may become the
// run's error in place of a real Ansible exit 1.
const RefusedPrefix = ErrPrefix + "refused: "

// graceFlag and separator are the two literals parseArgs and Command share.
const (
	graceFlag = "--grace"
	separator = "--"
)

// Command returns the argv that follows the supervisor's own path:
//
//	__pulsix-play-supervisor --grace <grace> -- <bin> <args…>
//
// grace is how long the supervisor lets the play drain after TERM before it
// escalates to KILL; the worker passes the runner's kill grace so one number
// governs both. bin must be absolute: the supervisor does no PATH lookup, so
// that what runs is exactly what the worker resolved.
func Command(grace time.Duration, bin string, args []string) []string {
	// A fresh slice, never append onto args: the caller keeps using it.
	cmd := make([]string, 0, 4+1+len(args))
	cmd = append(cmd, Arg, graceFlag, grace.String(), separator, bin)
	return append(cmd, args...)
}

// parseArgs decodes what Command built. args is everything after Arg. It is
// deliberately strict: a supervisor that guessed at a malformed argv could
// supervise the wrong program or wait the wrong time, and the worker is the
// only caller, so any deviation is a bug worth failing loudly on.
func parseArgs(args []string) (grace time.Duration, argv []string, err error) {
	var haveGrace bool
	sep := -1
	for i := 0; i < len(args) && sep < 0; i++ {
		switch args[i] {
		case separator:
			sep = i
		case graceFlag:
			i++
			if i == len(args) {
				return 0, nil, fmt.Errorf("%s needs a value", graceFlag)
			}
			grace, err = time.ParseDuration(args[i])
			if err != nil {
				return 0, nil, fmt.Errorf("parse %s %q: %w", graceFlag, args[i], err)
			}
			if grace <= 0 {
				return 0, nil, fmt.Errorf("%s %q must be positive", graceFlag, args[i])
			}
			haveGrace = true
		default:
			return 0, nil, fmt.Errorf("unknown argument %q", args[i])
		}
	}
	if sep < 0 {
		return 0, nil, errors.New("missing " + separator + " before the program")
	}
	if !haveGrace {
		return 0, nil, errors.New("missing " + graceFlag)
	}
	argv = args[sep+1:]
	if len(argv) == 0 {
		return 0, nil, errors.New("no program after " + separator)
	}
	if !filepath.IsAbs(argv[0]) {
		return 0, nil, fmt.Errorf("program %q is not an absolute path", argv[0])
	}
	return grace, argv, nil
}

// exitFor maps the runner's wait status to how the supervisor must end, so
// the worker reads the supervisor's exit exactly as it would have read the
// runner's: summarize tells failed (2) and unreachable (4) runs apart by
// exit code, and a runner killed by a signal must still surface as a
// negative ProcessState.ExitCode(). Exactly one of code and sig is set: a
// signalled runner gives (0, sig), which the supervisor re-raises on itself
// rather than exiting with a made-up 128+n.
func exitFor(ws syscall.WaitStatus) (code int, sig syscall.Signal) {
	if ws.Signaled() {
		return 0, ws.Signal()
	}
	return ws.ExitStatus(), 0
}

// Probe reports whether this host gives the supervisor what it needs, with
// the same refusal Main would print for the first run. The worker calls it
// at start, so an unsupported host shows before the worker claims a job
// rather than as the error of every playbook job (backlog 2.112).
func Probe() error {
	_, err := newSystem()
	return err
}
