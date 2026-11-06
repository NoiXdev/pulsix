package requirements

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// errOutside: galaxy would read the source from outside the repository's
// clone, or from git's own files there (spec, Constraints "Local sources",
// owner answer 2).
var errOutside = errors.New("is outside the repository")

// maxLinkHops bounds the links one source resolves through, as the kernel's
// own limit does; a loop ends there.
const maxLinkHops = 40

// checkSources checks every source a requirements file names and names
// the first one that is refused: one galaxy would hand to git as an option
// (optionLike), or one checkSource refuses (sourceError).
func checkSources(clone fs.StatFS, cloneDir, fileDir string, p plan) error {
	root := requirementsRoot(fileDir, p.name)
	for _, src := range p.sources {
		if optionLike(src) {
			return fmt.Errorf("%s: the source %q starts with \"-\"", p.name, src)
		}
		if err := checkSource(clone, cloneDir, fileDir, src); err != nil {
			return sourceError(p.name, root, src, err)
		}
	}
	return nil
}

// checkSource refuses a source that ansible-galaxy (ansible-core 2.21.5)
// would read from outside the clone at cloneDir. Galaxy runs in the
// requirements file's own directory, fileDir (clone-relative), and opens a
// source in two ways, both checked here:
//
//   - As a path, as written (galaxy/role.py:249 os.path.isfile(src) for a
//     role; a collection's file, dir or subdirs source,
//     galaxy/collection/concrete_artifact_manager.py:228): relative to the
//     working directory, without ~ or variable expansion and without
//     normalising. Every string is such a path, a URL too: x://../../y is
//     a relative path when the clone holds a directory x:.
//   - As a file URL, through urllib (role.py:251-252 for a source with
//     "://", concrete_artifact_manager.py:207 for a collection of type url;
//     fileURLPath).
//
// A path is followed the way the kernel does (resolve): absolute only below
// cloneDir as written, a link resolved before a later "..", nothing in
// .git, no link that leaves the clone, and no glob character galaxy would
// expand (dataclasses.py:137-149 globs a collection directory). A path
// that does not exist is left to galaxy: it then is a name, or galaxy
// fails. Other URLs (git+file:// included) are left to galaxy and git.
// A path that is a directory is walked (walkTree): galaxy reads its files.
func checkSource(clone fs.StatFS, cloneDir, fileDir, src string) error {
	return checkSourceWithin(clone, cloneDir, fileDir, src, treeLimits{entries: maxTreeEntries, depth: maxTreeDepth})
}

// checkSourceWithin is checkSource with the walk's bounds as a parameter.
func checkSourceWithin(clone fs.StatFS, cloneDir, fileDir, src string, lim treeLimits) error {
	if err := checkPath(clone, cloneDir, fileDir, src, true, lim); err != nil {
		return err
	}
	if p, ok := fileURLPath(src); ok {
		return checkPath(clone, cloneDir, fileDir, p, false, lim)
	}
	return nil
}

// checkPath checks p, relative to fileDir or absolute, as a path into the
// clone. asPath says whether p is the source as galaxy opens it as a path:
// then its own parts are a pattern to galaxy, and a directory it names is
// read (urllib opens no directory).
func checkPath(clone fs.StatFS, cloneDir, fileDir, p string, asPath bool, lim treeLimits) error {
	dir := parts(fileDir, false)
	if strings.HasPrefix(p, "/") {
		rel, ok := belowDir(cloneDir, p)
		if !ok {
			return errOutside
		}
		dir, p = nil, rel
	}
	r, err := resolve(clone, dir, parts(p, asPath))
	if err != nil {
		return err
	}
	if asPath && r.dir {
		return walkTree(clone, r.path, lim)
	}
	return nil
}

// belowDir is p relative to dir when p is dir or below it, as written: p
// is not cleaned, so a ".." in it stays for resolve.
func belowDir(dir, p string) (string, bool) {
	dir = strings.TrimSuffix(path.Clean(dir), "/")
	switch {
	case p == dir:
		return "", true
	case strings.HasPrefix(p, dir+"/"):
		return p[len(dir)+1:], true
	}
	return "", false
}

type part struct {
	name string
	glob bool // a part of the source itself: galaxy may glob it
}

func parts(p string, glob bool) []part {
	var ps []part
	for _, name := range strings.Split(p, "/") {
		if name != "" && name != "." {
			ps = append(ps, part{name, glob})
		}
	}
	return ps
}

