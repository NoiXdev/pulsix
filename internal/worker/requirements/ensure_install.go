package requirements

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/noixdev/pulsix/internal/worker/urlcred"
)

// counted is a counted file, read and checked.
type counted struct {
	fileData
	plan
	dir string // the file's clone-relative directory, "" for the clone root
	n   int    // its number in Files order, from 1: input/<n>.yml
}

// installs reports whether the file installs anything.
func (c counted) installs() bool { return c.roles || c.collections }

// Ensure returns the entry that holds the roles and collections of req's
// commit and files, installing them first when this worker has no entry
// for them yet (spec goal 1). One install per commit, published atomically:
// under the cache's lock, an entry that opens as a real directory is used
// as it is; otherwise the files are installed into a temporary directory,
// taken over into a worker-owned copy and renamed to the entry name.
//
// The files are read and checked before the cache is touched. A commit
// whose files count nothing, or list nothing, gets a zero Entry and no
// cache. The whole step, the source check included, is bounded by the
// service's timeout; whatever the deadline ends reports "timed out after
// <timeout>". Lines for the run log go to req.Line (spec goal 6).
//
// Only what decides the returned entry fails the step: the files, the
// cache and entry refusals (OpenDir), the lock, galaxy, the take-over and
// the publishing rename. A cleanup that fails (a leftover temp directory,
// the legacy galaxy directory, the temp entry after its take-over, marking
// the entry used, eviction) is logged as a warning and the entry is still
// returned. URL credentials are removed from every error's text.
func (s *Service) Ensure(ctx context.Context, req Request) (Entry, error) {
	entry, err := s.ensure(ctx, req)
	if err != nil {
		return Entry{}, withoutUserinfo(err)
	}
	return entry, nil
}

// withoutUserinfo is err with URL credentials removed from its text; it
// still unwraps to err.
func withoutUserinfo(err error) error {
	msg := urlcred.Redact(err.Error())
	if msg == err.Error() {
		return err
	}
	return &refusal{msg: msg, err: err}
}

// warn logs a cleanup that failed; the step goes on.
func (s *Service) warn(msg, path string, err error) {
	s.log.Warn(msg, "path", path, "err", urlcred.Redact(err.Error()))
}

func (s *Service) ensure(ctx context.Context, req Request) (Entry, error) {
	if req.Files.Empty() {
		return Entry{}, nil
	}
	if !fullCommit.MatchString(req.Commit) {
		return Entry{}, fmt.Errorf("commit %q is not a full commit id", req.Commit)
	}
	out := req.Line
	if out == nil {
		out = func(string) {}
	}
	step, cancel := context.WithTimeoutCause(ctx, s.timeout, timedOut{s.timeout})
	defer cancel()
	files, err := readFiles(step, req)
	if err != nil {
		return Entry{}, err
	}
	if !anyInstalls(files) {
		out(nothingLine(files))
		return Entry{}, nil
	}

	cacheDir := CacheDir(req.CloneDir)
	root, err := openCache(cacheDir)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = root.Close() }()
	unlock, err := lockWith(step, root, s.lockWaiting)
	if err != nil {
		return Entry{}, err
	}
	defer unlock()
	if err := sweepTemp(root, func(name string) error {
		return s.galaxy.RemoveAll(filepath.Join(cacheDir, name))
	}); err != nil {
		s.warn("remove leftover requirements installs", cacheDir, err)
	}
	if err := removeLegacy(req.CloneDir); err != nil {
		s.warn("remove the legacy galaxy directory", filepath.Clean(req.CloneDir)+".galaxy", err)
	}

	data := make([]fileData, len(files))
	for i, f := range files {
		data[i] = f.fileData
	}
	name := key(req.Commit, req.Files.Root, data)
	dir := filepath.Join(cacheDir, name)
	entry, err := OpenDir(dir)
	switch {
	case err == nil:
		_ = entry.Close()
		out("Requirements: using the install for commit " + req.Commit[:7] + " on this worker")
	case errors.Is(err, fs.ErrNotExist):
		in := install{req: req, root: root, cacheDir: cacheDir, name: name, files: files, out: out}
		if err := s.install(step, in); err != nil {
			return Entry{}, err
		}
		if entry, err = OpenDir(dir); err != nil {
			return Entry{}, err
		}
		_ = entry.Close()
	default:
		return Entry{}, err
	}

	if err := s.markUsed(root, name); err != nil {
		s.warn("mark the requirements entry used", dir, err)
	}
	if err := evict(root, maxEntries, name); err != nil {
		s.warn("evict requirements entries", cacheDir, err)
	}
	return newEntry(dir), nil
}

// markUsed sets the entry's times to now: the eviction keeps the most
// recently used.
func (s *Service) markUsed(root *os.Root, name string) error {
	if s.touch != nil {
		return s.touch(name)
	}
	now := s.clock.Now()
	return root.Chtimes(name, now, now)
}

// readFiles reads, parses and checks every counted file through the
// clone's confined root.
func readFiles(ctx context.Context, req Request) ([]counted, error) {
	files := make([]counted, 0, len(req.Files.Paths))
	for i, p := range req.Files.Paths {
		name := relToRoot(req.Files.Root, p)
		data, err := readFile(req.Clone, p, name)
		if err != nil {
			return nil, err
		}
		pl, err := parseFile(name, data)
		if err != nil {
			return nil, err
		}
		dir := path.Dir(p)
		if dir == "." {
			dir = ""
		}
		if err := checkSources(ctx, req.Clone, req.CloneDir, dir, pl); err != nil {
			return nil, err
		}
		files = append(files, counted{fileData: fileData{path: p, data: data}, plan: pl, dir: dir, n: i + 1})
	}
	return files, nil
}

