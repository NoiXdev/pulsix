package workers_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/workers"
)

// waitBound is how long a test waits for the watcher goroutine to act.
const waitBound = 5 * time.Second

// fakeOpenStreams is a manager's stream registry: workers with open
// streams, and every CloseWorker call reported on closed.
type fakeOpenStreams struct {
	mu     sync.Mutex
	open   map[uuid.UUID]int
	closed chan uuid.UUID
}

func newFakeOpenStreams(ids ...uuid.UUID) *fakeOpenStreams {
	f := &fakeOpenStreams{open: map[uuid.UUID]int{}, closed: make(chan uuid.UUID, 16)}
	for _, id := range ids {
		f.open[id]++
	}
	return f
}

func (f *fakeOpenStreams) CloseWorker(id uuid.UUID) int {
	f.mu.Lock()
	n := f.open[id]
	delete(f.open, id)
	f.mu.Unlock()
	f.closed <- id
	return n
}

func (f *fakeOpenStreams) Workers() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uuid.UUID, 0, len(f.open))
	for id := range f.open {
		out = append(out, id)
	}
	return out
}

func (f *fakeOpenStreams) isOpen(id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open[id] > 0
}

// nextResult is one answer of a fake session's Next.
type nextResult struct {
	id  uuid.UUID
	err error
}

// fakeSession answers Next from next, NotActive from notActive (each call
// reported on queries) and reports Close on closed.
type fakeSession struct {
	next    chan nextResult
	queries chan []uuid.UUID
	closed  chan struct{}

	mu        sync.Mutex
	notActive map[uuid.UUID]bool
	queryErr  error
	once      sync.Once
}

func newFakeSession(notActive ...uuid.UUID) *fakeSession {
	s := &fakeSession{next: make(chan nextResult), queries: make(chan []uuid.UUID, 16), closed: make(chan struct{}), notActive: map[uuid.UUID]bool{}}
	for _, id := range notActive {
		s.notActive[id] = true
	}
	return s
}

func (s *fakeSession) Next(ctx context.Context, _ time.Duration) (uuid.UUID, error) {
	select {
	case r := <-s.next:
		return r.id, r.err
	case <-ctx.Done():
		return uuid.Nil, ctx.Err()
	}
}

