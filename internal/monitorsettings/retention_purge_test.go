package monitorsettings_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/monitorsettings"
)

// fakeClock is the purger's clock; every DeleteBatch of fakePurgeStore
// advances it by step, so a test controls how many batches fit the budget.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type purgeCall struct {
	table monitorsettings.Table
	org   uuid.UUID
	r     monitorsettings.TimeRange
	limit int
}

// fakePurgeStore holds, per (table, org), how many rows each range still
// has, and deletes from them in limit-sized batches.
type fakePurgeStore struct {
	clock   *fakeClock
	step    time.Duration
	orgs    []monitorsettings.OrgRetention
	ranges  map[monitorsettings.Table][]monitorsettings.TimeRange
	rows    map[monitorsettings.Table]map[uuid.UUID]map[time.Time]int64
	listMax int
	listErr error
	delErr  map[uuid.UUID]error
	// onDelete, when set, runs after every DeleteBatch (a test cancels its
	// context there).
	onDelete func()
	calls    []purgeCall
	befores  []time.Time
}

func newFakePurgeStore(clock *fakeClock) *fakePurgeStore {
	return &fakePurgeStore{
		clock:  clock,
		ranges: map[monitorsettings.Table][]monitorsettings.TimeRange{},
		rows:   map[monitorsettings.Table]map[uuid.UUID]map[time.Time]int64{},
		delErr: map[uuid.UUID]error{},
	}
}

func (f *fakePurgeStore) put(table monitorsettings.Table, org uuid.UUID, start time.Time, n int64) {
	if f.rows[table] == nil {
		f.rows[table] = map[uuid.UUID]map[time.Time]int64{}
	}
	if f.rows[table][org] == nil {
		f.rows[table][org] = map[time.Time]int64{}
	}
	f.rows[table][org][start] = n
}

func (f *fakePurgeStore) OrgsWithShorterRetention(_ context.Context, maxDays int) ([]monitorsettings.OrgRetention, error) {
	f.listMax = maxDays
	return f.orgs, f.listErr
}

func (f *fakePurgeStore) ChunkRanges(_ context.Context, table monitorsettings.Table, before time.Time) ([]monitorsettings.TimeRange, error) {
	f.befores = append(f.befores, before)
	var out []monitorsettings.TimeRange
	for _, r := range f.ranges[table] {
		if r.Start.Before(before) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakePurgeStore) DeleteBatch(_ context.Context, table monitorsettings.Table, org uuid.UUID, r monitorsettings.TimeRange, limit int) (int64, error) {
	f.calls = append(f.calls, purgeCall{table, org, r, limit})
	f.clock.now = f.clock.now.Add(f.step)
	if err := f.delErr[org]; err != nil {
		return 0, err
	}
	// Rows are kept per chunk; the purger may narrow a range inside its
	// chunk, so look the chunk up by the range's start.
	chunk := r.Start
	for _, c := range f.ranges[table] {
		if !r.Start.Before(c.Start) && r.Start.Before(c.End) {
			chunk = c.Start
		}
	}
	left := f.rows[table][org][chunk]
	n := min(left, int64(limit))
	if n > 0 {
		f.rows[table][org][chunk] = left - n
	}
	if f.onDelete != nil {
		f.onDelete()
	}
	return n, nil
}

// fakeLocker records the lock calls.
type fakeLocker struct {
	free     bool
	err      error
	unlocked int
}

func (l *fakeLocker) TryLock(context.Context) (bool, error) { return l.free, l.err }
func (l *fakeLocker) Unlock(context.Context)                { l.unlocked++ }

var purgeNow = time.Date(2026, 11, 13, 12, 30, 0, 0, time.UTC)

func day(n int) time.Time { return purgeNow.Truncate(24*time.Hour).AddDate(0, 0, -n) }

func dayRange(n int) monitorsettings.TimeRange {
	return monitorsettings.TimeRange{Start: day(n), End: day(n - 1)}
}

func orgIDs(n int) []uuid.UUID {
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = uuid.Must(uuid.NewV7())
	}
	return ids // NewV7 ids are ascending, the store's ORDER BY org_id
}

