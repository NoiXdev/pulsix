package requirements

import (
	"context"
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

// errUnreadable: a part of the source's path cannot be looked at because the
// worker may not (permission denied), so whether it leaves the clone is not
// known. The source is refused all the same, but not as outside.
var errUnreadable = errors.New("cannot be read: permission denied")

// errGlob: a part of the source itself holds a glob character, which galaxy
// may expand for a collection directory and this check does not follow.
var errGlob = errors.New("holds a glob character (*, ? or [), which is not supported")

// maxLinkHops bounds the links one path resolves through, as the kernel's
// own limit does; a loop ends there.
const maxLinkHops = 40

// checkSources checks the values a requirements file hands to galaxy and
// names the first one that is refused: a value galaxy may put on a git
// command line that git could read as an option (optionLike), or a source
// galaxy would read from outside the clone (sourceCheck.source,
// sourceError). The whole check shares one lookup budget (treeLimits) and
// ends when ctx does.
func checkSources(ctx context.Context, clone fs.StatFS, cloneDir, fileDir string, p plan) error {
	for _, v := range p.values {
		if optionLike(v.text) {
			return fmt.Errorf("%s: the %s %q starts with \"-\"", p.name, v.field, v.text)
		}
	}
	for _, src := range p.gitSources {
		if frag, ok := gitSubdirectory(src); ok && !insideCheckout(frag) {
			return fmt.Errorf("%s: the source %q names the subdirectory %q, which leads outside the checkout", p.name, src, frag)
		}
	}
	root := requirementsRoot(fileDir, p.name)
	c := newSourceCheck(ctx, clone, defaultLimits())
	for _, src := range p.sources {
		if optionLike(src) {
			return fmt.Errorf("%s: the source %q starts with \"-\"", p.name, src)
		}
		if err := c.source(cloneDir, fileDir, src); err != nil {
			return sourceError(p.name, root, src, err)
		}
	}
	return nil
}

// gitSubdirectory is the fragment galaxy's parse_scm takes from a
// collection git source (ansible.galaxy.collection.concrete_artifact_manager
// .parse_scm): the part before the first comma (the rest is the version),
// the URL fragment as urldefrag finds it (urlparse drops tab, CR and LF
// first and cuts at the first "#"), not percent-decoded. galaxy installs
// from os.path.join(<checkout>, fragment) (_extract_collection_from_git).
// ok is false without a fragment. Only collections use one: a role's git
// source goes to git clone whole (ansible.utils.galaxy.scm_archive_resource).
func gitSubdirectory(src string) (string, bool) {
	s, _, _ := strings.Cut(src, ",")
	s = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(s)
	_, frag, ok := strings.Cut(s, "#")
	return frag, ok
}

// insideCheckout reports a fragment that is a plain relative path inside
// the checkout: not absolute, no backslash, and after galaxy's strip of
// "/" at both ends (parse_scm) no empty and no ".." segment. galaxy would
// make /abs relative by that strip; it is refused all the same.
func insideCheckout(frag string) bool {
	if strings.HasPrefix(frag, "/") || strings.Contains(frag, "\\") {
		return false
	}
	f := strings.Trim(frag, "/")
	if f == "" {
		return true
	}
	for _, seg := range strings.Split(f, "/") {
		if seg == "" || seg == ".." {
			return false
		}
	}
	return true
}

// checkSource checks one source on its own, with the default bounds and
// no deadline (sourceCheck.source).
func checkSource(clone fs.StatFS, cloneDir, fileDir, src string) error {
	return newSourceCheck(context.Background(), clone, defaultLimits()).source(cloneDir, fileDir, src)
}

// sourceCheck is one check of a file's sources: the clone, the context
// that ends it, and the bounds with the lookups spent so far.
type sourceCheck struct {
	ctx     context.Context
	clone   fs.StatFS
	lim     treeLimits
	lookups int
}

func newSourceCheck(ctx context.Context, clone fs.StatFS, lim treeLimits) *sourceCheck {
	return &sourceCheck{ctx: ctx, clone: clone, lim: lim}
}

// spend counts one lookup (a path part, a link hop, a directory entry)
// against the budget and ends the check when its context has ended, with
// the context's cause (context.Cause).
func (c *sourceCheck) spend() error {
	if c.ctx.Err() != nil {
		return context.Cause(c.ctx)
	}
	if c.lookups++; c.lookups > c.lim.lookups {
		return errTooManyLookups
	}
	return nil
}

// source refuses a source that ansible-galaxy (ansible-core 2.21.5) would
// read from outside the clone at cloneDir. Galaxy runs in the requirements
// file's own directory, fileDir (clone-relative), and opens a source in two
// ways, both checked here:
//
//   - As a path, as written (GalaxyRole.install, os.path.isfile(src), for a
//     role; a collection's file, dir or subdirs source,
//     ConcreteArtifactsManager.get_artifact_path): relative to the
//     working directory, without ~ or variable expansion and without
//     normalising. Every string is such a path, a URL too: x://../../y is
//     a relative path when the clone holds a directory x:.
//   - As a file URL, through urllib (GalaxyRole.install for a source with
//     "://", ConcreteArtifactsManager.get_artifact_path, through
//     _download_file, for a collection of type url; fileURLPath).
//
// A path is followed the way the kernel does (resolve): absolute only below
// cloneDir as written, a link resolved before a later "..", nothing in
// .git, no link that leaves the clone, and no glob character galaxy would
// expand (_find_collections_in_subdirs globs a collection directory). A path
// that does not exist is left to galaxy: it then is a name, or galaxy
// fails. Other URLs (git+file:// included) are left to galaxy and git.
// A path that is a directory is walked (walkTree): galaxy reads its files.
// The error is errOutside, errUnreadable (the worker may not look at a part
// of the path), errGlob (a glob character in the source), a bound's
// sentinel, or the context's error.
func (c *sourceCheck) source(cloneDir, fileDir, src string) error {
	if err := c.path(cloneDir, fileDir, src, true); err != nil {
		return err
	}
	if p, ok := fileURLPath(src); ok {
		return c.path(cloneDir, fileDir, p, false)
	}
	return nil
}

// path checks p, relative to fileDir or absolute, as a path into the
// clone. asPath says whether p is the source as galaxy opens it as a path:
// then its own parts are a pattern to galaxy, and a directory it names is
// read (urllib opens no directory).
func (c *sourceCheck) path(cloneDir, fileDir, p string, asPath bool) error {
	dir := parts(fileDir, false)
	if strings.HasPrefix(p, "/") {
		rel, ok := belowDir(cloneDir, p)
		if !ok {
			return errOutside
		}
		dir, p = nil, rel
	}
	r, err := c.resolve(dir, parts(p, asPath))
	if err != nil {
		return err
	}
	if asPath && r.dir {
		return c.walkTree(r.path)
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
// refused, as is a link it reports. Once a part does not exist, the kernel
// opens nothing below it; the rest is still followed by name and refused
// if it climbs out of the clone. The path reached (the existing one, or its
// first missing part) is last confirmed with the clone's own Stat, which
// refuses what its confined root refuses. Every part and hop is spent
// from the budget. resolve says what it reached: the path without links,
// and whether that exists as a directory.
func (c *sourceCheck) resolve(dir, src []part) (reach, error) {
	links, canLink := c.clone.(fs.ReadLinkFS)
	todo := slices.Concat(dir, src)
	var done []string
	missing := "" // the first part that does not exist
	isDir := true // the last part reached; the clone's root is one
	hops := 0
	for len(todo) > 0 {
		if err := c.spend(); err != nil {
			return reach{}, err
		}
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
		case strings.EqualFold(p.name, ".git"):
			return reach{}, errOutside
		case p.glob && strings.ContainsAny(p.name, "*?["):
			return reach{}, errGlob
		}
		cur := path.Join(path.Join(done...), p.name)
		var info fs.FileInfo
		var err error
		if canLink {
			info, err = links.Lstat(cur)
		} else {
			info, err = c.clone.Stat(cur)
		}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			missing = cur
			done = append(done, p.name)
			continue
		case err != nil:
			return reach{}, refusalOf(err)
		case info.Mode()&fs.ModeSymlink != 0:
			hops++
			if !canLink || hops > maxLinkHops {
				return reach{}, errOutside
			}
			target, err := links.ReadLink(cur)
			if err != nil || target == "" || path.IsAbs(target) {
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
		return reach{}, c.confirm(missing)
	}
	reached := path.Join(done...)
	if err := c.confirm(reached); err != nil {
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
// there is fine, a permission error is errUnreadable, any other refusal is
// outside.
func (c *sourceCheck) confirm(name string) error {
	if err := c.spend(); err != nil {
		return err
	}
	if _, err := c.clone.Stat(orDot(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return refusalOf(err)
	}
	return nil
}

// refusalOf is the sentinel for a lookup the clone failed: the worker's own
// lack of permission is errUnreadable, anything else the clone's confined
// root refuses is errOutside.
func refusalOf(err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return errUnreadable
	}
	return errOutside
}

// fileURLPath is the local path urllib opens for src (Python 3.14,
// urllib.request.FileHandler.open_local_file through url2pathname), and
// whether src is a file URL at all. It follows urllib generously, so that
// any string urllib might read as a file URL is checked: the string
// unwrapped as Request does (urllib.parse.unwrap: Python's strip, <...>,
// URL:), the scheme found as urlsplit does (urllib.parse._urlsplit: leading
// C0 controls and spaces dropped, tab, CR and LF removed everywhere, any
// letter case), the authority dropped (a host that resolves to this machine
// is local too, so the path is checked whatever the host), the path cut at
// the first "?" or "#", then percent-decoded (pyUnquote). A path without
// "//" is relative to galaxy's working directory: a collection of type url
// named file:../x.tar.gz was read from beside it (verified).
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
