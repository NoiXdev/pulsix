package workers_test

import (
	"context"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/workers"
)

// quietWait is how long a test waits to be sure no notification comes.
const quietWait = 300 * time.Millisecond

// registerWorker stores an active worker named name in org's zone.
func registerWorker(t *testing.T, svc *workers.Service, org, zone uuid.UUID, name string, serial int64) workers.Worker {
	t.Helper()
	w, err := svc.Register(ctx, org, workers.RegisterInput{ZoneID: zone, Name: name, CertSerial: big.NewInt(serial)})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func listen(t *testing.T, feed workers.RevocationFeed) workers.RevocationSession {
	t.Helper()
	sess, err := feed.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	return sess
}

func wantNext(t *testing.T, sess workers.RevocationSession, wait time.Duration, want uuid.UUID) {
	t.Helper()
	got, err := sess.Next(ctx, wait)
	if err != nil || got != want {
		t.Fatalf("Next = %s, %v; want %s", got, err, want)
	}
}

// TestRevocationNotifiesOnCommitOnly: the notification query is delivered
// when its transaction commits, never before and never for a rollback; a
// revocation through the store announces the worker, a failed one (unknown
// worker) announces nothing; a payload that is not a worker id reads as a
// rescan.
func TestRevocationNotifiesOnCommitOnly(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	svc := workers.NewService(workers.NewSQLStore(d))
	org := seedOrg(t, q, "revocation-notify")
	zone := seedZone(t, q, org, "default")
	sess := listen(t, workers.NewSQLRevocationFeed(d.Pool()))

	committed, rolledBack := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, tc := range []struct {
		id     uuid.UUID
		finish func(pgx.Tx) error
	}{{rolledBack, func(tx pgx.Tx) error { return tx.Rollback(ctx) }}, {committed, func(tx pgx.Tx) error { return tx.Commit(ctx) }}} {
		tx, err := d.Pool().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := gen.New(tx).NotifyWorkerRevoked(ctx, tc.id); err != nil {
			t.Fatal(err)
		}
		wantNext(t, sess, quietWait, uuid.Nil) // not before the commit
		if err := tc.finish(tx); err != nil {
			t.Fatal(err)
		}
	}
	wantNext(t, sess, waitBound, committed) // the rolled-back one never came

	w := registerWorker(t, svc, org, zone, "notify-1", 1001)
	if _, _, err := svc.Revoke(ctx, org, uuid.Must(uuid.NewV7())); err == nil {
		t.Fatal("revoking an unknown worker succeeded")
	}
	if _, _, err := svc.Revoke(ctx, org, w.ID); err != nil {
		t.Fatal(err)
	}
	wantNext(t, sess, waitBound, w.ID)

	if _, err := d.Pool().Exec(ctx, `SELECT pg_notify($1, 'not-a-worker')`, workers.RevocationChannel); err != nil {
		t.Fatal(err)
	}
	wantNext(t, sess, waitBound, uuid.Nil)
	wantNext(t, sess, quietWait, uuid.Nil) // nothing left: the wait just ends
}

// TestRevocationSessionNotActive: of the asked ids, the revoked and the
// unknown ones are reported, an active one is not — outside any org scope.
func TestRevocationSessionNotActive(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	svc := workers.NewService(workers.NewSQLStore(d))
	org := seedOrg(t, q, "revocation-not-active")
	zone := seedZone(t, q, org, "default")
	active := registerWorker(t, svc, org, zone, "active-1", 2001)
	revoked := registerWorker(t, svc, org, zone, "revoked-1", 2002)
	if _, _, err := svc.Revoke(ctx, org, revoked.ID); err != nil {
		t.Fatal(err)
	}
	unknown := uuid.Must(uuid.NewV7())
	sess := listen(t, workers.NewSQLRevocationFeed(d.Pool()))
	got, err := sess.NotActive(ctx, []uuid.UUID{active.ID, revoked.ID, unknown})
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got, compareIDs)
	want := []uuid.UUID{revoked.ID, unknown}
	slices.SortFunc(want, compareIDs)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("NotActive = %v, want %v", got, want)
	}
}

// managerB is a second manager on the same database: its own pool (named
// so a test can find its sessions), its stream registry and its watcher.
type managerB struct {
	streams *fakeOpenStreams
	feed    *signalFeed
}

// signalFeed reports every Listen call on calls and, while gate is set,
// holds it until the gate opens; ready reports each session it handed out,
// waiting each wait for a notification.
type signalFeed struct {
	inner   workers.RevocationFeed
	calls   chan struct{}
	ready   chan struct{}
	waiting chan struct{}
	gate    chan chan struct{}
}