func newTestPurger(store *fakePurgeStore, locker monitorsettings.Locker, maxDays int, log *slog.Logger) *monitorsettings.Purger {
	p := monitorsettings.NewPurger(store, locker, maxDays, 0, store.clock.Now, log)
	p.SetBatchPause(0)
	return p
}

// TestPurgeDeletesOnlyOlderRowsInBatches: one org at 30 days under a 90-day
// maximum; ranges older than its cutoff are drained in batches of
// RetentionPurgeBatch until a batch comes back short, the range holding the
// cutoff is clamped to it, newer ranges are never touched, both tables.
func TestPurgeDeletesOnlyOlderRowsInBatches(t *testing.T) {
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	org := uuid.Must(uuid.NewV7())
	store.orgs = []monitorsettings.OrgRetention{{OrgID: org, Days: 30}}
	cutoff := purgeNow.AddDate(0, 0, -30)
	for _, tbl := range []monitorsettings.Table{monitorsettings.TableCheckSamples, monitorsettings.TableCheckStateChanges} {
		store.ranges[tbl] = []monitorsettings.TimeRange{dayRange(40), dayRange(31), dayRange(30), dayRange(29)}
	}
	store.put(monitorsettings.TableCheckSamples, org, day(40), 25000)
	store.put(monitorsettings.TableCheckSamples, org, day(31), 10000)
	store.put(monitorsettings.TableCheckSamples, org, day(30), 5)
	store.put(monitorsettings.TableCheckStateChanges, org, day(40), 7)

	res := newTestPurger(store, nil, 90, nil).Round(ctx)

	if store.listMax != 90 {
		t.Fatalf("listed with max %d, want 90", store.listMax)
	}
	if !res.Complete || res.Orgs != 1 || res.Samples != 35005 || res.StateChanges != 7 || res.Errors != 0 {
		t.Fatalf("result = %+v, want complete, 1 org, 35005 samples, 7 state changes", res)
	}
	// The chunks are listed once per table and round, up to the round's
	// start; each org then stops at its own cutoff.
	if len(store.befores) != 2 || !store.befores[0].Equal(purgeNow) || !store.befores[1].Equal(purgeNow) {
		t.Fatalf("ranges listed before %v, want once per table before the round's start %v", store.befores, purgeNow)
	}
	var samples []purgeCall
	for _, c := range store.calls {
		if c.org != org || c.limit != monitorsettings.RetentionPurgeBatch || monitorsettings.RetentionPurgeBatch != 10000 {
			t.Fatalf("call %+v, want org %v and limit 10000", c, org)
		}
		if c.r.End.After(cutoff) || !c.r.Start.Before(cutoff) {
			t.Fatalf("call range %+v reaches past the cutoff %v", c.r, cutoff)
		}
		if c.table == monitorsettings.TableCheckSamples {
			samples = append(samples, c)
		}
	}
	// day 40: 10000, 10000, 5000 (short) = 3; day 31: 10000, 0 = 2;
	// day 30 (clamped to the cutoff): 5 = 1.
	if len(samples) != 6 {
		t.Fatalf("sample batches = %d (%+v), want 6", len(samples), samples)
	}
	if last := samples[5].r; !last.Start.Equal(day(30)) || !last.End.Equal(cutoff) {
		t.Fatalf("the range holding the cutoff = %+v, want [%v, %v)", last, day(30), cutoff)
	}
}

