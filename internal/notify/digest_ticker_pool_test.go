package notify_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/notify"
)

// blockingDigestBuilder is a DigestBuilder that genuinely needs a pool
// connection of its own — a real query against d, not an in-process fake —
// and, after signalling started, waits briefly before asking for it, so
// the concurrent writer in TestDigestTickerDoesNotStarvePoolConnections
// gets a head start on the pool's one spare connection and is the one
// blocked on the ticker's row lock by the time the build itself asks for a
// connection. That is the shape of the circular wait, not merely a lucky
// ordering. It mirrors schedules' own blockingRuns.
//
// Only the first call stalls; later ones return at once.
type blockingDigestBuilder struct {
	d       *db.DB
	started chan struct{}
	once    sync.Once
}

func (b *blockingDigestBuilder) Digest(ctx context.Context, _ uuid.UUID, _, _ time.Time) (notify.DigestSummary, error) {
	first := false
	b.once.Do(func() { close(b.started); first = true })
	query := "SELECT 1"
	if first {
		time.Sleep(150 * time.Millisecond)
		// Long past the batch deadline. What actually stalls this call
		// without the fix is never acquiring a connection at all, not
		// this query's own runtime; the length only shows that, once a
		// connection is acquired, the batch's own deadline ends it.
		query = "SELECT pg_sleep(60)"
	}
	if _, err := b.d.Pool().Exec(ctx, query); err != nil {
		return notify.DigestSummary{}, err
	}
	return notify.DigestSummary{Schedules: []notify.DigestSchedule{{Name: "Nightly", Runs: 1}}}, nil
}

// TestDigestTickerDoesNotStarvePoolConnections is the final review's
// Important #5. DigestTicker.Tick passed the loop context straight into
// ProcessDue, which opens a raw-pool transaction, takes FOR NO KEY UPDATE
// SKIP LOCKED on up to 20 digest rows and then — inside that transaction —
// calls the builder, which issues its own statements on *separate* pool
// connections. The schedule ticker was explicitly fixed for this shape
// (fix round 1, Important #3) and the digest ticker inherited only the
// SKIP LOCKED half, not the deadline half.
//
// The test builds a real DigestTicker with a short real tick over
// pool_max_conns=2 and calls Tick(context.Background()) directly, with no
// deadline of its own, so whatever bounds it is Tick's own internal logic.
// Concurrently, a digest settings write — through the real service and
// store — targets the very row the ticker has locked.
//
// Without the deadline this deadlocks: the writer grabs the pool's one
// spare connection and blocks on the ticker's row lock, so when the build
// asks for a connection there is none left; the ticker can never finish
// the row, so it never releases the lock, so the writer never gets it
// either — the transaction, the connection and the row locks are pinned
// until the process stops. With the fix the stalled build is cancelled by
// Tick's own deadline, the batch still commits and releases the lock, and
// the writer completes shortly after.
func TestDigestTickerDoesNotStarvePoolConnections(t *testing.T) {
	url := testdb.URL(t)
	d, err := db.OpenAsRole(context.Background(), url+"&pool_max_conns=2", "pulsix_app")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	org := seedOrg(t, d, "digest-pool")
	ch := seedChannel(t, d, org)
	scoped := db.WithOrgScope(context.Background(), org)
	store := notify.NewSQLStore(d)
	set := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC) // a Tuesday
	svc := notify.NewService(notify.Deps{Channels: store, Digests: store, Now: func() time.Time { return set }})
	stored, err := svc.SetDigest(scoped, org, notify.DigestInput{
		Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{ch.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	due := stored.NextRunAt.Add(30 * time.Second)

	builder := &blockingDigestBuilder{d: d, started: make(chan struct{})}
	outbox := notify.NewOutbox(d, "https://pulsix.example.test/", func() time.Time { return due }, rand.Reader, quiet())
	const tick = 500 * time.Millisecond
	tk := notify.NewDigestTicker(notify.NewDigestSQLStore(d, quiet()), builder, outbox, tick, func() time.Time { return due }, nil, quiet())

	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		// Tick's own bound is what must end this; the test supplies none.
		if _, err := tk.Tick(context.Background()); err != nil {
			t.Errorf("Tick: %v", err)
		}
	}()

	select {
	case <-builder.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the ticker never reached its builder — setup is broken, not the deadlock under test")
	}

	errs := make(chan error, 1)
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		wctx, cancel := context.WithTimeout(db.WithOrgScope(context.Background(), org), 40*time.Second)
		defer cancel()
		if _, err := svc.SetDigest(wctx, org, notify.DigestInput{
			Enabled: true, Weekday: "tue", Time: "09:00", Timezone: "UTC", Channels: []uuid.UUID{ch.ID},
		}); err != nil {
			errs <- fmt.Errorf("concurrent digest settings write: %w (pool exhaustion deadlock?)", err)
		}
	}()

	select {
	case <-writeDone:
	case <-time.After(45 * time.Second):
		t.Fatal("the concurrent digest settings write did not complete within 45s — pool exhaustion deadlock?")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	select {
	case <-tickDone:
	case <-time.After(20 * time.Second):
		t.Fatal("Tick never returned after its own deadline should have ended the batch")
	}
}