func (s *fakeSession) NotActive(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	s.queries <- slices.Clone(ids)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	var out []uuid.UUID
	for _, id := range ids {
		if s.notActive[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *fakeSession) Close() { s.once.Do(func() { close(s.closed) }) }

// fakeFeed fails the first len(errs) Listen calls, then hands out the
// sessions in order.
type fakeFeed struct {
	mu       sync.Mutex
	errs     []error
	sessions chan *fakeSession
	listens  int
}

func newFakeFeed(errs ...error) *fakeFeed {
	return &fakeFeed{errs: errs, sessions: make(chan *fakeSession, 4)}
}

func (f *fakeFeed) Listen(ctx context.Context) (workers.RevocationSession, error) {
	f.mu.Lock()
	f.listens++
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		f.mu.Unlock()
		return nil, err
	}
	f.mu.Unlock()
	select {
	case s := <-f.sessions:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runWatcher starts the watcher and stops it when the test ends.
func runWatcher(t *testing.T, feed workers.RevocationFeed, streams workers.OpenStreams) {
	t.Helper()
	w := workers.NewRevocationWatcher(feed, streams, workers.WatchConfig{RetryDelay: time.Millisecond})
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func compareIDs(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }

func wantQuery(t *testing.T, s *fakeSession, want ...uuid.UUID) {
	t.Helper()
	select {
	case got := <-s.queries:
		slices.SortFunc(got, compareIDs)
		slices.SortFunc(want, compareIDs)
		if !slices.Equal(got, want) {
			t.Fatalf("NotActive(%v), want %v", got, want)
		}
	case <-time.After(waitBound):
		t.Fatalf("no NotActive query, want one for %v", want)
	}
}

func wantClosed(t *testing.T, streams *fakeOpenStreams, want uuid.UUID) {
	t.Helper()
	select {
	case got := <-streams.closed:
		if got != want {
			t.Fatalf("CloseWorker(%s), want %s", got, want)
		}
	case <-time.After(waitBound):
		t.Fatalf("CloseWorker(%s) never came", want)
	}
}

func wantNoClose(t *testing.T, streams *fakeOpenStreams) {
	t.Helper()
	select {
	case got := <-streams.closed:
		t.Fatalf("unexpected CloseWorker(%s)", got)
	default:
	}
}

func send(t *testing.T, s *fakeSession, r nextResult) {
	t.Helper()
	select {
	case s.next <- r:
	case <-time.After(waitBound):
		t.Fatal("the watcher never waited for a notification")
	}
}

// TestRevocationWatcherVerifiesANotification: a notification names one
// worker; the watcher asks about that worker alone (not every worker with
// open streams, even one the database would report) and closes it once the
// database confirms. A notification for a worker without streams here asks
// nothing.
func TestRevocationWatcherVerifiesANotification(t *testing.T) {
	revoked, alsoInactive, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	streams := newFakeOpenStreams(revoked, alsoInactive)
	sess := newFakeSession()
	feed := newFakeFeed()
	feed.sessions <- sess
	runWatcher(t, feed, streams)

	wantQuery(t, sess, revoked, alsoInactive) // the rescan on connect: nothing inactive yet
	sess.mu.Lock()
	sess.notActive[revoked], sess.notActive[alsoInactive] = true, true
	sess.mu.Unlock()

	send(t, sess, nextResult{id: other})
	send(t, sess, nextResult{id: revoked})
	wantQuery(t, sess, revoked)
	wantClosed(t, streams, revoked)
	if !streams.isOpen(alsoInactive) {
		t.Fatal("a worker the notification did not name was closed")
	}
}

// TestRevocationWatcherRescansWhenQuiet: a wait that ends without a usable
// notification (quiet, or a payload that is not a worker id) rescans every
// worker with open streams; only the inactive one is closed.
func TestRevocationWatcherRescansWhenQuiet(t *testing.T) {
	revoked, active := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	streams := newFakeOpenStreams(revoked, active)
	sess := newFakeSession()
	feed := newFakeFeed()
	feed.sessions <- sess
	runWatcher(t, feed, streams)

	wantQuery(t, sess, revoked, active)
	sess.mu.Lock()
	sess.notActive[revoked] = true
	sess.mu.Unlock()
	send(t, sess, nextResult{id: uuid.Nil})
	wantQuery(t, sess, revoked, active)
	wantClosed(t, streams, revoked)
	wantNoClose(t, streams)
	if !streams.isOpen(active) {
		t.Fatal("an active worker's streams were closed")
	}
}

// TestRevocationWatcherReconnectsAndRescans: a session that fails (a lost
// connection while waiting, or a failed rescan) is closed and replaced; the
// rescan on the new session catches the revocation made meanwhile.
func TestRevocationWatcherReconnectsAndRescans(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(t *testing.T, s *fakeSession)
	}{
		{"wait fails", func(t *testing.T, s *fakeSession) { send(t, s, nextResult{err: errors.New("conn closed")}) }},
		{"rescan fails", func(t *testing.T, s *fakeSession) {
			s.mu.Lock()
			s.queryErr = errors.New("timeout")
			s.mu.Unlock()
			send(t, s, nextResult{id: uuid.Nil})
			<-s.queries
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			revoked := uuid.Must(uuid.NewV7())
			streams := newFakeOpenStreams(revoked)
			first, second := newFakeSession(), newFakeSession(revoked)
			feed := newFakeFeed()
			feed.sessions <- first
			runWatcher(t, feed, streams)

			wantQuery(t, first, revoked)
			tc.fail(t, first)
			select {
			case <-first.closed:
			case <-time.After(waitBound):
				t.Fatal("the failed session was not closed")
			}
			feed.sessions <- second
			wantQuery(t, second, revoked)
			wantClosed(t, streams, revoked)
		})
	}
}

// TestRevocationWatcherRetriesAFailedListen: a feed that cannot listen is
// tried again after the retry delay; the session it then gets rescans.
func TestRevocationWatcherRetriesAFailedListen(t *testing.T) {
	revoked := uuid.Must(uuid.NewV7())
	streams := newFakeOpenStreams(revoked)
	sess := newFakeSession(revoked)
	feed := newFakeFeed(errors.New("connection refused"), errors.New("connection refused"))
	feed.sessions <- sess
	runWatcher(t, feed, streams)

	wantQuery(t, sess, revoked)
	wantClosed(t, streams, revoked)
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if feed.listens != 3 {
		t.Fatalf("Listen called %d times, want 3", feed.listens)
	}
}

// TestRevocationWatcherWithoutStreamsAsksNothing: with no open streams the
// rescan has nothing to ask about.
func TestRevocationWatcherWithoutStreamsAsksNothing(t *testing.T) {
	streams := newFakeOpenStreams()
	sess := newFakeSession()
	feed := newFakeFeed()
	feed.sessions <- sess
	runWatcher(t, feed, streams)

	send(t, sess, nextResult{id: uuid.Nil})
	send(t, sess, nextResult{id: uuid.Must(uuid.NewV7())})
	send(t, sess, nextResult{id: uuid.Nil}) // the loop came round again: nothing was asked
	select {
	case q := <-sess.queries:
		t.Fatalf("NotActive(%v) without open streams", q)
	default:
	}
}

// TestRevocationWatcherStopsWithItsContext: cancelling the context ends Run
// and closes the session.
func TestRevocationWatcherStopsWithItsContext(t *testing.T) {
	sess := newFakeSession()
	feed := newFakeFeed()
	feed.sessions <- sess
	w := workers.NewRevocationWatcher(feed, newFakeOpenStreams(), workers.WatchConfig{})
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(runCtx) }()
	send(t, sess, nextResult{id: uuid.Nil})
	cancel()
	select {
	case <-done:
	case <-time.After(waitBound):
		t.Fatal("Run did not return after cancel")
	}
	select {
	case <-sess.closed:
	default:
		t.Fatal("the session was not closed")
	}
}
