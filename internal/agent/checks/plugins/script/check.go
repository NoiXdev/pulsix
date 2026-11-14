package script

import (
	"errors"
	"fmt"
	"slices"
)

// fileChecker is the checker the agent runs with: it walks a path the way
// the system will when the command runs (walk), on the platform's file
// system (osFS, check_unix.go and check_windows.go). Only the system may
// change a file a command runs or hands its interpreter, or what a path
// on its way names; anyone else who could would run code as the agent.
type fileChecker struct{}

func (fileChecker) Check(path string) error {
	if err := judgeSyntax(path); err != nil {
		return err
	}
	return contextFor("check "+path, walk(osFS{}, path))
}

// contextFor wraps err with what was being done, except a refusal, whose
// text is its reason alone (checker), and errNotFile.
func contextFor(doing string, err error) error {
	if _, ok := refusalReason(err); ok || err == nil || errors.Is(err, errNotFile) {
		return err
	}
	return fmt.Errorf("%s: %w", doing, err)
}

// maxHops bounds the links one walk follows, as Linux's MAXSYMLINKS does:
// a longer chain, or a loop, cannot be checked.
const maxHops = 40

var errTooManyLinks = errors.New("too many links on the way, or a loop")

// entryKind is what a name looked up in a directory is, the entry itself,
// not where a link leads.
type entryKind int

const (
	entryDir   entryKind = iota
	entryFile            // a regular file
	entryLink            // a symlink, or on Windows a junction
	entryOther           // a device, a fifo, a socket
	// entryKernel is a directory of a file system the kernel makes up
	// (Linux /proc): nothing below it is a file anyone could put there.
	entryKernel
)

// dirUse is why the walk judges a directory, which on Windows decides the
// rights it must deny others (dirMask); Unix denies every write right for
// every use.
type dirUse int

const (
	// useLookup: a name in it is looked up and found.
	useLookup dirUse = iota
	// useHolds: the path's regular file lies in it.
	useHolds
	// useMissing: the path's last name is missing in it, so nobody but
	// the system may put a file there.
	useMissing
	// useMissingWay: a name with more names after it is missing in it,
	// so nobody but the system may make the way either.
	useMissingWay
)

// walkFS is what the walk reads from the file system, per platform. Paths
// it is given have no link on their way but maybe the last name.
type walkFS interface {
	// split splits an absolute path into its root and names, as the
	// platform reads it (Windows cleans "." and ".." by text first).
	split(path string) (root string, names []string, err error)
	// join makes a path of root and names.
	join(root string, names []string) string
	// lookup says what the entry at p is without following it; a name
	// that is not there is errNotFile.
	lookup(p string) (entryKind, error)
	// follow reads the link at p, which lies in dir, and says where the
	// walk goes on: a root and names, or "" and names that go on from
	// dir; a target that is no path is errNotFile.
	follow(p, dir string) (root string, names []string, err error)
	// judgeDir judges dir for use; which ("its directory", "a directory
	// on its way") starts a refusal.
	judgeDir(dir string, use dirUse, which string) error
	// judgeLink judges the link object at p (Windows: its owner and
	// access list, since whoever may write it may re-point it).
	judgeLink(p string) error
	// judgeFile judges the regular file at p.
	judgeFile(p string) error
}

// walk resolves path itself, one name at a time from its root, following
// links with its own loop, and judges every directory a name is looked up
// in: the path's own directories, the one holding each link and every one
// a link's target passes through. With all of them safe, nobody but the
// system can change what the path names between the check and the run.
//
//   - A regular file at the end: its directory as the file's holder, then
//     the file itself. The only nil.
//   - A missing name: the directory it is missing in (nobody else may
//     create it there), then errNotFile. A link that leads nowhere is just
//     that.
//   - A directory, a device, a fifo, a socket, a path below a file, or
//     anything under a kernel file system (/proc): errNotFile, once the
//     directories on the way passed.
//
// A ".." climbs from where the walk is, so after a link from where the
// link led (Windows has none left: split cleaned them away).
func walk(fsys walkFS, path string) error {
	root, queue, err := fsys.split(path)
	if err != nil {
		return err
	}
	w := walker{fs: fsys, passed: map[judgement]bool{}}
	var stack []string // the names from root to the current directory
	hops := 0
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		dir := fsys.join(root, stack)
		last := len(queue) == 0
		if err := w.judge(dir, useLookup, last); err != nil {
			return err
		}
		p := fsys.join(root, append(slices.Clip(stack), name))
		kind, err := fsys.lookup(p)
		if errors.Is(err, errNotFile) {
			use := useMissingWay
			if last {
				use = useMissing
			}
			if err := w.judge(dir, use, last); err != nil {
				return err
			}
			return errNotFile
		}
		if err != nil {
			return err
		}
		switch kind {
		case entryDir:
			stack = append(stack, name)
		case entryLink:
			if hops++; hops > maxHops {
				return errTooManyLinks
			}
			if err := fsys.judgeLink(p); err != nil {
				return err
			}
			r, names, err := fsys.follow(p, dir)
			if err != nil {
				return err
			}
			if r != "" {
				root, stack = r, nil
			}
			queue = append(slices.Clip(names), queue...)
		case entryFile:
			if !last {
				return errNotFile
			}
			if err := w.judge(dir, useHolds, true); err != nil {
				return err
			}
			return fsys.judgeFile(p)
		default:
			return errNotFile
		}
	}
	return errNotFile
}

// walker remembers the judgements that passed during one walk, so a
// directory met again (the root after an absolute link) is read once.
type walker struct {
	fs     walkFS
	passed map[judgement]bool
}

type judgement struct {
	dir string
	use dirUse
}

// judge judges dir for use; own says whether the name looked up in it is
// the path's last (its directory) or one on the way.
func (w *walker) judge(dir string, use dirUse, own bool) error {
	k := judgement{dir, use}
	if w.passed[k] {
		return nil
	}
	which := "a directory on its way"
	if own {
		which = "its directory"
	}
	if err := w.fs.judgeDir(dir, use, which); err != nil {
		return err
	}
	w.passed[k] = true
	return nil
}
