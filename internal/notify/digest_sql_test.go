package notify_test

import (
	"context"
	"errors"
	"github.com/noixdev/pulsix/internal/audit"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/notify"
)

func TestDigestStoreTickAndOutbox(t *testing.T) {
	w := newOutboxWorld(t, "digest-flow")
	scoped := db.WithOrgScope(ctx, w.org)
	store := notify.NewSQLStore(w.d)
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	svc := notify.NewService(notify.Deps{Channels: store, Digests: store, Now: func() time.Time { return now }})
	set, err := svc.SetDigest(scoped, w.org, notify.DigestInput{Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{w.chA.ID, w.chDisabled.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Channels) != 2 || !set.NextRunAt.Equal(time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("stored = %+v", set)
	}
	if _, err := svc.SetDigest(scoped, w.org, notify.DigestInput{Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{uuid.Must(uuid.NewV7())}}); err == nil {
		t.Fatal("an unknown channel was accepted")
	}
	// Not due yet.
	due := now.AddDate(0, 0, 6) // Monday 28 Sep 08:00
	ticker := func(at time.Time, b notify.DigestBuilder) *notify.DigestTicker {
		return notify.NewDigestTicker(notify.NewDigestSQLStore(w.d, quiet(), nil), b, w.outbox, time.Minute, func() time.Time { return at }, nil, quiet())
	}
	week := notify.DigestSummary{Schedules: []notify.DigestSchedule{{Name: "Nightly", Runs: 2}}, Slowest: []notify.DigestRun{{Schedule: "Nightly", RunID: uuid.Must(uuid.NewV7()), DurationSeconds: 60}}}
	if n, _ := ticker(due.Add(-time.Minute), fixedBuilder{sum: week}).Tick(ctx); n != 0 {
		t.Fatal("a digest was due before its time")
	}
	if n, err := ticker(due.Add(30*time.Second), fixedBuilder{sum: week}).Tick(ctx); err != nil || n != 1 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	got := w.queued(t)
	if len(got) != 1 || got[0].channel != w.chA.ID || got[0].key != "digest:2026-09-28T08:00:00Z" || got[0].ev.Kind != notify.EventDigest {
		t.Fatalf("queued = %+v, want one digest for the enabled channel A", got)
	}
	if got[0].ev.Digest.Timezone != "UTC" || got[0].ev.Digest.Slowest[0].URL != "https://pulsix.example.test/o/digest-flow/runs/"+week.Slowest[0].RunID.String() {
		t.Fatalf("digest = %+v", got[0].ev.Digest)
	}
	after, err := svc.Digest(scoped, w.org)
	if err != nil || !after.NextRunAt.Equal(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("next after the send = %+v, %v", after.NextRunAt, err)
	}
	var period string
	if err := w.d.Pool().QueryRow(ctx, `SELECT last_period FROM notification_digests WHERE org_id = $1`, w.org).Scan(&period); err != nil || period != "digest:2026-09-28T08:00:00Z" {
		t.Fatalf("last_period = %q, %v", period, err)
	}
	// A builder failure leaves the next digest due.
	if _, err := ticker(time.Date(2026, 10, 5, 8, 0, 30, 0, time.UTC), fixedBuilder{err: true}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if still, _ := svc.Digest(scoped, w.org); !still.NextRunAt.Equal(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("a failed digest advanced to %s", still.NextRunAt)
	}
	// Settings that no longer evaluate disable the digest — and, since the
	// timezone is checked before the digest is even built (fix round 1,
	// Minor #2), no phantom message is queued for the broken row either.
	queuedBefore := len(w.queued(t))
	if _, err := w.d.Pool().Exec(ctx, `UPDATE notification_digests SET timezone = 'Mars/Olympus' WHERE org_id = $1`, w.org); err != nil {
		t.Fatal(err)
	}
	// Past the failed attempt's retry window (digestRetryDelay, backlog
	// 2.82): the tick above failed to build, so the row is held out of
	// the due scan for a while. Nothing is sent meanwhile, and the
	// broken settings are caught on the next scan that reaches it.
	if _, err := ticker(time.Date(2026, 10, 5, 8, 16, 0, 0, time.UTC), fixedBuilder{sum: week}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err := w.d.Pool().QueryRow(ctx, `SELECT enabled FROM notification_digests WHERE org_id = $1`, w.org).Scan(&enabled); err != nil || enabled {
		t.Fatalf("enabled = %v, %v; want the broken digest disabled", enabled, err)
	}
	if got := len(w.queued(t)); got != queuedBefore {
		t.Fatalf("queued = %d, want %d; a digest whose timezone no longer evaluates must not be sent", got, queuedBefore)
	}
}

// TestDigestLateDigestAdvancesWithoutSending is fix round 1, Minor #4: a
// digest more than maxDigestLate (24h) overdue is skipped by Tick's
// handle closure before it ever reaches the builder or the outbox, but
// processOne still must advance next_run_at — otherwise the same stale
// occurrence stays due forever and every tick re-skips it.
func TestDigestLateDigestAdvancesWithoutSending(t *testing.T) {
	w := newOutboxWorld(t, "digest-late")
	scoped := db.WithOrgScope(ctx, w.org)
	store := notify.NewSQLStore(w.d)
	setAt := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	svc := notify.NewService(notify.Deps{Channels: store, Digests: store, Now: func() time.Time { return setAt }})
	if _, err := svc.SetDigest(scoped, w.org, notify.DigestInput{Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{w.chA.ID}}); err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC) // the digest's next_run_at
	// now is 49h after due — more than maxDigestLate (24h) — so this
	// occurrence must be skipped, not sent 49h late.
	now := due.Add(49 * time.Hour)
	week := notify.DigestSummary{Schedules: []notify.DigestSchedule{{Name: "Nightly", Runs: 1}}}
	tk := notify.NewDigestTicker(notify.NewDigestSQLStore(w.d, quiet(), nil), fixedBuilder{sum: week}, w.outbox, time.Minute, func() time.Time { return now }, nil, quiet())
	if n, err := tk.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	if got := w.queued(t); len(got) != 0 {
		t.Fatalf("queued = %+v, want nothing for a digest 49h late", got)
	}
	after, err := svc.Digest(scoped, w.org)
	if err != nil {
		t.Fatal(err)
	}
	// The stale occurrence must not repeat: the row is advanced to the
	// next Monday 08:00 UTC strictly after now (Wed 30 Sep 09:00 UTC), not
	// left pointing at the skipped due time — which would re-skip forever.
	want := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	if after.NextRunAt == nil || !after.NextRunAt.Equal(want) {
		t.Fatalf("next_run_at = %v, want %s", after.NextRunAt, want)
	}
}

var errBuild = errors.New("db down")

type fixedBuilder struct {
	sum notify.DigestSummary
	err bool
}

func (b fixedBuilder) Digest(context.Context, uuid.UUID, time.Time, time.Time) (notify.DigestSummary, error) {
	if b.err {
		return notify.DigestSummary{}, errBuild
	}
	return b.sum, nil
}

// TestDigestStoreCommitsAfterAHandleSlowerThanTheWriteTimeout is the
// regression test for the CI failure "commit digest tick: conn closed".
//
// processOne used to open its detached write-back context *before*
// calling handle, so the context's digestWriteTimeout budget was spent by
// handle itself. Detaching from cancellation is not detaching from the
// clock: a build that takes longer than that budget — trivial at the
// default one-minute tick, whose batch deadline is four minutes — left
// the row's advance, its savepoint commit and its rollback all running on
// an already-expired context. pgx aborts an in-flight statement by
// poisoning its connection, so the expired context did not merely skip
// this row: it destroyed the connection the batch's own (correctly
// detached) commit still had to run on, so ProcessDue failed with "conn
// closed" and every savepoint already committed in the batch was lost.
//
// The handle here sleeps past digestWriteTimeout without touching the
// batch context, which carries no deadline at all — so nothing but
// processOne's own budgeting can fail this, and against the pre-fix code
// it fails every time rather than as a race.
func TestDigestStoreCommitsAfterAHandleSlowerThanTheWriteTimeout(t *testing.T) {
	w := newOutboxWorld(t, "digest-slow-handle")
	scoped := db.WithOrgScope(ctx, w.org)
	store := notify.NewSQLStore(w.d)
	set := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC) // a Tuesday
	svc := notify.NewService(notify.Deps{Channels: store, Digests: store, Now: func() time.Time { return set }})
	stored, err := svc.SetDigest(scoped, w.org, notify.DigestInput{
		Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{w.chA.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	due := stored.NextRunAt.Add(30 * time.Second)

	var handled int
	n, err := notify.NewDigestSQLStore(w.d, quiet(), nil).ProcessDue(ctx, due, 20,
		func(context.Context, pgx.Tx, notify.DueDigest) error {
			handled++
			// Longer than one row's write-back budget, and shorter than
			// any batch deadline a real tick would impose.
			time.Sleep(slowHandle)
			return nil
		})
	if err != nil {
		t.Fatalf("ProcessDue after a slow handle: %v", err)
	}
	if n != 1 || handled != 1 {
		t.Fatalf("processed %d rows, handled %d, want 1 and 1", n, handled)
	}
	// The write-back must have landed: a row whose handle succeeded is
	// advanced to the next week and stamped with the occurrence it sent.
	after, err := svc.Digest(scoped, w.org)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	if after.NextRunAt == nil || !after.NextRunAt.Equal(want) {
		t.Fatalf("next_run_at = %v, want %s", after.NextRunAt, want)
	}
	var period string
	if err := w.d.Pool().QueryRow(ctx, `SELECT last_period FROM notification_digests WHERE org_id = $1`, w.org).Scan(&period); err != nil {
		t.Fatal(err)
	}
	if period != (notify.DueDigest{Occurrence: *stored.NextRunAt}).Key() {
		t.Fatalf("last_period = %q, want the sent occurrence", period)
	}
}

// slowHandle exceeds digestWriteTimeout (10s, unexported), the budget one
// row's write-back gets once handle has returned.
const slowHandle = 11 * time.Second

// TestADigestSkippedForBeingLateIsNotRecordedAsSent is backlog 2.82. A
// digest more than maxDigestLate overdue is skipped on purpose — the
// manager was down and the week it covers has passed — and the ticker
// signalled that by returning nil from handle, which processOne could
// not tell from a successful send. So the row advanced to next week AND
// stamped last_period with an occurrence nothing was ever sent for.
// last_period is the record of what WAS delivered; a skipped period
// must leave it alone while next_run_at still moves on.
func TestADigestSkippedForBeingLateIsNotRecordedAsSent(t *testing.T) {
	w := newOutboxWorld(t, "digest-late")
	scoped := db.WithOrgScope(ctx, w.org)
	store := notify.NewSQLStore(w.d)
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	svc := notify.NewService(notify.Deps{Channels: store, Digests: store, Now: func() time.Time { return now }})
	if _, err := svc.SetDigest(scoped, w.org, notify.DigestInput{
		Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{w.chA.ID},
	}); err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)

	// First, a real send, so last_period holds something a wrong write
	// would visibly overwrite.
	week := notify.DigestSummary{Schedules: []notify.DigestSchedule{{Name: "Nightly", Runs: 2}}}
	ticker := func(at time.Time) *notify.DigestTicker {
		return notify.NewDigestTicker(notify.NewDigestSQLStore(w.d, quiet(), nil), fixedBuilder{sum: week}, w.outbox, time.Minute, func() time.Time { return at }, nil, quiet())
	}
	if n, err := ticker(due.Add(30 * time.Second)).Tick(ctx); err != nil || n != 1 {
		t.Fatalf("the first Tick = %d, %v", n, err)
	}
	sent := (notify.DueDigest{Occurrence: due}).Key()
	if got := lastPeriod(t, w); got != sent {
		t.Fatalf("last_period after a real send = %q, want %q", got, sent)
	}

	// Now the next occurrence comes due while the manager is away for
	// two days, so the tick that finally runs is past maxDigestLate.
	nextDue := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	queuedBefore := len(w.queued(t))
	if n, err := ticker(nextDue.Add(48 * time.Hour)).Tick(ctx); err != nil || n != 1 {
		t.Fatalf("the late Tick = %d, %v", n, err)
	}
	if got := len(w.queued(t)); got != queuedBefore {
		t.Fatalf("a skipped digest queued %d new messages", got-queuedBefore)
	}
	// It moved on — the skipped week is not retried for ever...
	after, err := svc.Digest(scoped, w.org)
	if err != nil {
		t.Fatal(err)
	}
	if after.NextRunAt == nil || !after.NextRunAt.After(nextDue) {
		t.Fatalf("next_run_at = %v, want it advanced past the skipped occurrence", after.NextRunAt)
	}
	// ...but it did not claim to have sent it.
	if got := lastPeriod(t, w); got != sent {
		t.Fatalf("last_period = %q, want it still %q: nothing was sent for the skipped week", got, sent)
	}
}

func lastPeriod(t *testing.T, w *outboxWorld) string {
	t.Helper()
	var period string
	if err := w.d.Pool().QueryRow(ctx, `SELECT last_period FROM notification_digests WHERE org_id = $1`, w.org).Scan(&period); err != nil {
		t.Fatal(err)
	}
	return period
}

// TestAFailingDigestDoesNotStarveTheOthers is backlog 2.82. LockDueDigests
// orders by next_run_at and takes the oldest digestBatch rows. A digest
// whose build keeps failing never advances, so it keeps the oldest
// next_run_at and is re-selected on every tick — and with digestBatch
// such organizations, no healthy one ever gets a slot. Each jam only
// clears when the stuck rows pass maxDigestLate, by which point the
// healthy digests behind them are themselves too late to send: they are
// skipped, silently, for that week.
//
// Two organizations and a batch of one reproduce it exactly: the
// failing one is due first, so without a retry delay the healthy one is
// never reached.
func TestAFailingDigestDoesNotStarveTheOthers(t *testing.T) {
	w := newOutboxWorld(t, "digest-starve")
	other := seedOrg(t, w.d, "digest-starve-other")
	store := notify.NewSQLStore(w.d)
	due := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)

	// The failing organization is due a minute earlier, so it sorts
	// first on every scan.
	setDigest(t, w.d, store, w.org, due.Add(-time.Minute))
	setDigest(t, w.d, store, other, due)

	handled := map[uuid.UUID]int{}
	st := notify.NewDigestSQLStore(w.d, quiet(), nil)
	run := func(now time.Time) {
		t.Helper()
		if _, err := st.ProcessDue(ctx, now, 1, func(_ context.Context, _ pgx.Tx, d notify.DueDigest) error {
			handled[d.OrgID]++
			if d.OrgID == w.org {
				return errors.New("this organization's digest cannot be built")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	now := due.Add(time.Minute)
	run(now)
	if handled[w.org] != 1 || handled[other] != 0 {
		t.Fatalf("after the first tick: failing = %d, healthy = %d; want the failing one taken first", handled[w.org], handled[other])
	}
	// The very next tick, no clock movement to speak of: the healthy
	// organization must now get the slot.
	run(now.Add(time.Second))
	if handled[other] != 1 {
		t.Fatalf("the healthy digest was never reached: failing = %d, healthy = %d", handled[w.org], handled[other])
	}
}

// setDigest writes an enabled weekly digest for org due at next.
func setDigest(t *testing.T, d *db.DB, store *notify.SQLStore, org uuid.UUID, next time.Time) {
	t.Helper()
	ch := seedChannel(t, d, org)
	svc := notify.NewService(notify.Deps{Channels: store, Digests: store, Now: func() time.Time { return next.AddDate(0, 0, -1) }})
	if _, err := svc.SetDigest(db.WithOrgScope(ctx, org), org, notify.DigestInput{
		Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{ch.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE notification_digests SET next_run_at = $2 WHERE org_id = $1`, org, next); err != nil {
		t.Fatal(err)
	}
}

// TestTwoDigestTickersDoNotDoubleSendAPeriod is backlog 2.82. Several
// managers each run a digest ticker, so two can scan the same instant.
// Two mechanisms are supposed to stop a period going out twice —
// LockDueDigests' FOR NO KEY UPDATE SKIP LOCKED, and the outbox's
// unique (org, event_key, channel) with the occurrence in the key —
// but nothing proved it, and a double-sent weekly digest is the kind
// of thing nobody notices until a customer asks why they got two.
func TestTwoDigestTickersDoNotDoubleSendAPeriod(t *testing.T) {
	w := newOutboxWorld(t, "digest-concurrent")
	store := notify.NewSQLStore(w.d)
	due := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	setDigest(t, w.d, store, w.org, due)
	now := due.Add(30 * time.Second)

	week := notify.DigestSummary{Schedules: []notify.DigestSchedule{{Name: "Nightly", Runs: 2}}}
	ticker := func() *notify.DigestTicker {
		return notify.NewDigestTicker(notify.NewDigestSQLStore(w.d, quiet(), nil), fixedBuilder{sum: week}, w.outbox, time.Minute, func() time.Time { return now }, nil, quiet())
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = ticker().Tick(ctx)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("ticker %d: %v", i, err)
		}
	}

	key := (notify.DueDigest{Occurrence: due}).Key()
	var n int
	if err := w.d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM notification_deliveries WHERE org_id = $1 AND event_key = $2`, w.org, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the period %s was queued %d times, want exactly 1", key, n)
	}
}

// TestADisabledDigestIsRecordedWhereSomeoneCanSeeIt is backlog 2.82.
// Stored settings whose timezone no longer resolves stop the digest —
// the right call, since no retry fixes a zone name that is gone. But
// the only trace was a line in the manager's log: for the
// organization, its weekly digest simply switched itself off, with an
// unticked checkbox as the entire explanation. A schedule that fails
// for the same reason records a visible failed run.
func TestADisabledDigestIsRecordedWhereSomeoneCanSeeIt(t *testing.T) {
	w := newOutboxWorld(t, "digest-disabled-audit")
	store := notify.NewSQLStore(w.d)
	scoped := db.WithOrgScope(ctx, w.org)
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	svc := notify.NewService(notify.Deps{Channels: store, Digests: store, Now: func() time.Time { return now }})
	if _, err := svc.SetDigest(scoped, w.org, notify.DigestInput{
		Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{w.chA.ID},
	}); err != nil {
		t.Fatal(err)
	}
	// A zone the database holds but no manager can resolve — a direct
	// edit, a restore, or a name IANA dropped.
	if _, err := w.d.Pool().Exec(ctx, `UPDATE notification_digests SET timezone = 'Mars/Olympus' WHERE org_id = $1`, w.org); err != nil {
		t.Fatal(err)
	}

	rec := &recordingAuditor{}
	st := notify.NewDigestSQLStore(w.d, quiet(), rec)
	due := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	if _, err := st.ProcessDue(ctx, due.Add(time.Minute), 20, func(context.Context, pgx.Tx, notify.DueDigest) error {
		t.Fatal("a digest whose timezone does not resolve must not be built")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var enabled bool
	if err := w.d.Pool().QueryRow(ctx, `SELECT enabled FROM notification_digests WHERE org_id = $1`, w.org).Scan(&enabled); err != nil || enabled {
		t.Fatalf("enabled = %v, %v; want it disabled", enabled, err)
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want the disable recorded once: %+v", len(events), events)
	}
	e := events[0]
	if e.OrgID == nil || *e.OrgID != w.org {
		t.Fatalf("event org = %v, want %s", e.OrgID, w.org)
	}
	if !strings.Contains(e.Summary, "Mars/Olympus") {
		t.Fatalf("summary = %q; it must name the zone that stopped resolving — that is the whole repair instruction", e.Summary)
	}
	if e.ObjectType != "notification_digest" {
		t.Fatalf("object type = %q", e.ObjectType)
	}
}

// recordingAuditor collects the system audit events the digest store
// writes, so a test can assert the organization was told.
type recordingAuditor struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordingAuditor) Record(_ context.Context, e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingAuditor) snapshot() []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.Event(nil), r.events...)
}
