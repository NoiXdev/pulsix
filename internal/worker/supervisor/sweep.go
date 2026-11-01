package supervisor

import (
	"errors"
	"fmt"
	"io"
	"syscall"
)

// SweepArg is the reserved first argument that puts the worker binary into
// sweep mode: it kills every process its own uid can signal, then exits.
// The worker runs it through the setuid helper as the play uid between jobs
// (backlog 7.21). A play runs as that uid, and so does its supervisor, so
// the play can kill the supervisor and leave a process of its own behind,
// past the end of the job, waiting for the next one. Nothing of a finished
// job may outlive it, and the play uid owns nothing but plays — a worker
// runs one job at a time — so the sweep kills all of it.
const SweepArg = "__pulsix-play-sweep"

// Sweep is sweep mode's whole body: kill(-1, SIGKILL), which signals every
// process the caller may signal except itself — as the play uid, every
// process of the play uid. Nothing left to kill (ESRCH) is success. It is
// the exit code; a failure is named on stderr.
func Sweep(stderr io.Writer) int {
	return sweep(stderr, syscall.Kill)
}

// sweep is Sweep with the kill call injected for tests.
func sweep(stderr io.Writer, kill func(pid int, sig syscall.Signal) error) int {
	if err := kill(-1, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		_, _ = fmt.Fprintf(stderr, "%ssweep the play uid's processes: %v\n", ErrPrefix, err)
		return 1
	}
	return 0
}
