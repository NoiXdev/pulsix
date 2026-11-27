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

// Watch defaults (backlog 2.54, plan ruling R5).
const (
	defaultRescanEvery  = 30 * time.Second
	defaultRetryDelay   = 5 * time.Second
	defaultQueryTimeout = 10 * time.Second
)

// RevocationFeed opens the session revocations arrive on.
type RevocationFeed interface {
	Listen(ctx context.Context) (RevocationSession, error)
}

// RevocationSession is one subscription to RevocationChannel.
type RevocationSession interface {
	// Next waits up to wait for a notification and returns the worker id
	// it names; uuid.Nil means nothing usable arrived (the wait ended, or
	// the payload was not a worker id), which calls for a full rescan. An
	// error means the session is lost.
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
	// RescanEvery bounds a wait for a notification; a quiet wait ends
	// with a rescan of every worker with open streams.
	RescanEvery time.Duration
	// RetryDelay is the pause before listening again after a failure.
	RetryDelay time.Duration
	// QueryTimeout bounds one NotActive query.
	QueryTimeout time.Duration
	Log          *slog.Logger
}

// RevocationWatcher ends, on this manager, the open streams of a worker
// revoked on any manager (backlog 2.54). The manager that serves a
// revocation closes its own streams in Service.Revoke; every other one
// learns of it from the revocation's notification. A notification is only
// a hint: the watcher closes a worker's streams once the database reports
// the worker revoked or gone, which is what the interceptor refuses too.
// On every (re)connect and after every quiet RescanEvery it rescans every
// worker with open streams, so a notification missed while the session was
// down is not lost and a dead session is noticed.
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
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = defaultQueryTimeout
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &RevocationWatcher{feed: feed, streams: streams, cfg: cfg}
}

// Run listens until ctx ends, reconnecting after every failure.
func (w *RevocationWatcher) Run(ctx context.Context) {
	for ctx.Err() == nil {
		sess, err := w.feed.Listen(ctx)
		if err != nil {
			if ctx.Err() == nil {
				w.cfg.Log.WarnContext(ctx, "listen for worker revocations; retrying", "err", err, "retry_in", w.cfg.RetryDelay)
			}
			w.pause(ctx)
			continue
		}
		err = w.watch(ctx, sess)
		sess.Close()
		if ctx.Err() != nil {
			return
		}
		w.cfg.Log.WarnContext(ctx, "worker revocation session lost; reconnecting", "err", err, "retry_in", w.cfg.RetryDelay)
		w.pause(ctx)
	}
}

// pause waits RetryDelay or until ctx ends.
func (w *RevocationWatcher) pause(ctx context.Context) {
	t := time.NewTimer(w.cfg.RetryDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// watch serves one session until it fails: a rescan first, then one cut
// per notification and a rescan after every quiet wait.
func (w *RevocationWatcher) watch(ctx context.Context, sess RevocationSession) error {
	if err := w.cut(ctx, sess, w.streams.Workers()); err != nil {
		return err
	}
	for {
		id, err := sess.Next(ctx, w.cfg.RescanEvery)
		if err != nil {
			return fmt.Errorf("wait for a worker revocation: %w", err)
		}
		candidates := w.streams.Workers()
		if id != uuid.Nil {
			if !slices.Contains(candidates, id) {
				continue // no stream of that worker here
			}
			candidates = []uuid.UUID{id}
		}
		if err := w.cut(ctx, sess, candidates); err != nil {
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
