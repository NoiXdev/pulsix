// The retest scheduler's tests: unit tests with a fake clock, fake store
// and fake enqueuer (selection, stagger, trigger), and the integration
// test for the no-duplicate-queueing invariant against the real database.
package schedule_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedule"
)

// fakeSealer seals nothing real: the integration enqueue path stores
// connection_test payloads plain (nil payload, never sealed).
type fakeSealer struct{}

func (fakeSealer) SealAAD(plain, _ []byte) ([]byte, error) { return plain, nil }

func (fakeSealer) OpenAAD(sealed, _ []byte) ([]byte, error) { return sealed, nil }

// seedOrgID and seedZoneID build the org/zone the integration test needs.
func seedOrgID(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func seedZoneID(t *testing.T, q *gen.Queries, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: id, OrgID: orgID, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	return id
}

var ctx = context.Background()

// fakeRetestStore is the Store fake.
type fakeRetestStore struct {
	orgs []uuid.UUID
	due  map[uuid.UUID][]hosts.Host
	err  error
}

func (f *fakeRetestStore) ListOrgIDs(context.Context) ([]uuid.UUID, error) {
	return f.orgs, nil
}

func (f *fakeRetestStore) HostsDueForRetest(_ context.Context, orgID uuid.UUID, _ time.Time) ([]hosts.Host, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.due[orgID], nil
}

// fakeEnqueuer records EnqueueHostJob calls; whenEnqueued, when set, runs
// inside every enqueue (for tests that need the exact call-time state,
// e.g. whether the advisory lock is held).
type fakeEnqueuer struct {
	mu        sync.Mutex
	calls     []enqueueRecord
	onEnqueue func()
}

type enqueueRecord struct {
	orgID   uuid.UUID
	kind    jobs.Kind
	hostIDs []uuid.UUID
	trigger string
}

func (f *fakeEnqueuer) EnqueueHostJob(_ context.Context, orgID uuid.UUID, kind jobs.Kind, hostIDs []uuid.UUID, _ json.RawMessage, trigger string, _ *uuid.UUID) (jobs.Job, error) {
	if f.onEnqueue != nil {
		f.onEnqueue()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, enqueueRecord{orgID: orgID, kind: kind, hostIDs: hostIDs, trigger: trigger})
	return jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, ZoneID: uuid.Must(uuid.NewV7()), Kind: kind, HostIDs: hostIDs, Trigger: trigger}, nil
}

func (f *fakeEnqueuer) all() []enqueueRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// recordingSleep records waits without consuming real time.
type recordingSleep struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (r *recordingSleep) sleep(_ context.Context, d time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waits = append(r.waits, d)
	return nil
}

func newUnitTestRetest(store schedule.Store, eq *fakeEnqueuer, interval time.Duration, sleep *recordingSleep) *schedule.Retest {
	return schedule.NewRetest(store, eq, nil, interval, sleep.sleep, func() time.Time {
		return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	}, nil)
}

func TestRetestTickEnqueuesDueHosts(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	h1, h2 := hosts.Host{ID: uuid.Must(uuid.NewV7()), Name: "a", Managed: true}, hosts.Host{ID: uuid.Must(uuid.NewV7()), Name: "b", Managed: true}
	store := &fakeRetestStore{orgs: []uuid.UUID{org}, due: map[uuid.UUID][]hosts.Host{org: {h1, h2}}}
	eq := &fakeEnqueuer{}
	sleep := &recordingSleep{}
	r := newUnitTestRetest(store, eq, time.Hour, sleep)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	calls := eq.all()
	if len(calls) != 2 {
		t.Fatalf("enqueued %+v, want one per host", calls)
	}
	for _, c := range calls {
		if c.orgID != org || c.kind != jobs.KindConnectionTest || c.trigger != jobs.TriggerSystem {
			t.Fatalf("call = %+v", c)
		}
		if len(c.hostIDs) != 1 {
			t.Fatalf("one host per job: %+v", c)
		}
	}
}

