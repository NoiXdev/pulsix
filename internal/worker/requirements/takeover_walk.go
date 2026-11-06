package requirements

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

// nameOverhead is what a held name costs besides its bytes: its string
// header and the slack of the slice it is held in.
const nameOverhead = 32

// listChunk is how many directory entries a walk reads at a time.
const listChunk = 1024

// walker visits trees depth first, a directory's entries in name order. A
// path is built only when its entry is visited; what a walk holds is, per
// directory on its way down, that directory's path and the names of its
// entries. Their bytes (each name plus nameOverhead) are budgeted by
// lim.names, so the memory of a walk stays below that bound (64 MiB by
// default) plus one chunk of listChunk entries, however many entries a tree
// has and however long their names are. The walk ends before the next
// entry once ctx has ended, with ctx's cause.
type walker struct {
	ctx     context.Context
	lim     takeOverLimits
	entries int
	held    int
	peak    int
}

// frame is a listed directory on the walk's way down.
type frame struct {
	dir   string
	names []string
	next  int
	size  int
}

// visitFunc handles the entry p at depth (0 for a tree's top). For a
// directory to descend into, it returns the directory opened; the walk
// lists and closes it.
type visitFunc func(p string, depth int) (*os.File, error)

// walk visits each of tops and everything below it. An error names the path
// it came from.
func (w *walker) walk(tops []string, visit visitFunc) error {
	top := &frame{names: tops}
	for _, name := range tops {
		top.size += len(name) + nameOverhead
	}
	w.entries = len(tops)
	if err := w.hold(top.size); err != nil {
		return fmt.Errorf("take over the install: %w", err)
	}
	stack := []*frame{top}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		if f.next == len(f.names) {
			w.held -= f.size
			stack = stack[:len(stack)-1]
			continue
		}
		if w.ctx.Err() != nil {
			return fmt.Errorf("take over the install: %w", context.Cause(w.ctx))
		}
		p := path.Join(f.dir, f.names[f.next])
		f.next++
		dir, err := visit(p, len(stack)-1)
		if err == nil && dir != nil {
			var next *frame
			next, err = w.list(p, dir)
			_ = dir.Close()
			if err == nil {
				stack = append(stack, next)
			}
		}
		if err != nil {
			return &takeOverError{path: p, err: err}
		}
	}
	return nil
}

// list reads the entries of the directory p, opened as d, spending the
// entry and the name budget as it reads.
func (w *walker) list(p string, d *os.File) (*frame, error) {
	f := &frame{dir: p, size: len(p)}
	if err := w.hold(len(p)); err != nil {
		return nil, err
	}
	for {
		chunk, err := d.ReadDir(listChunk)
		if w.entries += len(chunk); w.entries > w.lim.entries {
			return nil, fmt.Errorf("more than %d files and directories", w.lim.entries)
		}
		for _, e := range chunk {
			name := e.Name()
			if err := w.hold(len(name) + nameOverhead); err != nil {
				return nil, err
			}
			f.size += len(name) + nameOverhead
			f.names = append(f.names, name)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, bare(err)
		}
	}
	slices.Sort(f.names)
	return f, nil
}

// hold spends n bytes of the name budget.
func (w *walker) hold(n int) error {
	if w.held+n > w.lim.names {
		return fmt.Errorf("more than %d bytes of names to hold", w.lim.names)
	}
	w.held += n
	w.peak = max(w.peak, w.held)
	return nil
}

// followLink follows the link p in root as the kernel would: part by part, a
// ".." going to the real parent of what was reached and a further link
// spliced in. Every directory p is in is a real directory. Reaching a part
// that does not exist, or a part below a file, ends the walk without error:
// the kernel stops there as well. Going above root, or an absolute link on
// the way, is errTakeOverLinkOut; more than maxLinkHops links is
// errTakeOverLinkChain. root must be one nothing else changes (the copy).
func (c *copier) followLink(root *os.Root, p string) error {
	var done []string
	if d := path.Dir(p); d != "." {
		done = strings.Split(d, "/")
	}
	todo := []string{path.Base(p)}
	hops := 0
	for len(todo) > 0 {
		if c.lookups++; c.lookups > c.lim.lookups {
			return fmt.Errorf("more than %d lookups to check its symlinks", c.lim.lookups)
		}
		name := todo[0]
		todo = todo[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			if len(done) == 0 {
				return errTakeOverLinkOut
			}
			done = done[:len(done)-1]
			continue
		}
		cur := path.Join(path.Join(done...), name)
		fi, err := root.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return bare(err)
		case fi.Mode()&fs.ModeSymlink != 0:
			if hops++; hops > maxLinkHops {
				return errTakeOverLinkChain
			}
			next, err := root.Readlink(cur)
			if err != nil {
				return bare(err)
			}
			if next == "" || path.IsAbs(next) {
				return errTakeOverLinkOut
			}
			todo = append(strings.Split(next, "/"), todo...)
			continue
		case !fi.IsDir():
			return nil // nothing is opened below a file
		}
		done = append(done, name)
	}
	return nil
}
