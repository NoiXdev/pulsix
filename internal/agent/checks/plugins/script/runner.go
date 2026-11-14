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

// runner runs the file at path directly, never through a shell, with
// argv as its arguments: path is the program the checker resolved and
// passed, argv[0] the program as the command names it (what the process
// sees as its name, which a multi-call binary reads), the rest its
// arguments. It must return once ctx is done,
// whatever the process does. A non-zero exit code, or a process ended by
// a signal, is a result, not an error. Errors are errTimeout (ctx's
// deadline passed and the process tree was killed; the result's duration
// is still set), errBusy (an earlier run of the same path and argv could
// not be stopped and has not exited yet), errNoExtension (Windows: the
// program's name has no extension, so Windows would look for another
// file), or a failure to start the program. An
// error must not quote argv beyond the program path; the plugin reports
// none of a start failure's text but an errno's.
type runner interface {
	Run(ctx context.Context, path string, argv []string) (runResult, error)
}

var (
	errTimeout     = errors.New("the command timed out")
	errBusy        = errors.New("an earlier run of this command has not exited yet")
	errNoExtension = errors.New("the program's name has no extension")
)

// checker checks a file before a run: the program, and every argument
// that is an absolute path naming a regular file (the script an
// interpreter is given). Check resolves symlinks (and on Windows
// junctions) and judges the file they lead to, its directory and every
// directory above it, then returns that resolved path: the plugin runs
// and passes the file that was checked, never the name again, which a
// link in a directory others can write could point elsewhere by then. It
// returns one of:
//
//   - the resolved path and nil: the path names a regular file nobody but
//     the system can change, nor swap through a directory on its way;
//   - an error wrapping errNotFile: the path names no regular file
//     (missing, a dangling link, a directory, a fifo, a device). The
//     plugin refuses such a program; for such an argument it asks
//     CheckPlace, and leaves an argument whose place passes alone: an
//     argument that names no file (a mount point, a device, a URL path)
//     is no script;
//   - refuse(reason): the path names an existing regular file the checker
//     inspected and refuses (writable by others, wrong owner), or a path
//     it refuses by its syntax alone, without a stat (Windows: a network
//     path). reason says why without the path; only then does the plugin
//     report "<path>: <reason>";
//   - any other error: the file could not be checked (a failed stat, an
//     unreadable ACL). The plugin fails closed and reports a fixed text,
//     naming the program but never an argument, whose path may be
//     something other than a file name.
//
// CheckPlace judges where an absolute path that names no regular file
// lives, so a script missing at the check cannot appear before the run in
// a directory others can write. It takes the deepest directory that
// exists on the path's way (the path's own directory when the path itself
// exists, walking up while one is missing), resolves its links, and judges
// it and every directory above it by the rules Check applies to a file's
// directories. It returns nil when they pass, refuse(reason) (reason
// without a path, "its directory …" or "a directory above it …") when one
// it inspected does not, and any other error when it could not check
// them; the plugin echoes the argument only with a refusal.
type checker interface {
	Check(path string) (resolved string, err error)
	CheckPlace(path string) error
}

var (
	errNotFile = errors.New("does not exist or is not a regular file")
	// errRefused is what a refuse error matches (errors.Is).
	errRefused = errors.New("refused")
)

// refuse is the checker's refusal of a file it inspected: its text is
// reason alone, and it matches errRefused.
func refuse(reason string) error { return refusal(reason) }

type refusal string

func (r refusal) Error() string      { return string(r) }
func (refusal) Is(target error) bool { return target == errRefused }