func TestRetestTickStaggersByHostHash(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	hs := make([]hosts.Host, 4)
	for i := range hs {
		hs[i] = hosts.Host{ID: uuid.Must(uuid.NewV7()), Name: "h", Managed: true}
	}
	store := &fakeRetestStore{orgs: []uuid.UUID{org}, due: map[uuid.UUID][]hosts.Host{org: hs}}
	eq := &fakeEnqueuer{}
	sleep := &recordingSleep{}
	interval := 6 * time.Hour
	r := newUnitTestRetest(store, eq, interval, sleep)

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	// The delays are deterministic and inside the span.
	delays := make([]time.Duration, 0, len(hs))
	for _, h := range hs {
		d := schedule.DelayOf(h.ID, interval)
		if d < 0 || d >= interval {
			t.Fatalf("delay %v outside the interval", d)
		}
		delays = append(delays, d)
	}
	slices.Sort(delays)
	// The waits are the sorted deltas: first the smallest delay, then the
	// differences — adding up to the largest delay, never the interval.
	waits := sleep.waits
	if len(waits) != len(hs) {
		t.Fatalf("waits = %v", waits)
	}
	if waits[0] != delays[0] {
		t.Fatalf("first wait = %v, want the smallest delay %v", waits[0], delays[0])
	}
	var total time.Duration
	for _, w := range waits {
		total += w
	}
	if total != delays[len(delays)-1] {
		t.Fatalf("total wait = %v, want the largest delay %v", total, delays[len(delays)-1])
	}
	if total >= interval {
		t.Fatalf("a pass must stay inside one interval: %v", total)
	}
}

func TestRetestTickStaggerIsStablePerHost(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	interval := time.Hour
	first := schedule.DelayOf(id, interval)
	second := schedule.DelayOf(id, interval)
	if first != second || first < 0 || first >= interval {
		t.Fatalf("a host's stagger must be deterministic and inside the span: %v vs %v", first, second)
	}
}

func TestRetestTickSkipsOtherOrgFailures(t *testing.T) {
	orgA, orgB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := &fakeRetestStore{orgs: []uuid.UUID{orgA, orgB}, err: errors.New("locked")}
	eq := &fakeEnqueuer{}
	sleep := &recordingSleep{}
	r := newUnitTestRetest(store, eq, time.Hour, sleep)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(eq.all()) != 0 {
		t.Fatalf("a failing selection must enqueue nothing: %+v", eq.all())
	}
}

func TestRetestTickNoHostsEnqueuesNothing(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := &fakeRetestStore{orgs: []uuid.UUID{org}, due: map[uuid.UUID][]hosts.Host{}}
	eq := &fakeEnqueuer{}
	sleep := &recordingSleep{}
	r := newUnitTestRetest(store, eq, time.Hour, sleep)

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := eq.all(); len(calls) != 0 {
		t.Fatalf("no due hosts, no enqueues: %+v", calls)
	}
	if len(sleep.waits) != 0 {
		t.Fatalf("no due hosts, no staggering: %v", sleep.waits)
	}
}

func TestRetestTickContextCancelledMidPass(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	hs := []hosts.Host{{ID: uuid.Must(uuid.NewV7()), Managed: true}}
	store := &fakeRetestStore{orgs: []uuid.UUID{org}, due: map[uuid.UUID][]hosts.Host{org: hs}}
	eq := &fakeEnqueuer{}
	cancelErr := errors.New("cancelled")
	r := schedule.NewRetest(store, eq, nil, time.Hour, func(context.Context, time.Duration) error {
		return cancelErr
	}, time.Now, nil)

	if err := r.Tick(ctx); !errors.Is(err, cancelErr) {
		t.Fatalf("Tick = %v, want the sleep's error", err)
	}
}

func TestRetestRunStopsOnCancelledSleep(t *testing.T) {
	store := &fakeRetestStore{}
	eq := &fakeEnqueuer{}
	// The first interval wait is cancelled: Run must return, not spin.
	done := make(chan struct{})
	r := schedule.NewRetest(store, eq, nil, time.Millisecond, func(ctx context.Context, d time.Duration) error {
		return context.Canceled
	}, time.Now, nil)
	go func() {
		r.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on the cancelled wait")
	}
}

// fakeLocker is the Locker fake: held tracks the lock between TryLock and
// Unlock (the state an enqueue under the lock must observe), locked is
// what TryLock returns (another instance's race, or the win).
type fakeLocker struct {
	held   bool
	locked bool
}

func (f *fakeLocker) TryLock(context.Context) (bool, error) {
	f.held = f.locked
	return f.locked, nil
}

func (f *fakeLocker) Unlock(context.Context) {
	f.held = false
}

func TestRetestTickSkipsWhenAnotherInstanceHoldsTheLock(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := &fakeRetestStore{orgs: []uuid.UUID{org}, due: map[uuid.UUID][]hosts.Host{org: {{ID: uuid.Must(uuid.NewV7()), Managed: true}}}}
	eq := &fakeEnqueuer{}
	sleep := &recordingSleep{}
	locker := &fakeLocker{locked: false}
	r := schedule.NewRetest(store, eq, locker, time.Hour, sleep.sleep, time.Now, nil)

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(eq.all()) != 0 {
		t.Fatalf("a lost lock race must enqueue nothing: %+v", eq.all())
	}
}

