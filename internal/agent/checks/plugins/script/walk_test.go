package script

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fakeNode is one entry of a fakeFS. unsafe, when set, is why the walk's
// judge refuses it (a directory, a link object or a file).
type fakeNode struct {
	kind   entryKind
	target string // a link's target
	unsafe string
	err    error // what looking the entry up fails with
}

// fakeFS is a walkFS with Unix path rules, the same on every platform. It
// records every judgement the walk asks for, as "<dir> <use>".
type fakeFS struct {
	nodes  map[string]fakeNode
	judged []string
}

func fakeNames(p string) []string {
	var names []string
	for _, n := range strings.Split(p, "/") {
		if n != "" && n != "." {
			names = append(names, n)
		}
	}
	return names
}

func (f *fakeFS) split(path string) (string, []string, error) {
	if !strings.HasPrefix(path, "/") {
		return "", nil, errors.New("not absolute")
	}
	return "/", fakeNames(path), nil
}

func (*fakeFS) join(root string, names []string) string { return root + strings.Join(names, "/") }

func (f *fakeFS) lookup(p string) (entryKind, error) {
	if p == "/" {
		return entryDir, nil
	}
	n, ok := f.nodes[p]
	switch {
	case !ok:
		return 0, errNotFile
	case n.err != nil:
		return 0, n.err
	}
	return n.kind, nil
}

func (f *fakeFS) follow(link, _ string) (string, []string, error) {
	t := f.nodes[link].target
	if strings.HasPrefix(t, "/") {
		return "/", fakeNames(t), nil
	}
	return "", fakeNames(t), nil
}

func (*fakeFS) below(string) error { return errNotFile }

func (f *fakeFS) judgeDir(dir string, use dirUse, which string) error {
	f.judged = append(f.judged, fmt.Sprintf("%s %d", dir, use))
	if r := f.nodes[dir].unsafe; r != "" {
		return refuse(which + " " + r)
	}
	return nil
}

func (f *fakeFS) judgeLink(link string) error {
	if r := f.nodes[link].unsafe; r != "" {
		return refuse("a link on its way " + r)
	}
	return nil
}

func (f *fakeFS) judgeFile(file string) error {
	if r := f.nodes[file].unsafe; r != "" {
		return refuse(r)
	}
	return nil
}

const (
	userOwned = "is owned by uid 1000, not by root"
	openToAll = "is writable by group or others"
)

// fakeTree is a small system: root-owned /usr, /opt, /dev and /proc, a
// user's home, and links between them.
func fakeTree() map[string]fakeNode {
	dir := fakeNode{kind: entryDir}
	link := func(t string) fakeNode { return fakeNode{kind: entryLink, target: t} }
	nodes := map[string]fakeNode{
		"/usr":               dir,
		"/usr/bin":           dir,
		"/usr/bin/sh":        {kind: entryFile},
		"/usr/bin/theirs":    {kind: entryFile, unsafe: "owned by uid 1000, not by root"},
		"/usr/lib":           dir,
		"/usr/lib/x":         {kind: entryFile},
		"/bin":               link("usr/bin"),
		"/home":              dir,
		"/home/u":            {kind: entryDir, unsafe: userOwned},
		"/home/u/sh":         {kind: entryFile},
		"/home/u/sub":        dir,
		"/home/u/bin":        link("/usr/bin"),
		"/home/u/l2":         link("/usr/bin/sh"),
		"/tmp":               {kind: entryDir, unsafe: openToAll},
		"/opt":               dir,
		"/opt/l1":            link("/home/u/l2"),
		"/opt/sh":            link("../usr/bin/sh"),
		"/opt/binlink":       link("/usr/bin"),
		"/opt/lib":           {kind: entryDir, unsafe: userOwned},
		"/opt/lib/x":         {kind: entryFile},
		"/opt/dangling":      link("/opt/nothing"),
		"/opt/dangling-user": link("/home/u/nothing"),
		"/opt/userdir":       link("/home/u"),
		"/opt/theirs-link":   {kind: entryLink, target: "/usr/bin/sh", unsafe: "is owned by S-1-5-32-545"},
		"/opt/loop1":         link("/opt/loop2"),
		"/opt/loop2":         link("loop1"),
		"/opt/fifo":          {kind: entryOther},
		"/opt/closed":        {kind: entryDir},
		"/opt/closed/x":      {err: errors.New("permission denied")},
		"/dev":               dir,
		"/dev/null":          {kind: entryOther},
		"/dev/stderr":        link("/proc/self/fd/2"),
		"/proc":              dir,
		"/proc/self":         link("4711"),
		"/proc/4711":         dir,
		"/proc/4711/status":  {kind: entryFile},
		"/proc/4711/fd":      dir,
		"/proc/4711/fd/2":    link("socket:[815]"),
		"/proc/999":          {kind: entryDir, unsafe: userOwned},
		"/proc/999/fd":       dir,
		"/proc/999/fd/2":     link("socket:[816]"),
		"/chain":             dir,
	}
	// /chain/0 is the first of maxHops links that end at the shell,
	// /chain/-1 one more.
	for i := -1; i < maxHops; i++ {
		nodes[fmt.Sprintf("/chain/%d", i)] = link(fmt.Sprintf("/chain/%d", i+1))
	}
	nodes["/chain/"+fmt.Sprint(maxHops-1)] = link("/usr/bin/sh")
	return nodes
}