// resolve walks dir, then src, from the clone's root the way the kernel
// resolves a path from galaxy's working directory: a link is replaced by
// its target before a later ".." takes the link's parent away. Links are
// read through fs.ReadLinkFS; a clone that cannot read them cannot tell
// whether ".." undoes a link, so there any ".." over an existing part is
// refused. Once a part does not exist, the kernel opens nothing below it;
// the rest is still followed by name and refused if it climbs out of the
// clone. The path reached (the existing one, or its first missing part) is
// last confirmed with the clone's own Stat, which refuses what its
// confined root refuses. resolve says what it reached: the path without
// links, and whether that exists as a directory.
func resolve(clone fs.StatFS, dir, src []part) (reach, error) {
	links, canLink := clone.(fs.ReadLinkFS)
	todo := slices.Concat(dir, src)
	var done []string
	missing := "" // the first part that does not exist
	isDir := true // the last part reached; the clone's root is one
	hops := 0
	for len(todo) > 0 {
		p := todo[0]
		todo = todo[1:]
		switch {
		case p.name == "..":
			if len(done) == 0 || !canLink && missing == "" {
				return reach{}, errOutside
			}
			done = done[:len(done)-1]
			isDir = true
			continue
		case missing != "":
			done = append(done, p.name)
			continue
		case strings.EqualFold(p.name, ".git"), p.glob && strings.ContainsAny(p.name, "*?["):
			return reach{}, errOutside
		}
		cur := path.Join(path.Join(done...), p.name)
		var info fs.FileInfo
		var err error
		if canLink {
			info, err = links.Lstat(cur)
		} else {
			info, err = clone.Stat(cur)
		}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			missing = cur
			done = append(done, p.name)
			continue
		case err != nil:
			return reach{}, errOutside
		case info.Mode()&fs.ModeSymlink != 0:
			hops++
			target, err := links.ReadLink(cur)
			if hops > maxLinkHops || err != nil || target == "" || path.IsAbs(target) {
				return reach{}, errOutside
			}
			todo = append(parts(target, false), todo...)
			continue
		}
		done = append(done, p.name)
		isDir = info.IsDir()
		if !isDir && len(todo) > 0 {
			break // the kernel opens nothing below a file
		}
	}
	if missing != "" {
		return reach{}, confirm(clone, missing)
	}
	reached := path.Join(done...)
	if err := confirm(clone, reached); err != nil {
		return reach{}, err
	}
	return reach{path: reached, dir: isDir}, nil
}

// reach is what resolve reached: a clone-relative path without links, and
// whether it exists as a directory.
type reach struct {
	path string
	dir  bool
}

// confirm asks the clone's own Stat about a path reached by resolve: not
// there is fine, any other refusal is outside.
func confirm(clone fs.StatFS, name string) error {
	if name == "" {
		name = "."
	}
	if _, err := clone.Stat(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errOutside
	}
	return nil
}

// fileURLPath is the local path urllib opens for src (Python 3.14,
// urllib/request.py:1478-1491 FileHandler.open_local_file through
// url2pathname, :1671-1707), and whether src is a file URL at all. It
// follows urllib generously, so that any string urllib might read as a
// file URL is checked: the string unwrapped as Request does
// (urllib/parse.py:1105-1115: Python's strip, <...>, URL:), the scheme
// found as urlsplit does (:501-533: leading C0 controls and spaces
// dropped, tab, CR and LF removed everywhere, any letter case), the
// authority dropped (a host that resolves to this machine is local too,
// so the path is checked whatever the host), the path cut at the first
// "?" or "#", then percent-decoded (pyUnquote). A path without "//" is
// relative to galaxy's working directory: a collection of type url named
// file:../x.tar.gz was read from beside it (verified).
func fileURLPath(src string) (string, bool) {
	u := pyStrip(src)
	if len(u) >= 2 && u[0] == '<' && u[len(u)-1] == '>' {
		u = pyStrip(u[1 : len(u)-1])
	}
	if rest, ok := strings.CutPrefix(u, "URL:"); ok {
		u = pyStrip(rest)
	}
	u = strings.TrimLeft(u, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\t\n\x0b\x0c\r\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f ")
	u = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(u)
	scheme, rest, ok := strings.Cut(u, ":")
	if !ok || !strings.EqualFold(scheme, "file") {
		return "", false
	}
	if after, ok := strings.CutPrefix(rest, "//"); ok {
		rest = ""
		if i := strings.IndexAny(after, "/?#"); i >= 0 {
			rest = after[i:]
		}
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	return pyUnquote(rest), true
}

// pyUnquote is Python's urllib.parse.unquote for a file path: every %XX
// with two hex digits becomes that byte, anything else stays as it is.
func pyUnquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	}
	return c - 'a' + 10
}