// TestPurgeStopsAtTheBudget: each batch takes a minute of the fake clock,
// so the two-minute budget admits two; the run reports itself incomplete,
// and the next run starts with the org after the interrupted one, then
// resumes it.
func TestPurgeStopsAtTheBudget(t *testing.T) {
	if monitorsettings.RetentionPurgeBudget != 2*time.Minute {
		t.Fatalf("RetentionPurgeBudget = %v, want 2m", monitorsettings.RetentionPurgeBudget)
	}
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	store.step = time.Minute
	ids := orgIDs(2)
	big, small := ids[0], ids[1]
	store.orgs = []monitorsettings.OrgRetention{{OrgID: big, Days: 30}, {OrgID: small, Days: 30}}
	store.ranges[monitorsettings.TableCheckSamples] = []monitorsettings.TimeRange{dayRange(40)}
	store.put(monitorsettings.TableCheckSamples, big, day(40), 50000)
	store.put(monitorsettings.TableCheckSamples, small, day(40), 3)
	p := newTestPurger(store, nil, 90, nil)

	res := p.Round(ctx)
	if res.Complete || res.Samples != 20000 || len(store.calls) != 2 {
		t.Fatalf("first run = %+v after %d batches, want incomplete after 2 batches, 20000 rows", res, len(store.calls))
	}

	store.calls = nil
	clock.now = clock.now.Add(time.Hour)
	res = p.Round(ctx)
	if len(store.calls) == 0 || store.calls[0].org != small {
		t.Fatalf("second run started with %+v, want the org after the interrupted one", store.calls)
	}
	if res.Complete || res.Samples != 10003 {
		t.Fatalf("second run = %+v, want incomplete, 3 + 10000 rows", res)
	}
	if got := store.rows[monitorsettings.TableCheckSamples][small][day(40)]; got != 0 {
		t.Fatalf("small org has %d rows left, want 0", got)
	}
}

// TestPurgeCarriesOnAfterAnOrgFails: a failing org is logged and counted,
// the next org is still purged, and the run is not complete.
func TestPurgeCarriesOnAfterAnOrgFails(t *testing.T) {
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	ids := orgIDs(2)
	store.orgs = []monitorsettings.OrgRetention{{OrgID: ids[0], Days: 30}, {OrgID: ids[1], Days: 90}}
	store.ranges[monitorsettings.TableCheckSamples] = []monitorsettings.TimeRange{dayRange(120)}
	store.put(monitorsettings.TableCheckSamples, ids[1], day(120), 4)
	store.delErr[ids[0]] = errors.New("boom")
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	res := newTestPurger(store, nil, 365, log).Round(ctx)
	if res.Errors != 1 || res.Samples != 4 || res.Complete {
		t.Fatalf("result = %+v, want 1 error, 4 rows, incomplete", res)
	}
	out := buf.String()
	if !strings.Contains(out, "boom") || !strings.Contains(out, ids[0].String()) {
		t.Fatalf("log = %q, want the failing org and its error", out)
	}
	if strings.Count(out, "measurement retention purge") != 2 { // one error line, one summary
		t.Fatalf("log = %q, want one error line and one summary", out)
	}
}

// TestPurgeLogsOneSummary: a run with work logs exactly one INFO line with
// the org count and the rows deleted, whatever the number of batches; a
// run without work logs nothing at INFO.
func TestPurgeLogsOneSummary(t *testing.T) {
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	org := uuid.Must(uuid.NewV7())
	store.orgs = []monitorsettings.OrgRetention{{OrgID: org, Days: 30}}
	store.ranges[monitorsettings.TableCheckSamples] = []monitorsettings.TimeRange{dayRange(50), dayRange(40)}
	store.put(monitorsettings.TableCheckSamples, org, day(50), 30000)
	store.put(monitorsettings.TableCheckSamples, org, day(40), 30000)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	newTestPurger(store, nil, 90, log).Round(ctx)
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "orgs=1") || !strings.Contains(out, "deleted_samples=60000") || !strings.Contains(out, "complete=true") {
		t.Fatalf("log = %q, want one summary line with orgs=1 deleted_samples=60000 complete=true", out)
	}

	buf.Reset()
	empty := newFakePurgeStore(clock)
	newTestPurger(empty, nil, 90, log).Round(ctx)
	if buf.Len() != 0 {
		t.Fatalf("log of a run without work = %q, want nothing at INFO", buf.String())
	}
}