// TestWalk: every directory a name is looked up in must pass, those that
// hold a link and those a link leads through included, and a directory
// the path names must pass itself; a missing name, a directory, a device
// and a kernel object's name behind /proc/<pid>/fd name no file; too many
// links cannot be checked.
func TestWalk(t *testing.T) {
	notFile, tooMany := "<no file>", "<too many links>"
	cases := []struct {
		path string
		want string // "" passes; notFile, tooMany, or the refusal
	}{
		{"/usr/bin/sh", ""},
		{"/bin/sh", ""},
		{"/opt/sh", ""},
		{"/opt/binlink/../lib/x", ""}, // ".." climbs from where the link led
		{"/usr/bin/theirs", "owned by uid 1000, not by root"},
		{"/home/u/sh", "its directory " + userOwned},
		{"/home/u/sub/x", "a directory on its way " + userOwned},
		{"/tmp/x.sh", "its directory " + openToAll},
		{"/home/u/bin/sh", "a directory on its way " + userOwned},      // a link in a user's directory
		{"/home/u/bin/evil.sh", "a directory on its way " + userOwned}, // the reviewer's probe
		{"/opt/l1", "its directory " + userOwned},                      // a chain through a user's directory
		{"/opt/lib/x", "its directory " + userOwned},
		{"/opt/theirs-link", "a link on its way is owned by S-1-5-32-545"},
		{"/usr/bin/missing", notFile},
		{"/usr/missing/deeper", notFile},
		{"/home/u/missing", "its directory " + userOwned},
		{"/home/u/missing/deeper", "a directory on its way " + userOwned},
		{"/opt/dangling", notFile},
		{"/opt/dangling-user", "its directory " + userOwned},
		{"/opt/userdir", "the directory it names " + userOwned},
		{"/home/u", "the directory it names " + userOwned},
		{"/tmp", "the directory it names " + openToAll},
		{"/home/u/bin", "its directory " + userOwned},
		{"/opt/fifo", notFile},
		{"/opt/fifo/below", notFile},
		{"/usr/bin/sh/below", notFile},
		{"/usr/bin", notFile},
		{"/", notFile},
		{"/dev/null", notFile},
		{"/dev/stderr", notFile}, // socket:[815] is a missing name in a root-owned fd directory
		{"/proc/self/status", ""},
		{"/proc/999/fd/2", "a directory on its way " + userOwned},
		{"/opt/loop1", tooMany},
		{"/opt/loop1/x", tooMany},
		{"/chain/0", ""},
		{"/chain/-1", tooMany},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			err := walk(&fakeFS{nodes: fakeTree()}, tc.path, formExact)
			switch tc.want {
			case "":
				if err != nil {
					t.Fatalf("walk = %v, want it to pass", err)
				}
			case notFile:
				if !errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
					t.Fatalf("walk = %v, want errNotFile", err)
				}
			case tooMany:
				if err == nil || errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
					t.Fatalf("walk = %v, want an error that is no refusal and not errNotFile", err)
				}
			default:
				if !errors.Is(err, errRefused) || err.Error() != tc.want {
					t.Fatalf("walk = %v, want the refusal %q", err, tc.want)
				}
			}
		})
	}
}

// TestWalkJudgesEachDirectoryForItsUse: every lookup directory once; the
// file's own directory, and the one holding a link that is the path's
// last name (the directory the program runs from), as holders; a
// directory the path names itself; a missing name's directory by whether
// more names follow and how the path was written.
func TestWalkJudgesEachDirectoryForItsUse(t *testing.T) {
	use := func(dir string, u dirUse) string { return fmt.Sprintf("%s %d", dir, u) }
	f := &fakeFS{nodes: fakeTree()}
	_ = walk(f, "/usr/bin/missing", formRooted)
	if want := []string{use("/", useLookup), use("/usr", useLookup), use("/usr/bin", useLookup), use("/usr/bin", useMissingRooted)}; !slices.Equal(f.judged, want) {
		t.Errorf("rooted: judged %q, want %q", f.judged, want)
	}
	for path, want := range map[string][]string{
		"/opt/sh": {use("/", useLookup), use("/opt", useLookup), use("/opt", useHolds),
			use("/usr", useLookup), use("/usr/bin", useLookup), use("/usr/bin", useHolds)},
		"/usr/bin": {use("/", useLookup), use("/usr", useLookup), use("/usr/bin", useNamed)},
		"/bin/sh":  {use("/", useLookup), use("/usr", useLookup), use("/usr/bin", useLookup), use("/usr/bin", useHolds)},
		"/usr/bin/missing": {use("/", useLookup), use("/usr", useLookup), use("/usr/bin", useLookup),
			use("/usr/bin", useMissing)},
		"/usr/missing/deeper": {use("/", useLookup), use("/usr", useLookup), use("/usr", useMissingWay)},
	} {
		f := &fakeFS{nodes: fakeTree()}
		_ = walk(f, path, formExact)
		if !slices.Equal(f.judged, want) {
			t.Errorf("%s: judged %q, want %q", path, f.judged, want)
		}
	}
}

// TestWalkCouldNotCheck: a lookup that fails other than by a missing
// name is neither a refusal nor errNotFile.
func TestWalkCouldNotCheck(t *testing.T) {
	err := walk(&fakeFS{nodes: fakeTree()}, "/opt/closed/x", formExact)
	if err == nil || errors.Is(err, errNotFile) || errors.Is(err, errRefused) {
		t.Fatalf("walk = %v, want an error that is neither errNotFile nor a refusal", err)
	}
}
