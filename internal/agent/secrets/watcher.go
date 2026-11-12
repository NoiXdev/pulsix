package secrets

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"
)

// State is the configuration file's state as the agent reports it:
// StateAbsent, StateOK, or the error that made the agent drop every secret.
// An error state never quotes the file's content.
type State string

// The two states that are not errors.
const (
	StateAbsent State = "absent"
	StateOK     State = "ok"
)

// checkInterval is how often, at most, the watcher looks at the file.
const checkInterval = time.Minute

// Watcher holds the secrets of one configuration file and re-reads it when
// it changes. It looks at the file on the first Current and then at most
// once per checkInterval, on the Current that finds the interval over: no
// goroutine of its own. It is safe for concurrent use.
type Watcher struct {
	path  string
	check Check
	now   func() time.Time
	stat  func(string) (fs.FileInfo, error)
	log   *slog.Logger

	mu      sync.Mutex
	looked  bool
	lastAt  time.Time
	last    fingerprint
	set     Set
	state   State
	healthy bool // the last look loaded the file or found it absent
}

// fingerprint is what tells the watcher the file changed.
type fingerprint struct {
	exists bool
	size   int64
	mtime  time.Time
	mode   fs.FileMode
}

// Option configures a Watcher.
type Option func(*Watcher)

// WithClock replaces the wall clock.
func WithClock(now func() time.Time) Option { return func(w *Watcher) { w.now = now } }

// WithStat replaces os.Lstat, which tells the watcher whether the file
// changed.
func WithStat(stat func(string) (fs.FileInfo, error)) Option {
	return func(w *Watcher) { w.stat = stat }
}

// WithLogger logs every change of the loaded names or the state.
func WithLogger(log *slog.Logger) Option { return func(w *Watcher) { w.log = log } }

// NewWatcher watches path; check is the file protection rule Load applies.
func NewWatcher(path string, check Check, opts ...Option) *Watcher {
	w := &Watcher{
		path:  path,
		check: check,
		now:   time.Now,
		stat:  os.Lstat,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Path is the watched file.
func (w *Watcher) Path() string { return w.path }

// Current returns the secrets and the file's state, looking at the file
// first when it has not done so for checkInterval. The file is re-read
// when its size, modification time or mode changed, when it appeared or
// disappeared, and after a failed read; otherwise only its protection is
// checked again (an access-list change leaves size and time alone). Any
// failure leaves no secrets at all, never the last good ones.
func (w *Watcher) Current() (Set, State) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if !w.looked || now.Sub(w.lastAt) >= checkInterval {
		w.look()
		w.looked, w.lastAt = true, now
	}
	return w.set, w.state
}

// look runs one check of the file; w.mu is held.
func (w *Watcher) look() {
	fp := w.fingerprint()
	if w.looked && w.healthy && fp == w.last {
		if !fp.exists {
			return
		}
		if err := w.check(w.path); err != nil {
			w.update(fp, Set{}, State(err.Error()), false)
		}
		return
	}
	set, err := Load(w.path, w.check)
	switch {
	case err == nil:
		w.update(fp, set, StateOK, true)
	case errors.Is(err, ErrAbsent):
		w.update(fp, Set{}, StateAbsent, true)
	default:
		w.update(fp, Set{}, State(err.Error()), false)
	}
}

func (w *Watcher) fingerprint() fingerprint {
	fi, err := w.stat(w.path)
	if err != nil {
		// Missing or unreadable: Load tells which.
		return fingerprint{}
	}
	return fingerprint{exists: true, size: fi.Size(), mtime: fi.ModTime(), mode: fi.Mode()}
}

// update stores a look's outcome and logs it when the names or the state
// changed.
func (w *Watcher) update(fp fingerprint, set Set, state State, healthy bool) {
	changed := !w.looked || state != w.state || !equalNames(set.Names(), w.set.Names())
	w.last, w.set, w.state, w.healthy = fp, set, state, healthy
	if !changed {
		return
	}
	switch state {
	case StateOK:
		w.log.Info("loaded the secrets file", "path", w.path, "names", set)
	case StateAbsent:
		w.log.Info("no secrets file", "path", w.path)
	default:
		w.log.Warn("refused the secrets file; no secrets are available", "path", w.path, "state", string(state))
	}
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
