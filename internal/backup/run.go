package backup

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// run is an attempt in progress.
type run struct {
	id        uuid.UUID
	kind      string
	startedAt time.Time
	name      string
	actor     *uuid.UUID
}

// record marks stale attempts abandoned and records a new running one. The
// caller holds the lock. For a scheduled slot another manager took first,
// inserted is false.
func (s *Service) record(ctx context.Context, kind string, slot *time.Time, actor *uuid.UUID) (r run, inserted bool, err error) {
	if err := s.abandon(ctx); err != nil {
		return run{}, false, err
	}
	// Object names carry whole seconds; the header gets the same instant.
	started := s.now().UTC().Truncate(time.Second)
	r = run{kind: kind, startedAt: started, name: ObjectName(started, kind), actor: actor}
	r.id, inserted, err = s.store.InsertAttempt(ctx, NewAttempt{Kind: kind, Slot: slot, StartedAt: started, ObjectName: r.name, Actor: actor})
	if err != nil {
		return run{}, false, fmt.Errorf("record backup attempt: %w", err)
	}
	return r, inserted, nil
}

// execute dumps r into st and records how it ended.
func (s *Service) execute(ctx context.Context, r run, st Storage, includeMetrics bool) (Entry, error) {
	s.cleanPartials(ctx, st)
	size, err := s.transfer(ctx, r, st, includeMetrics)
	return s.finish(ctx, r, size, err)
}

// cleanPartials removes the half-written files an earlier, stopped backup left
// in st, when st can have any. Every backup holds the instance-wide lock by
// the time it gets here, so none of those files belongs to an upload in
// flight. A failure is logged and never fails the backup.
func (s *Service) cleanPartials(ctx context.Context, st Storage) {
	c, ok := st.(PartialCleaner)
	if !ok {
		return
	}
	n, err := c.CleanPartials(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "remove partial backup files", "destination", st.Describe(), "removed", n, "err", err)
	}
	if n > 0 {
		s.log.InfoContext(ctx, "removed partial backup files a stopped backup left behind", "destination", st.Describe(), "count", n)
	}
}

// transfer streams the dump into st through a pipe, so the backup is never
// held in memory or on a local disk first.
//
// A watchdog bounds the transfer by progress: every read the upload takes
// from the pipe re-arms it. When no byte has reached the destination for
// the stall timeout, it cancels the transfer's context and closes both
// ends of the pipe, so an upload waiting in Read and a dump waiting in
// Write return as well, and the attempt fails as stalled. The caller's ctx
// is left alone, so the attempt is recorded failed, not abandoned.
func (s *Service) transfer(ctx context.Context, r run, st Storage, includeMetrics bool) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	w := &pipeWriter{pw: pw}
	stalled := stalledError{after: s.stallTimeout}
	wd := startWatchdog(s.stallTimeout, func() {
		cancel()
		pw.CloseWithError(stalled)
		pr.CloseWithError(stalled)
	})
	defer wd.halt()
	dumped := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			// This goroutine is outside the ticker's recover: a panic here
			// would end the manager, so it fails the attempt instead.
			if p := recover(); p != nil {
				err = fmt.Errorf("dump panicked: %v", p)
				s.log.ErrorContext(ctx, "backup dump panicked", "panic", p, "stack", string(debug.Stack()))
			}
			pw.CloseWithError(err) // nil ends the upload's input cleanly
			dumped <- err
		}()
		_, err = s.dump(ctx, w, DumpOptions{
			Kind: r.kind, PulsixVersion: s.version, IncludeMetrics: includeMetrics,
			Now: func() time.Time { return r.startedAt },
		})
	}()
	size, putErr := s.put(ctx, st, r.name, pr, wd)
	dumpErr := <-dumped

	switch {
	case (dumpErr != nil || putErr != nil) && wd.fired.Load():
		// Whatever either side saw is a consequence of the cancellation.
		return 0, stalled
	case dumpErr != nil && !w.failed.Load():
		// The dump failed on its own; the upload only saw it end.
		return 0, dumpErr
	case putErr != nil:
		return 0, putErr
	case dumpErr != nil:
		return 0, dumpErr
	}
	return size, nil
}

// put uploads the pipe's output into st, reporting every read to wd as
// progress, and closes the pipe's reading end however the upload ends — a
// panic included — so the dump goroutine never blocks on a write nobody
// reads.
func (s *Service) put(ctx context.Context, st Storage, name string, pr *io.PipeReader, wd *watchdog) (int64, error) {
	defer pr.CloseWithError(errUploadEnded)
	return st.Put(ctx, name, progressReader{r: pr, wd: wd})
}

// failPanicked records an attempt whose backup panicked as failed and logs
// the panic. Recording it may panic as well (the store is what panicked);
// that is logged too, and the attempt stays running until the next backup
// marks it abandoned.
func (s *Service) failPanicked(ctx context.Context, r run, p any) {
	s.log.ErrorContext(ctx, "backup panicked", "kind", r.kind, "name", r.name, "panic", p, "stack", string(debug.Stack()))
	defer func() {
		if p2 := recover(); p2 != nil {
			s.log.ErrorContext(ctx, "record a panicked backup attempt", "name", r.name, "panic", p2)
		}
	}()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	f := Finish{At: s.now().UTC(), State: StateFailed, Error: fmt.Sprintf("the backup panicked: %v", p)}
	if err := s.store.FinishAttempt(fctx, r.id, f); err != nil {
		s.log.ErrorContext(ctx, "record a panicked backup attempt", "name", r.name, "err", err)
	}
}

// pipeWriter remembers whether a write failed, which only happens when the
// upload stopped reading: the dump's error is then a consequence.
type pipeWriter struct {
	pw     *io.PipeWriter
	failed atomic.Bool
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	n, err := w.pw.Write(p)
	if err != nil {
		w.failed.Store(true)
	}
	return n, err
}

// finish records how r ended: succeeded, failed with err, or abandoned
// when ctx ended under it (the manager is shutting down).
func (s *Service) finish(ctx context.Context, r run, size int64, err error) (Entry, error) {
	f := Finish{At: s.now().UTC(), State: StateSucceeded, Size: size}
	switch {
	case err != nil && ctx.Err() != nil:
		f = Finish{At: f.At, State: StateAbandoned, Error: reasonStopped}
	case err != nil:
		f = Finish{At: f.At, State: StateFailed, Error: err.Error()}
		s.log.ErrorContext(ctx, "backup failed", "kind", r.kind, "name", r.name, "err", err)
	default:
		s.log.InfoContext(ctx, "backup finished", "kind", r.kind, "name", r.name, "size", size)
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if ferr := s.store.FinishAttempt(fctx, r.id, f); ferr != nil {
		// The backup itself stands or falls on its own; the row is marked
		// abandoned by the next backup.
		s.log.ErrorContext(ctx, "record the end of a backup attempt", "name", r.name, "err", ferr)
	}
	e := Entry{
		Name: r.name, CreatedAt: r.startedAt, Kind: r.kind, Actor: r.actor, PulsixVersion: s.version,
		Size: f.Size, State: f.State, Error: f.Error,
	}
	if err != nil {
		return e, fmt.Errorf("backup %s: %w", r.name, err)
	}
	return e, nil
}