// relToRoot is the clone-relative path p relative to the requirements root.
func relToRoot(root, p string) string {
	if root == "" {
		return p
	}
	return strings.TrimPrefix(p, root+"/")
}

func anyInstalls(files []counted) bool {
	for _, f := range files {
		if f.installs() {
			return true
		}
	}
	return false
}

// nothingLine is the line of a commit whose files list nothing.
func nothingLine(files []counted) string {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.name
	}
	verb := "lists"
	if len(names) > 1 {
		verb = "list"
	}
	return "Requirements: " + strings.Join(names, ", ") + " " + verb + " nothing to install"
}

// openCache opens the cache directory, creating it (0700) when it does not
// exist.
func openCache(dir string) (*os.Root, error) {
	root, err := OpenDir(dir)
	if !errors.Is(err, fs.ErrNotExist) {
		return root, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("make requirements cache %s: %w", dir, err)
	}
	return OpenDir(dir)
}

// install is one install into the cache that root is opened on.
type install struct {
	req      Request
	root     *os.Root
	cacheDir string
	name     string // the entry name to publish
	files    []counted
	out      func(string)
}

// install runs galaxy for every file into a fresh temp entry, takes the
// result over and publishes it under in.name. On any failure nothing is
// published and the temp entries are removed. step bounds galaxy and the
// take-over.
func (s *Service) install(step context.Context, in install) error {
	tmp, err := mkTemp(in.root)
	if err != nil {
		return err
	}
	tmpDir := filepath.Join(in.cacheDir, tmp)
	discard := func(err error) error {
		if rerr := s.galaxy.RemoveAll(tmpDir); rerr != nil {
			err = errors.Join(err, fmt.Errorf("remove %s: %w", tmpDir, rerr))
		}
		return err
	}
	if err := prepare(in.root, tmp, in.files); err != nil {
		return discard(err)
	}

	var names []string
	for _, f := range in.files {
		if f.installs() {
			names = append(names, f.name)
		}
	}
	in.out("Requirements: installing for commit " + in.req.Commit[:7] + " from " + strings.Join(names, ", "))
	start := s.clock.Now()
	for _, f := range in.files {
		for _, kind := range f.kinds() {
			cmd := Install{
				Kind:         kind,
				File:         filepath.Join(tmpDir, "input", strconv.Itoa(f.n)+".yml"),
				Dir:          filepath.Join(in.req.CloneDir, filepath.FromSlash(f.dir)),
				Entry:        tmpDir,
				CloneDir:     in.req.CloneDir,
				RepoCacheDir: in.req.RepoCacheDir,
			}
			lines := &commandLines{out: in.out}
			err := s.galaxy.Install(step, cmd, lines.line)
			lines.done()
			if err != nil {
				return discard(commandError(step, kind, f.name, err))
			}
		}
	}

	pub, err := takeOver(step, in.root, tmp)
	if err != nil {
		return discard(err)
	}
	if err := s.galaxy.RemoveAll(tmpDir); err != nil {
		s.warn("remove the install after its take-over", tmpDir, err)
	}
	if err := in.root.Rename(pub, in.name); err != nil {
		return removePublished(in.root, pub, fmt.Errorf("publish requirements entry %s: %w", in.name, err))
	}
	in.out(fmt.Sprintf("Requirements: installed in %.1f s", s.clock.Now().Sub(start).Seconds()))
	return nil
}

// kinds are the installs the file asks for, roles before collections.
func (c counted) kinds() []Kind {
	var kinds []Kind
	if c.roles {
		kinds = append(kinds, KindRole)
	}
	if c.collections {
		kinds = append(kinds, KindCollection)
	}
	return kinds
}

// prepare lays out the temp entry tmp: input/ with a copy of every counted
// file and galaxy.cfg, and the empty home/, tmp/, roles/ and collections/.
func prepare(root *os.Root, tmp string, files []counted) error {
	for _, d := range []string{"input", "home", "tmp", "roles", "collections"} {
		if err := root.Mkdir(path.Join(tmp, d), 0o700); err != nil {
			return fmt.Errorf("prepare the install: %w", err)
		}
	}
	write := func(name string, data []byte) error {
		if err := root.WriteFile(path.Join(tmp, "input", name), data, 0o600); err != nil {
			return fmt.Errorf("prepare the install: %w", err)
		}
		return nil
	}
	for _, f := range files {
		if err := write(strconv.Itoa(f.n)+".yml", f.data); err != nil {
			return err
		}
	}
	return write("galaxy.cfg", []byte("[defaults]\n"))
}

// removePublished removes the worker-owned copy pub after err.
func removePublished(root *os.Root, pub string, err error) error {
	if rerr := root.RemoveAll(pub); rerr != nil {
		err = errors.Join(err, fmt.Errorf("remove %s: %w", pub, rerr))
	}
	return err
}

// commandError is a failed galaxy command as Ensure reports it (Ruling 10):
// the step ended → its cause ("timed out after …" for the deadline, the
// caller's error when the caller's context ended); an exit → the code and
// the end of stderr, credentials removed.
func commandError(step context.Context, kind Kind, name string, err error) error {
	cmd := fmt.Sprintf("ansible-galaxy %s install -r %s", kind, name)
	var exit *ExitError
	switch {
	case step.Err() != nil:
		return fmt.Errorf("%s: %w", cmd, context.Cause(step))
	case errors.As(err, &exit):
		tail := stderrTail(exit.Stderr)
		redacted := &ExitError{Code: exit.Code, Stderr: tail}
		if tail == "" {
			return fmt.Errorf("%s: %w", cmd, redacted)
		}
		return fmt.Errorf("%s: %w: %s", cmd, redacted, tail)
	}
	return fmt.Errorf("%s: %w", cmd, err)
}
