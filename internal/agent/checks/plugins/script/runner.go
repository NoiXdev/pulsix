package script

import (
	"context"
	"errors"
	"time"
)

// maxOutput caps the standard output read per run: 64 KiB.
const maxOutput = 64 << 10

// runResult is one run of a command.
type runResult struct {
	stdout    []byte        // at most maxOutput bytes
	truncated bool          // the process wrote more than maxOutput
	exitCode  int           // valid when exited
	exited    bool          // ran to an exit; false when killed or timed out
	duration  time.Duration // wall time of the run
}

// runner runs argv directly, never through a shell: argv[0] is the
// program, the rest its arguments. It must return once ctx is done,
// whatever the process does. A non-zero exit code, or a process ended by
// a signal, is a result, not an error. Errors are errTimeout (ctx's
// deadline passed and the process tree was killed; the result's duration
// is still set), errBusy (an earlier run of the same argv could not be
// stopped and has not exited yet), or a failure to start the program. An
// error must not quote argv beyond the program path; the plugin reports
// none of a start failure's text but an errno's.
type runner interface {
	Run(ctx context.Context, argv []string) (runResult, error)
}

var (
	errTimeout = errors.New("the command timed out")
	errBusy    = errors.New("an earlier run of this command has not exited yet")
)

// checker checks a file before a run: the program, and every argument
// that is an absolute path naming a regular file (the script an
// interpreter is given). Check resolves symlinks and refuses a file others
// could change; the error says why, without the path, since the plugin
// reports it as "<path>: <reason>".
//
// When path does not name a regular file (missing, a directory, a device)
// Check returns an error wrapping errNotFile. The plugin refuses such a
// program, and leaves such an argument alone: an argument that names no
// file (a mount point, a device, a URL) is no script.
type checker interface {
	Check(path string) error
}

var errNotFile = errors.New("does not exist or is not a regular file")
