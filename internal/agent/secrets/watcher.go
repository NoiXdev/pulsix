package secrets

import (
	"errors"
	"io"
	"log/slog"
	"slices"
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

// Watcher holds the secrets of one configuration file and reads it again
// on every look. It looks at the file on the first Current and then at most
// once per checkInterval, on the Current that finds the interval over: no
// goroutine of its own. It is safe for concurrent use.
//
// Every look is a full Load — protection check, read and parse — whether
// or not the file's size, time or mode changed. The file is at most 1 MiB
// and read at most once a minute, and no stat-based shortcut can see every
// change that matters (an access-list edit, a rewrite within one timestamp
// tick, a path the agent can no longer look at).
type Watcher struct {
	path  string
	check Check
	now   func() time.Time
	log   *slog.Logger

	mu     sync.Mutex
	looked bool
	lastAt time.Time
	set    Set
	state  State
}

// Option configures a Watcher.
type Option func(*Watcher)

// WithClock replaces the wall clock.
func WithClock(now func() time.Time) Option { return func(w *Watcher) { w.now = now } }

// WithLogger logs every change of the loaded names or the state.
func WithLogger(log *slog.Logger) Option { return func(w *Watcher) { w.log = log } }

// NewWatcher watches path; check is the file protection rule Load applies.
func NewWatcher(path string, check Check, opts ...Option) *Watcher {
	w := &Watcher{
		path:  path,
		check: check,
		now:   time.Now,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Path is the watched file.
func (w *Watcher) Path() string { return w.path }

// Current returns the secrets and the file's state, reading the file
// first when it has not done so for checkInterval. Only a file that does
// not exist is StateAbsent; any other failure, a path the agent cannot
// look at included, is an error state. Any failure leaves no secrets at
// all, never the last good ones.
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

// look reads the file once; w.mu is held.
func (w *Watcher) look() {
	set, err := Load(w.path, w.check)
	switch {
	case err == nil:
		w.update(set, StateOK)
	case errors.Is(err, ErrAbsent):
		w.update(Set{}, StateAbsent)
	default:
		w.update(Set{}, State(err.Error()))
	}
}

// update stores a look's outcome and logs it when the secret or command
// names or the state changed.
func (w *Watcher) update(set Set, state State) {
	changed := !w.looked || state != w.state ||
		!slices.Equal(set.Names(), w.set.Names()) ||
		!slices.Equal(set.CommandNames(), w.set.CommandNames())
	w.set, w.state = set, state
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