func TestRetestTickEnqueuesUnderTheLock(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := &fakeRetestStore{orgs: []uuid.UUID{org}, due: map[uuid.UUID][]hosts.Host{org: {{ID: uuid.Must(uuid.NewV7()), Managed: true}}}}
	eq := &fakeEnqueuer{}
	sleep := &recordingSleep{}
	locker := &fakeLocker{locked: true}
	heldAtEnqueue := false
	eq.onEnqueue = func() { heldAtEnqueue = locker.held }
	r := schedule.NewRetest(store, eq, locker, time.Hour, sleep.sleep, time.Now, nil)

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := eq.all(); len(calls) != 1 {
		t.Fatalf("enqueue under the lock: %+v", calls)
	}
	if !heldAtEnqueue {
		t.Fatal("the pass must hold the lock while enqueuing")
	}
}

// TestRetestNoDuplicateQueueing is the integration invariant: a first pass
// enqueues one connection_test per due host; a second pass (and a third,
// before the first job is claimed) queues nothing — the queued job makes
// the host not due, and no host is ever double-queued.
func TestRetestNoDuplicateQueueing(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrgID(t, q, "retest-org")
	zone := seedZoneID(t, q, org)
	store := hosts.NewSQLStore(d)
	svc := hosts.NewService(store, time.Now)

	// Four managed hosts, two freshly tested (inside the window).
	hostIDs := make([]uuid.UUID, 0, 4)
	for _, name := range []string{"a", "b", "c", "d"} {
		h, err := svc.CreateHost(db.WithOrgScope(ctx, org), org, hosts.HostInput{
			Name: name, Address: "10.0.0.1" + name, ZoneID: zone, Managed: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		hostIDs = append(hostIDs, h.ID)
	}
	fresh := []uuid.UUID{hostIDs[0], hostIDs[1]}
	for _, id := range fresh {
		if err := store.CreateHostTest(db.WithOrgScope(ctx, org), hosts.HostTest{
			ID: uuid.Must(uuid.NewV7()), OrgID: org, HostID: id, At: time.Now().Add(-time.Hour),
			OK: true, HostKeyState: hosts.KeyStateUnpinned,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The REAL jobs service is the enqueuer: the invariant depends on the
	// queued rows the enqueuer writes, so the fake would prove nothing here.
	jobsSvc := jobs.NewService(jobs.NewSQLStore(d), nil, nil, nil, svc, nil, fakeSealer{}, time.Now, rand.Reader)
	sleep := &recordingSleep{}
	r := schedule.NewRetest(svc, jobsSvc, nil, time.Hour, sleep.sleep, time.Now, nil)

	// Pass 1: exactly the two untested hosts (a, b are freshly tested).
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	queued := queuedConnectionTests(t, d, org, hostIDs)
	if len(queued) != 2 {
		t.Fatalf("after pass 1: %d queued jobs, want exactly the two untested hosts", len(queued))
	}
	for _, id := range hostIDs[2:] {
		if !queued[id] {
			t.Fatalf("host %s was not queued by pass 1", id)
		}
	}
	for _, id := range fresh {
		if queued[id] {
			t.Fatalf("host %s is tested inside the window and must not be queued", id)
		}
	}
	// Pass 2: both hosts are queued now (not due) → no new job, no
	// duplicate for either.
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if queued := queuedConnectionTests(t, d, org, hostIDs); len(queued) != 2 {
		t.Fatalf("after pass 2: %d queued jobs — no duplicates allowed", len(queued))
	}
	// Pass 3: every host is tested or queued — nothing may be enqueued.
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if queued := queuedConnectionTests(t, d, org, hostIDs); len(queued) != 2 {
		t.Fatalf("after pass 3: %d queued jobs — no duplicates allowed", len(queued))
	}
}

// queuedConnectionTests maps the hosts that currently have an active
// (queued/claimed/running) connection_test job.
func queuedConnectionTests(t *testing.T, d *db.DB, org uuid.UUID, hostIDs []uuid.UUID) map[uuid.UUID]bool {
	t.Helper()
	rows, err := d.Pool().Query(ctx, `SELECT host_ids FROM jobs WHERE org_id = $1 AND kind = 'connection_test' AND status IN ('queued','claimed','running','cancelling')`, org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[uuid.UUID]bool{}
	for _, id := range hostIDs {
		want[id] = false
	}
	for rows.Next() {
		var ids []uuid.UUID
		if err := rows.Scan(&ids); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			want[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	out := map[uuid.UUID]bool{}
	for id, v := range want {
		if v {
			out[id] = true
		}
	}
	return out
}
