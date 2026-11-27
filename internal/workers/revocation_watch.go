package workers

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
)

// RevocationChannel is the database notification channel a revocation
// announces the worker's id on (backlog 2.54). The NotifyWorkerRevoked
// query names it too.
const RevocationChannel = "pulsix_worker_revoked"

// Watch defaults (backlog 2.54, plan rulings R5 and R10).
const (
	defaultRescanEvery   = 30 * time.Second
	defaultRetryDelay    = 5 * time.Second
	defaultMaxRetryDelay = time.Minute
	defaultQueryTimeout  = 10 * time.Second
)

// RevocationFeed opens the session revocations arrive on.
type RevocationFeed interface {
	Listen(ctx context.Context) (RevocationSession, error)
}

// RevocationSession is one subscription to RevocationChannel.
type RevocationSession interface {
	// Next waits up to wait for a notification and returns the worker id
	// it names; uuid.Nil means the wait ended without one. A payload that
	// is not a worker id is skipped. An error means the session is lost.
	Next(ctx context.Context, wait time.Duration) (uuid.UUID, error)
	// NotActive returns those of ids that are revoked or no longer exist.
	// It runs on the session itself, so it also proves the session alive.
	NotActive(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error)
	Close()
}

// OpenStreams is this manager's stream registry (grpcsrv.StreamRegistry):
// it closes a worker's streams and lists the workers that have any.
type OpenStreams interface {
	StreamCloser
	Workers() []uuid.UUID
}

// WatchConfig tunes a RevocationWatcher; zero fields take the defaults.
type WatchConfig struct {
	// RescanEvery is the period of the rescan of every worker with open
	// streams. It runs on a fixed deadline: notifications do not
	// postpone it.
	RescanEvery time.Duration
	// RetryDelay is the pause before listening again after a first
	// failure; each further failure in a row doubles it, up to
	// MaxRetryDelay.
	RetryDelay    time.Duration
	MaxRetryDelay time.Duration
	// QueryTimeout bounds one NotActive query.
	QueryTimeout time.Duration
	Log          *slog.Logger
	// Now and After are the clock and the retry timer (injected by tests).
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
}

// RevocationWatcher ends, on this manager, the open streams of a worker
// revoked on any manager (backlog 2.54). The manager that serves a
// revocation closes its own streams in Service.Revoke; every other one
// learns of it from the revocation's notification. A notification is only
// a hint: the watcher closes a worker's streams once the database reports
// the worker revoked or gone, which is what the interceptor refuses too.
// On every (re)connect and every RescanEvery it rescans every worker with
// open streams, so a notification missed while the session was down (or
// never sent, by an older manager) is not lost and a dead session is
// noticed. With no open streams the rescan asks nothing, so a session that
// died silently meanwhile is noticed at the first rescan with streams: the
// rescan on reconnect still catches what it missed (plan ruling R11).
type RevocationWatcher struct {
	feed    RevocationFeed
	streams OpenStreams
	cfg     WatchConfig
}

// NewRevocationWatcher builds the watcher; Run drives it.
func NewRevocationWatcher(feed RevocationFeed, streams OpenStreams, cfg WatchConfig) *RevocationWatcher {
	if cfg.RescanEvery <= 0 {
		cfg.RescanEvery = defaultRescanEvery
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = defaultRetryDelay
	}
	if cfg.MaxRetryDelay < cfg.RetryDelay {
		cfg.MaxRetryDelay = max(defaultMaxRetryDelay, cfg.RetryDelay)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = defaultQueryTimeout
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &RevocationWatcher{feed: feed, streams: streams, cfg: cfg}
}

// Run listens until ctx ends, reconnecting after every failure with a
// growing pause. A run of failures logs one warning and, once a session
// works again, one recovery; the failures in between log at debug level.
func (w *RevocationWatcher) Run(ctx context.Context) {
	delay, failing := w.cfg.RetryDelay, false
	fail := func(msg string, err error) {
		level := slog.LevelWarn
		if failing {
			level = slog.LevelDebug
		}
		w.cfg.Log.Log(ctx, level, msg, "err", err, "retry_in", delay)
		failing = true
		w.pause(ctx, delay)
		delay = min(2*delay, w.cfg.MaxRetryDelay)
	}
	healthy := func() {
		if failing {
			w.cfg.Log.InfoContext(ctx, "listening for worker revocations again")
		}
		delay, failing = w.cfg.RetryDelay, false
	}
	for ctx.Err() == nil {
		sess, err := w.feed.Listen(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fail("listen for worker revocations; retrying", err)
			continue
		}
		err = w.watch(ctx, sess, healthy)
		sess.Close()
		if ctx.Err() != nil {
			return
		}
		fail("worker revocation session lost; reconnecting", err)
	}
}

// pause waits d or until ctx ends.
func (w *RevocationWatcher) pause(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-w.cfg.After(d):
	}
}

// watch serves one session until it fails: a rescan first (after which
// the session counts as healthy), then one cut per notification, and a
// rescan whenever the fixed RescanEvery deadline has passed — however many
// notifications arrived meanwhile.
func (w *RevocationWatcher) watch(ctx context.Context, sess RevocationSession, healthy func()) error {
	if err := w.cut(ctx, sess, w.streams.Workers()); err != nil {
		return err
	}
	healthy()
	deadline := w.cfg.Now().Add(w.cfg.RescanEvery)
	for {
		if wait := deadline.Sub(w.cfg.Now()); wait <= 0 {
			if err := w.cut(ctx, sess, w.streams.Workers()); err != nil {
				return err
			}
			deadline = w.cfg.Now().Add(w.cfg.RescanEvery)
		}
		id, err := sess.Next(ctx, max(deadline.Sub(w.cfg.Now()), time.Millisecond))
		if err != nil {
			return fmt.Errorf("wait for a worker revocation: %w", err)
		}
		if id == uuid.Nil || !slices.Contains(w.streams.Workers(), id) {
			continue // the wait ended, or no stream of that worker here
		}
		if err := w.cut(ctx, sess, []uuid.UUID{id}); err != nil {
			return err
		}
	}
}

// cut closes the streams of those of ids the database reports revoked or
// gone. No ids asks nothing.
func (w *RevocationWatcher) cut(ctx context.Context, sess RevocationSession, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	qctx, cancel := context.WithTimeout(ctx, w.cfg.QueryTimeout)
	defer cancel()
	gone, err := sess.NotActive(qctx, ids)
	if err != nil {
		return fmt.Errorf("check the workers with open streams: %w", err)
	}
	for _, id := range gone {
		if n := w.streams.CloseWorker(id); n > 0 {
			w.cfg.Log.InfoContext(ctx, "closed a revoked worker's event streams", "worker_id", id, "streams", n)
		}
	}
	return nil
}