// TestPurgeSkipsWithoutTheLock: another instance holds the lock, so the
// run lists nothing and deletes nothing; with the lock it unlocks after.
func TestPurgeSkipsWithoutTheLock(t *testing.T) {
	if monitorsettings.RetentionPurgeLockKey != 8317 {
		t.Fatalf("RetentionPurgeLockKey = %d, want 8317", monitorsettings.RetentionPurgeLockKey)
	}
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	store.orgs = []monitorsettings.OrgRetention{{OrgID: uuid.Must(uuid.NewV7()), Days: 30}}
	held := &fakeLocker{free: false}
	newTestPurger(store, held, 90, nil).Round(ctx)
	if store.listMax != 0 || len(store.calls) != 0 || held.unlocked != 0 {
		t.Fatalf("run without the lock listed (max %d) or deleted (%d calls)", store.listMax, len(store.calls))
	}
	failing := &fakeLocker{err: errors.New("no session")}
	newTestPurger(store, failing, 90, nil).Round(ctx)
	if store.listMax != 0 {
		t.Fatal("run with a failing lock listed orgs")
	}
	free := &fakeLocker{free: true}
	newTestPurger(store, free, 90, nil).Round(ctx)
	if store.listMax != 90 || free.unlocked != 1 {
		t.Fatalf("run with the lock: listed max %d, unlocked %d; want 90 and 1", store.listMax, free.unlocked)
	}
}

// TestPurgeListFailure: a failing list is an error, nothing is deleted.
func TestPurgeListFailure(t *testing.T) {
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	store.listErr = errors.New("db down")
	res := newTestPurger(store, nil, 90, nil).Round(ctx)
	if res.Errors != 1 || res.Complete || len(store.calls) != 0 {
		t.Fatalf("result = %+v with %d calls, want 1 error and no deletes", res, len(store.calls))
	}
}

