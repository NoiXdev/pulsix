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
// program as the command gives it, the path the checker passed, which the
// process also sees as its name; the rest are its arguments, unchanged.
// It must return once ctx is done, whatever the process does. A non-zero
// exit code, or a process ended by a signal, is a result, not an error.
// Errors are errTimeout (ctx's deadline passed and the process tree was
// killed; the result's duration is still set), errBusy (an earlier run of
// the same argv could not be stopped and has not exited yet),
// errNoExtension (Windows: the program's name has no extension, so
// Windows would look for another file), or a failure to start the
// program. An error must not quote argv beyond the program path; the
// plugin reports none of a start failure's text but an errno's.
type runner interface {
	Run(ctx context.Context, argv []string) (runResult, error)
}

var (
	errTimeout     = errors.New("the command timed out")
	errBusy        = errors.New("an earlier run of this command has not exited yet")
	errNoExtension = errors.New("the program's name has no extension")
)

// checker checks a path before a run: the program, and every argument
// that is an absolute path (the script an interpreter is given). It walks
// the path from its root one name at a time, following links (and on
// Windows junctions) itself, and judges every directory a name is looked
// up in on the way, those holding a link and those a link leads through
// included, then what the path names. With every one of them safe, nobody
// but the system can change what the path names before the run, so the
// plugin runs the command exactly as configured. It returns one of:
//
//   - nil: the path names a regular file nobody but the system can change,
//     nor swap through a directory on its way;
//   - an error wrapping errNotFile: the path names no regular file (a
//     missing name, a directory, a device, a fifo, a socket, a kernel
//     object behind /proc/<pid>/fd), and nobody but the system can put
//     one there: the directory the name is missing in, or lies in, passed
//     with every one on the way, and a directory the path names passed
//     itself (an interpreter may run what it holds). The plugin refuses
//     such a program and leaves such an argument alone: an argument that
//     names no file (a mount point, a device, a URL path) is no script;
//   - refuse(reason): a directory on the way, a link object (Windows) or
//     the file is one others could change, or the path is refused by its
//     syntax alone, without a stat (Windows: a network path, a file path
//     in device form). reason says why without the path; only then does
//     the plugin report "<path>: <reason>";
//   - any other error: the path could not be checked (a failed stat, an
//     unreadable access list, too many links or a loop). The plugin fails
//     closed and reports a fixed text, naming the program but never an
//     argument, whose path may be something other than a file name.
//
// form says how an argument wrote the path (fileArgument); the program is
// always formExact.
type checker interface {
	Check(path string, form pathForm) error
}

// pathForm is how a path was written, which on Windows decides how
// strictly the directory a missing last name lies in is judged.
type pathForm int

const (
	// formExact: an absolute path, on Windows with its drive.
	formExact pathForm = iota
	// formRooted: a Windows argument rooted without a drive (`/c`,
	// `/T:30`, `\x`), the form of a switch: its last name may be missing
	// in a directory where others may add subdirectories (C:\), which
	// puts no file there.
	formRooted
)

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
