package requirements

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"regexp"
	"time"
)

// fullCommit is a full SHA-1 or SHA-256 commit id, as git prints it.
var fullCommit = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// Kind is what one ansible-galaxy command installs.
type Kind string

// The two install kinds, as galaxy's subcommands name them.
const (
	KindRole       Kind = "role"
	KindCollection Kind = "collection"
)

// DefaultTimeout bounds the whole requirements step: the source check, the
// wait for the lock and every galaxy command (spec goal 5).
const DefaultTimeout = 10 * time.Minute

// Install is one ansible-galaxy command. The temp entry it fills (spec,
// owner decision 2026-11-06) is
// <cache>/.tmp-<x>/{input/<n>.yml, input/galaxy.cfg, home/, tmp/, roles/, collections/},
// all created 0700 (files 0600) by Ensure; the Galaxy implementation opens
// what the play uid needs.
type Install struct {
	Kind         Kind
	File         string // <Entry>/input/<n>.yml, n from 1 in Files order
	Dir          string // galaxy's working directory: the file's own directory in the clone
	Entry        string // the temp entry; -p is <Entry>/roles or <Entry>/collections
	CloneDir     string // opened read-only to the play group with RepoCacheDir as root
	RepoCacheDir string
}

// Galaxy runs ansible-galaxy.
type Galaxy interface {
	// Install runs one command and sends each line of its output to line.
	// A command that ran and failed returns an *ExitError.
	Install(ctx context.Context, in Install, line func(string)) error
	// RemoveAll removes dir and what the install wrote into it, also files
	// the play uid owns.
	RemoveAll(dir string) error
}

// ExitError is a galaxy command that exited with a non-zero code.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit %d", e.Code) }

// Clock tells the time: the duration line and the entries' last use.
type Clock interface{ Now() time.Time }

// SystemClock is the wall clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// Request is what Ensure installs: the counted files of a commit's clone.
type Request struct {
	CloneDir     string    // <RepoCacheDir>/<org>/<repo>
	Clone        fs.StatFS // the clone's confined root (worker.OpenCloneFS)
	Files        Files
	Commit       string // full SHA-1 or SHA-256
	RepoCacheDir string
	Line         func(string) // nil: discarded
}

// Entry is a published install: roles_path and collections_path name its
// two directories.
type Entry struct{ Dir, RolesDir, CollectionsDir string }

// IsZero reports that there is no entry: the commit has nothing to install.
func (e Entry) IsZero() bool { return e == Entry{} }

func newEntry(dir string) Entry {
	return Entry{Dir: dir, RolesDir: filepath.Join(dir, "roles"), CollectionsDir: filepath.Join(dir, "collections")}
}

// Service installs a commit's requirements once into the repository's
// cache and hands out the published entry.
type Service struct {
	galaxy  Galaxy
	clock   Clock
	timeout time.Duration
	log     *slog.Logger

	// Test seams (export_test.go): lockWaiting runs when an Ensure finds
	// the lock held; touch replaces the Chtimes that marks an entry used.
	lockWaiting func()
	touch       func(name string) error
}

// Option configures a Service.
type Option func(*Service)

// WithLogger sends the warnings of a cleanup that failed (a leftover, the
// legacy directory, an eviction) to logger instead of slog.Default(). They
// go to the worker's log, not to the run log: the operator acts on them.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Service) { s.log = logger }
}

// NewService returns a Service that runs galaxy. timeout bounds one Ensure;
// 0 means DefaultTimeout.
func NewService(galaxy Galaxy, clock Clock, timeout time.Duration, opts ...Option) *Service {
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	s := &Service{galaxy: galaxy, clock: clock, timeout: timeout}
	for _, opt := range opts {
		opt(s)
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// timedOut is the cause of a step that ran out of time: "timed out after
// <timeout>" in every error of the step that the deadline ended, and still
// context.DeadlineExceeded to errors.Is.
type timedOut struct{ after time.Duration }

func (e timedOut) Error() string        { return "timed out after " + e.after.String() }
func (e timedOut) Is(target error) bool { return target == context.DeadlineExceeded }