// TestPurgeRunStopsWithItsContext: Run returns once its context is done.
func TestPurgeRunStopsWithItsContext(t *testing.T) {
	clock := &fakeClock{now: purgeNow}
	p := monitorsettings.NewPurger(newFakePurgeStore(clock), nil, 90, time.Millisecond, clock.Now, nil)
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { p.Run(rctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// TestPurgeUsesTheEffectiveRetention: the purger deletes by
// EffectiveRetentionDays, whatever the store lists: an org whose choice is
// not below the maximum is left alone, a shorter one is cut at its choice.
func TestPurgeUsesTheEffectiveRetention(t *testing.T) {
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	ids := orgIDs(4)
	// A stale or inconsistent list: 365 and 120 are not below the maximum
	// 90, 0 is no choice; only the 30-day org has a shorter retention.
	store.orgs = []monitorsettings.OrgRetention{{OrgID: ids[0], Days: 365}, {OrgID: ids[1], Days: 30}, {OrgID: ids[2], Days: 0}, {OrgID: ids[3], Days: 120}}
	store.ranges[monitorsettings.TableCheckSamples] = []monitorsettings.TimeRange{dayRange(200), dayRange(60)}
	for _, id := range ids {
		store.put(monitorsettings.TableCheckSamples, id, day(200), 1)
		store.put(monitorsettings.TableCheckSamples, id, day(60), 1)
	}
	res := newTestPurger(store, nil, 90, nil).Round(ctx)
	for _, c := range store.calls {
		if c.org != ids[1] {
			t.Fatalf("deleted for org %v, want only the 30-day org", c.org)
		}
		if c.r.End.After(purgeNow.AddDate(0, 0, -30)) {
			t.Fatalf("range %+v past the 30-day cutoff", c.r)
		}
	}
	if res.Samples != 2 {
		t.Fatalf("deleted %d samples, want the 30-day org's 2", res.Samples)
	}
}

// TestPurgeRevisitsOnlyNewRangesUntilTheDailySweep: once an org's table is
// drained to its cutoff, the next round (an hour later) only deletes
// between the old and the new cutoff, one statement per table, instead of
// one per old chunk; a day later a full sweep visits every old chunk again
// (late rows with old timestamps, another manager's progress).
func TestPurgeRevisitsOnlyNewRangesUntilTheDailySweep(t *testing.T) {
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	org := uuid.Must(uuid.NewV7())
	store.orgs = []monitorsettings.OrgRetention{{OrgID: org, Days: 30}}
	var ranges []monitorsettings.TimeRange
	for d := 90; d >= 0; d-- {
		ranges = append(ranges, dayRange(d))
	}
	for _, tbl := range []monitorsettings.Table{monitorsettings.TableCheckSamples, monitorsettings.TableCheckStateChanges} {
		store.ranges[tbl] = ranges
	}
	p := newTestPurger(store, nil, 90, nil)

	p.Round(ctx)
	if n := len(store.calls); n < 2*60 {
		t.Fatalf("first round: %d calls, want one per old chunk and table (>= 120)", n)
	}

	store.calls = nil
	oldCutoff := clock.now.AddDate(0, 0, -30)
	clock.now = clock.now.Add(time.Hour)
	newCutoff := clock.now.AddDate(0, 0, -30)
	if res := p.Round(ctx); !res.Complete {
		t.Fatalf("second round = %+v, want complete", res)
	}
	if len(store.calls) != 2 {
		t.Fatalf("second round: %d calls (%+v), want one per table", len(store.calls), store.calls)
	}
	for _, c := range store.calls {
		if !c.r.Start.Equal(oldCutoff) || !c.r.End.Equal(newCutoff) {
			t.Fatalf("second round range %+v, want [%v, %v)", c.r, oldCutoff, newCutoff)
		}
	}

	store.calls = nil
	clock.now = clock.now.Add(monitorsettings.RetentionPurgeFullSweepInterval)
	p.Round(ctx)
	if n := len(store.calls); n < 2*60 {
		t.Fatalf("round after a day: %d calls, want the full sweep again (>= 120)", n)
	}
}

// TestPurgeStopsQuietlyOnShutdown: a context cancelled mid-round ends the
// round without an error per remaining org.
func TestPurgeStopsQuietlyOnShutdown(t *testing.T) {
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	ids := orgIDs(5)
	for _, id := range ids {
		store.orgs = append(store.orgs, monitorsettings.OrgRetention{OrgID: id, Days: 30})
		store.put(monitorsettings.TableCheckSamples, id, day(40), 50000)
	}
	store.ranges[monitorsettings.TableCheckSamples] = []monitorsettings.TimeRange{dayRange(40)}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	store.onDelete = cancel
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	res := newTestPurger(store, nil, 90, log).Round(rctx)
	if res.Errors != 0 || res.Complete || len(store.calls) != 1 {
		t.Fatalf("result = %+v after %d calls, want no errors, incomplete, one batch", res, len(store.calls))
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("log = %q, want no error on shutdown", buf.String())
	}
}

// TestPurgePausesBetweenBatches: with a pause set, consecutive batches
// that deleted rows are spaced by it.
func TestPurgePausesBetweenBatches(t *testing.T) {
	if monitorsettings.RetentionPurgeBatchPause != 100*time.Millisecond {
		t.Fatalf("RetentionPurgeBatchPause = %v, want 100ms", monitorsettings.RetentionPurgeBatchPause)
	}
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	org := uuid.Must(uuid.NewV7())
	store.orgs = []monitorsettings.OrgRetention{{OrgID: org, Days: 30}}
	store.ranges[monitorsettings.TableCheckSamples] = []monitorsettings.TimeRange{dayRange(40)}
	store.put(monitorsettings.TableCheckSamples, org, day(40), 30000) // 3 full batches and an empty one
	p := monitorsettings.NewPurger(store, nil, 90, 0, clock.Now, nil)
	p.SetBatchPause(20 * time.Millisecond)
	start := time.Now()
	p.Round(ctx)
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("round took %v, want at least 3 pauses of 20ms", elapsed)
	}
}