func (f *signalFeed) Listen(ctx context.Context) (workers.RevocationSession, error) {
	f.calls <- struct{}{}
	select {
	case g := <-f.gate:
		select {
		case <-g:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	default:
	}
	s, err := f.inner.Listen(ctx)
	if err != nil {
		return nil, err
	}
	f.ready <- struct{}{}
	return signalSession{RevocationSession: s, waiting: f.waiting}, nil
}

// signalSession reports every Next call on waiting: the watcher has
// finished its rescan and listens.
type signalSession struct {
	workers.RevocationSession
	waiting chan struct{}
}

func (s signalSession) Next(ctx context.Context, wait time.Duration) (uuid.UUID, error) {
	select {
	case s.waiting <- struct{}{}:
	default:
	}
	return s.RevocationSession.Next(ctx, wait)
}

const managerBName = "pulsix-manager-b"

func startManagerB(t *testing.T, streams *fakeOpenStreams) *managerB {
	t.Helper()
	url, err := db.WithApplicationName(testdb.URL(t), managerBName)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.OpenAsRole(ctx, url, "pulsix_app")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	feed := &signalFeed{inner: workers.NewSQLRevocationFeed(pool.Pool()), calls: make(chan struct{}, 16), ready: make(chan struct{}, 16), waiting: make(chan struct{}, 1), gate: make(chan chan struct{}, 1)}
	runWatcher(t, feed, streams)
	waitFor(t, feed.calls, "B listens")
	waitFor(t, feed.ready, "B's session")
	waitFor(t, feed.waiting, "B's rescan on connect to finish")
	return &managerB{streams: streams, feed: feed}
}

func waitFor(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(waitBound):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestRevocationReachesAnotherManager: a revocation served by manager A
// closes the worker's streams on A (synchronously) and on B (through the
// notification); another worker's streams on B are untouched.
func TestRevocationReachesAnotherManager(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	svcA := workers.NewService(workers.NewSQLStore(d))
	org := seedOrg(t, q, "revocation-two-managers")
	zone := seedZone(t, q, org, "default")
	revoked := registerWorker(t, svcA, org, zone, "revoked-1", 3001)
	kept := registerWorker(t, svcA, org, zone, "kept-1", 3002)
	streamsA := newFakeOpenStreams(revoked.ID)
	svcA.SetRevokeHooks(streamsA, nil)
	b := startManagerB(t, newFakeOpenStreams(revoked.ID, kept.ID))

	if _, res, err := svcA.Revoke(ctx, org, revoked.ID); err != nil || res.Streams != 1 {
		t.Fatalf("Revoke on A = %+v, %v; want A's one stream closed", res, err)
	}
	wantClosed(t, streamsA, revoked.ID)
	wantClosed(t, b.streams, revoked.ID)
	time.Sleep(quietWait)
	wantNoClose(t, b.streams)
	if !b.streams.isOpen(kept.ID) {
		t.Fatal("the other worker's streams on B were closed")
	}
}

// TestRevocationWhileDisconnectedIsCaughtOnReconnect: B's session dies (its
// backend is terminated), the revocation commits while B is not listening,
// so its notification reaches nobody; B's rescan on reconnect closes the
// worker's streams anyway.
func TestRevocationWhileDisconnectedIsCaughtOnReconnect(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	svcA := workers.NewService(workers.NewSQLStore(d))
	org := seedOrg(t, q, "revocation-reconnect")
	zone := seedZone(t, q, org, "default")
	revoked := registerWorker(t, svcA, org, zone, "revoked-1", 4001)
	kept := registerWorker(t, svcA, org, zone, "kept-1", 4002)
	b := startManagerB(t, newFakeOpenStreams(revoked.ID, kept.ID))

	gate := make(chan struct{})
	b.feed.gate <- gate
	admin, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	var killed int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM (SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1) k`, managerBName).Scan(&killed); err != nil || killed == 0 {
		t.Fatalf("terminate B's sessions = %d, %v", killed, err)
	}
	waitFor(t, b.feed.calls, "B to notice the lost session and listen again")

	if _, _, err := svcA.Revoke(ctx, org, revoked.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(quietWait) // the notification is gone by now
	close(gate)
	waitFor(t, b.feed.ready, "B's new session")
	wantClosed(t, b.streams, revoked.ID)
	if !b.streams.isOpen(kept.ID) {
		t.Fatal("the other worker's streams on B were closed")
	}
}
