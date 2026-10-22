package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// recordingGroupSink records every finished run group; err is returned
// from each call.
type recordingGroupSink struct {
	mu    sync.Mutex
	calls []RunGroupResult
	err   error
}

func (s *recordingGroupSink) RunGroupFinished(_ context.Context, r RunGroupResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r)
	return s.err
}

func (s *recordingGroupSink) snapshot() []RunGroupResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RunGroupResult(nil), s.calls...)
}

type groupWorld struct {
	d    *db.DB
	org  uuid.UUID
	q    *SQLQueue
	sink *recordingGroupSink
}

func newGroupWorld(t *testing.T, slug string) *groupWorld {
	t.Helper()
	d := testdb.Open(t)
	org := uuid.Must(uuid.NewV7())
	if _, err := gen.New(d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: org, Slug: slug, Name: slug}); err != nil {
		t.Fatal(err)
	}
	q := NewSQLQueue(d, nil, nil, nil, nil, nil, time.Now, rand.Reader)
	sink := &recordingGroupSink{}
	q.SetRunGroupSink(sink)
	return &groupWorld{d: d, org: org, q: q, sink: sink}
}

// runGroup inserts one scheduled run group with one job per zone, each
// targeting one host and already running.
func (w *groupWorld) runGroup(t *testing.T, zones int) (uuid.UUID, uuid.UUID, []Job) {
	t.Helper()
	group, schedule, tmpl := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	var out []Job
	for i := 0; i < zones; i++ {
		j := Job{
			ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: testZone(t, w.d, w.org), Kind: KindRun, TemplateID: &tmpl,
			HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: StatusQueued, RunGroupID: group,
			Trigger: TriggerSchedule, TriggerRef: &schedule, CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}
		if err := NewSQLStore(w.d).CreateJob(db.WithOrgScope(context.Background(), w.org), j, nil); err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	for _, j := range out {
		if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'running', started_at = now() WHERE id = $1`, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	return group, schedule, out
}

func TestRunGroupReportedOnceWhenItsLastJobFinishes(t *testing.T) {
	w := newGroupWorld(t, "rungroup-once")
	group, schedule, js := w.runGroup(t, 2)
	// The worker writes JSON null for an empty host list (a nil slice).
	if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded,
		map[string]any{"changed_hosts": []string{js[0].HostIDs[0].String()}, "failed_hosts": nil}, ""); err != nil {
		t.Fatal(err)
	}
	if got := w.sink.snapshot(); len(got) != 0 {
		t.Fatalf("reported before the group finished: %+v", got)
	}
	if err := w.q.Finish(context.Background(), js[1].ID, StatusFailed,
		map[string]any{"changed_hosts": nil, "unreachable_hosts": []string{js[1].HostIDs[0].String()}}, "exited with code 4"); err != nil {
		t.Fatal(err)
	}
	got := w.sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("reports = %d, want 1", len(got))
	}
	r := got[0]
	if r.GroupID != group || r.OrgID != w.org || r.FirstJobID != js[0].ID || r.Status != StatusFailed || r.Jobs != 2 ||
		r.Hosts != 2 || r.HostsChanged != 1 || r.HostsFailed != 1 || r.HostsOK() != 1 ||
		r.Trigger != TriggerSchedule || r.TriggerRef == nil || *r.TriggerRef != schedule || r.StartedAt == nil || r.FinishedAt == nil {
		t.Fatalf("result = %+v", r)
	}
	// A repeated finish is the idempotent no-op and reports nothing more.
	if err := w.q.Finish(context.Background(), js[1].ID, StatusFailed, nil, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(w.sink.snapshot()); n != 1 {
		t.Fatalf("reports after a repeated finish = %d", n)
	}
}

func TestConcurrentFinishesReportTheGroupOnce(t *testing.T) {
	w := newGroupWorld(t, "rungroup-race")
	for round := 0; round < 5; round++ {
		_, _, js := w.runGroup(t, 3)
		var wg sync.WaitGroup
		for _, j := range js {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := w.q.Finish(context.Background(), j.ID, StatusSucceeded, nil, ""); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if n := len(w.sink.snapshot()); n != round+1 {
			t.Fatalf("round %d: reports = %d, want %d", round, n, round+1)
		}
	}
}

func TestCancellingTheLastQueuedJobCompletesTheGroup(t *testing.T) {
	w := newGroupWorld(t, "rungroup-cancel")
	_, _, js := w.runGroup(t, 2)
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'queued', started_at = NULL WHERE id = $1`, js[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	scoped := db.WithOrgScope(context.Background(), w.org)
	if status, err := NewSQLStore(w.d).CancelJob(scoped, w.org, js[1].ID); err != nil || status != StatusCancelled {
		t.Fatalf("cancel = %s, %v", status, err)
	}
	w.q.ClearTerminalPayload(scoped, js[1].ID)
	got := w.sink.snapshot()
	if len(got) != 1 || got[0].Status != StatusCancelled {
		t.Fatalf("reports = %+v, want one cancelled group", got)
	}
	// A repeated call into the completed group's check (fix round 1,
	// Important #1's claimRunGroupReport guard) is the deterministic,
	// non-racy shape of the same idempotency the concurrency test above
	// exercises under a real race.
	w.q.ClearTerminalPayload(scoped, js[1].ID)
	if n := len(w.sink.snapshot()); n != 1 {
		t.Fatalf("reports after a repeated group check = %d, want 1", n)
	}
}

func TestRunGroupSinkErrorDoesNotFailTheFinish(t *testing.T) {
	w := newGroupWorld(t, "rungroup-sinkerr")
	w.sink.err = errors.New("outbox down")
	_, _, js := w.runGroup(t, 1)
	if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("Finish = %v, want nil although the sink failed", err)
	}
	var status string
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT status FROM jobs WHERE id = $1`, js[0].ID).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("status = %s, %v", status, err)
	}
}

func TestNonRunJobsAreNotReported(t *testing.T) {
	w := newGroupWorld(t, "rungroup-kinds")
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: testZone(t, w.d, w.org), Kind: KindAdhoc,
		HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: TriggerManual, CreatedAt: time.Now()}
	if err := NewSQLStore(w.d).CreateJob(db.WithOrgScope(context.Background(), w.org), j, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'running' WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), j.ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(w.sink.snapshot()); n != 0 {
		t.Fatalf("an ad-hoc job was reported as a run group (%d)", n)
	}
}

func TestGroupSummariesAggregateStatusAndTiming(t *testing.T) {
	w := newGroupWorld(t, "rungroup-summary")
	failedGroup, _, fj := w.runGroup(t, 2)
	runningGroup, _, rj := w.runGroup(t, 2)
	queuedGroup, _, qj := w.runGroup(t, 1)
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'queued', started_at = NULL WHERE id = $1`, qj[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), fj[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), fj[1].ID, StatusError, nil, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), rj[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	got, err := NewSQLStore(w.d).GroupSummaries(db.WithOrgScope(context.Background(), w.org), w.org, []uuid.UUID{failedGroup, runningGroup, queuedGroup, uuid.Must(uuid.NewV7())})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("summaries = %d, want 3", len(got))
	}
	if g := got[failedGroup]; g.Status != StatusFailed || g.FirstJobID != fj[0].ID || g.FinishedAt == nil || g.Duration() <= 0 {
		t.Fatalf("failed group = %+v", g)
	}
	if g := got[runningGroup]; g.Status != StatusRunning || g.Duration() != 0 {
		t.Fatalf("running group = %+v", g)
	}
	if g := got[queuedGroup]; g.Status != StatusQueued || g.StartedAt != nil {
		t.Fatalf("queued group = %+v", g)
	}
}

// TestQueuedCancelRacingAFinishReportsTheGroupOnce is fix round 1's
// Important #1: CancelJob commits a queued job's terminal transition as a
// standalone statement, outside any group lock. Once that commit has
// landed, BOTH a concurrent member finish (through terminate, which locks
// and finds the group complete) and the cancel's own group check
// (ClearTerminalPayload -> recordCancelledRunGroup, which also locks and
// finds the group complete) independently believe they are the one
// completing the group — without a cross-path guard, both report.
func TestQueuedCancelRacingAFinishReportsTheGroupOnce(t *testing.T) {
	w := newGroupWorld(t, "rungroup-cancel-race")
	for round := 0; round < 5; round++ {
		_, _, js := w.runGroup(t, 2)
		if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'queued', started_at = NULL WHERE id = $1`, js[1].ID); err != nil {
			t.Fatal(err)
		}
		scoped := db.WithOrgScope(context.Background(), w.org)
		// The cancel's own status write commits here, before either the
		// finish or the cancel's own group check has run — the exact
		// unlocked-write race the fix closes.
		if status, err := NewSQLStore(w.d).CancelJob(scoped, w.org, js[1].ID); err != nil || status != StatusCancelled {
			t.Fatalf("cancel = %s, %v", status, err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded, nil, ""); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			w.q.ClearTerminalPayload(scoped, js[1].ID)
		}()
		wg.Wait()
		if n := len(w.sink.snapshot()); n != round+1 {
			t.Fatalf("round %d: reports = %d, want %d (a queued cancel racing a finish must report once)", round, n, round+1)
		}
	}
}

// dbErrorSink is a RunGroupSink whose write fails with a real Postgres
// error, executed through the finishing transaction itself (ctx carries
// it) — the shape the fixed savepoint must contain.
type dbErrorSink struct{ d *db.DB }

func (s *dbErrorSink) RunGroupFinished(ctx context.Context, _ RunGroupResult) error {
	_, err := s.d.Exec(ctx, `SELECT 1/0`)
	return err
}

// TestRunGroupSinkDatabaseErrorStillFinishesTheJob is fix round 1's
// Important #2: a sink error that comes from the database (not a plain Go
// error the fake in TestRunGroupSinkErrorDoesNotFailTheFinish returns)
// aborts the transaction unless the sink's own statement runs under a
// savepoint. Without that, the next statement in the same transaction
// (clearPayload) fails with "current transaction is aborted", the whole
// finish rolls back, and the job is stuck running.
func TestRunGroupSinkDatabaseErrorStillFinishesTheJob(t *testing.T) {
	w := newGroupWorld(t, "rungroup-sink-dberr")
	w.q.SetRunGroupSink(&dbErrorSink{d: w.d})
	group, tmpl := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	j := Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: testZone(t, w.d, w.org), Kind: KindRun, TemplateID: &tmpl,
		HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: StatusQueued, RunGroupID: group,
		Trigger: TriggerManual, CreatedAt: time.Now(),
	}
	scoped := db.WithOrgScope(context.Background(), w.org)
	if err := NewSQLStore(w.d).CreateJob(scoped, j, json.RawMessage(`"sealed-payload"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'running', started_at = now() WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), j.ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("Finish = %v, want nil although the sink's own statement failed", err)
	}
	var status string
	var payload []byte
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT status, payload FROM jobs WHERE id = $1`, j.ID).Scan(&status, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" {
		t.Fatalf("status = %s, want succeeded", status)
	}
	if payload != nil {
		t.Fatalf("payload = %s, want wiped despite the sink's own statement failing", payload)
	}
}

func TestCancelledRunGroupCheckIgnoresOtherJobs(t *testing.T) {
	w := newGroupWorld(t, "rungroup-cancel-kinds")
	scoped := db.WithOrgScope(context.Background(), w.org)
	// An unknown job is logged and ignored.
	w.q.recordCancelledRunGroup(scoped, uuid.Must(uuid.NewV7()))
	// A cancelled ad-hoc job is no run group.
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: testZone(t, w.d, w.org), Kind: KindAdhoc,
		HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: TriggerManual, CreatedAt: time.Now()}
	if err := NewSQLStore(w.d).CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSQLStore(w.d).CancelJob(scoped, w.org, j.ID); err != nil {
		t.Fatal(err)
	}
	w.q.ClearTerminalPayload(scoped, j.ID)
	// Without a sink nothing is checked at all.
	w.q.SetRunGroupSink(nil)
	_, _, js := w.runGroup(t, 1)
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'queued', started_at = NULL WHERE id = $1`, js[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSQLStore(w.d).CancelJob(scoped, w.org, js[0].ID); err != nil {
		t.Fatal(err)
	}
	w.q.ClearTerminalPayload(scoped, js[0].ID)
	if n := len(w.sink.snapshot()); n != 0 {
		t.Fatalf("reports = %d, want none", n)
	}
}

// failReportClaim installs a trigger that makes the report claim's own
// UPDATE raise, and removes it again when the test ends. It is the
// cheapest faithful stand-in for the final review's scenario — an operator
// setting a statement_timeout on the application role, which the group
// read's four count(DISTINCT ...) sub-selects then exceed: what matters is
// that a *Postgres* error happens on the report path inside the finishing
// transaction, not which of its two statements raised it. Both run under
// the same savepoint.
//
// The DDL goes through the bootstrap superuser connection, because
// pulsix_app deliberately does not own the jobs table.
func failReportClaim(t *testing.T) {
	t.Helper()
	admin, err := db.Open(context.Background(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Pool().Exec(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE OR REPLACE FUNCTION pulsix_test_fail_report_claim() RETURNS trigger
	      LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'report claim refused'; END $$`)
	exec(`CREATE TRIGGER pulsix_test_fail_report_claim BEFORE UPDATE ON jobs FOR EACH ROW
	      WHEN (NEW.run_group_reported AND NOT OLD.run_group_reported)
	      EXECUTE FUNCTION pulsix_test_fail_report_claim()`)
	t.Cleanup(func() {
		exec(`DROP TRIGGER IF EXISTS pulsix_test_fail_report_claim ON jobs`)
		exec(`DROP FUNCTION IF EXISTS pulsix_test_fail_report_claim()`)
	})
}

// TestRunGroupReportPathDatabaseErrorStillFinishesTheJob is the final
// review's Important #1. The group read and the report claim ran directly
// on the finishing transaction, with only the sink call itself under a
// savepoint. A Postgres error in either therefore aborted the transaction:
// clearPayload, the very next statement, failed with "current transaction
// is aborted", terminate rolled the whole finish back, and the job kept
// its non-terminal status — forever, because a retried Finish fails the
// identical way. The schedule's overlap guard then skipped every further
// occurrence of that schedule, so a notification-only code path stopped
// the automation product. The read's own comment claimed the opposite
// guarantee ("must not wedge the job in running"); this is that guarantee
// made true.
func TestRunGroupReportPathDatabaseErrorStillFinishesTheJob(t *testing.T) {
	w := newGroupWorld(t, "rungroup-report-dberr")
	failReportClaim(t)
	_, _, js := w.runGroup(t, 1)
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET payload = '"sealed-payload"' WHERE id = $1`, js[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("Finish = %v, want nil: a notification-only path must never wedge a job in running", err)
	}
	var status string
	var payload []byte
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT status, payload FROM jobs WHERE id = $1`, js[0].ID).Scan(&status, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" {
		t.Fatalf("status = %s, want succeeded although the report path raised", status)
	}
	if payload != nil {
		t.Fatalf("payload = %s, want wiped: the terminal transition must still commit", payload)
	}
	if got := w.sink.snapshot(); len(got) != 0 {
		t.Fatalf("reported %d group(s) although the claim never succeeded", len(got))
	}
}

// panickingSink panics instead of returning an error.
type panickingSink struct{}

func (panickingSink) RunGroupFinished(context.Context, RunGroupResult) error { panic("sink exploded") }

// TestRunGroupSinkPanicStillFinishesTheJob is the final review's
// Important #6, first hole. The sink call had no recover, and there is
// none above it either: the call path is the worker's Finish RPC and
// grpcsrv installs only the auth interceptors, while grpc-go does not
// recover handler panics itself. A panic inside the notification outbox
// therefore killed the whole manager — strictly worse than the wedge the
// function's own doc comment is written to prevent.
func TestRunGroupSinkPanicStillFinishesTheJob(t *testing.T) {
	w := newGroupWorld(t, "rungroup-sink-panic")
	w.q.SetRunGroupSink(panickingSink{})
	_, _, js := w.runGroup(t, 1)
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET payload = '"sealed-payload"' WHERE id = $1`, js[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("Finish = %v, want nil although the sink panicked", err)
	}
	var status string
	var payload []byte
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT status, payload FROM jobs WHERE id = $1`, js[0].ID).Scan(&status, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || payload != nil {
		t.Fatalf("status = %s, payload = %s; a panicking sink must be treated like a sink error", status, payload)
	}
}

// TestStatelessErrorJobsHostsAreNotCountedAsOK is backlog 2.81's central
// inaccuracy. Hosts sums cardinality(host_ids) over every job of the
// group, while HostsFailed only ever counted names the worker put in the
// stats lists — and a job that ends error or cancelled has no per-host
// stats at all (the worker never got as far as a recap). Its hosts
// therefore landed in neither failure list and HostsOK, being
// Hosts - HostsFailed, absorbed every one of them: a run group whose jobs
// all died at the worker rendered as "N hosts (ok N, changed 0, failed 0)"
// under a "failed" result. HostsNoResult counts them for what they are —
// hosts this run never reached a verdict on — and HostsOK no longer claims
// them.
func TestStatelessErrorJobsHostsAreNotCountedAsOK(t *testing.T) {
	w := newGroupWorld(t, "rungroup-noresult")
	_, _, js := w.runGroup(t, 2)
	if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded,
		map[string]any{"ok_hosts": []string{js[0].HostIDs[0].String()}}, ""); err != nil {
		t.Fatal(err)
	}
	// The worker died before any recap: a terminal status and no stats.
	if err := w.q.Finish(context.Background(), js[1].ID, StatusError, nil, "the worker went away"); err != nil {
		t.Fatal(err)
	}
	got := w.sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("reports = %d, want 1", len(got))
	}
	r := got[0]
	if r.Status != StatusFailed {
		t.Fatalf("group status = %q, want failed", r.Status)
	}
	if r.Hosts != 2 || r.HostsNoResult != 1 || r.HostsFailed != 0 || r.HostsOK() != 1 {
		t.Fatalf("hosts = %d, no result = %d, failed = %d, ok = %d; want 2/1/0/1 — the errored job's host is not an OK host",
			r.Hosts, r.HostsNoResult, r.HostsFailed, r.HostsOK())
	}
}

// TestStatsHostsOutsideTheJobsTargetsAreNotCounted pins the other half of
// backlog 2.81. The stats lists carry host UUIDs only where the claim's
// connection map named one; a host the claim does not name — an implicit
// localhost task — stays a bare name (internal/worker/runner/stats.go).
// Counting every distinct element of those lists therefore mixed two
// different universes with cardinality(host_ids): a failing localhost
// added one to HostsFailed and subtracted one from HostsOK, and enough of
// them could drive HostsFailed past the run's real host count entirely.
// RetryFailed already intersects the same lists against the job's own
// targets (failedHostIDs); the group's aggregate now does too.
func TestStatsHostsOutsideTheJobsTargetsAreNotCounted(t *testing.T) {
	w := newGroupWorld(t, "rungroup-stray")
	_, _, js := w.runGroup(t, 1)
	if err := w.q.Finish(context.Background(), js[0].ID, StatusFailed, map[string]any{
		"failed_hosts":  []string{js[0].HostIDs[0].String(), "localhost"},
		"changed_hosts": []string{"localhost", uuid.Must(uuid.NewV7()).String()},
	}, "a task failed"); err != nil {
		t.Fatal(err)
	}
	got := w.sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("reports = %d, want 1", len(got))
	}
	r := got[0]
	// changed names a bare localhost and a uuid belonging to no job of this
	// group; failed names the real host plus that same localhost.
	if r.Hosts != 1 || r.HostsFailed != 1 || r.HostsChanged != 0 || r.HostsNoResult != 0 || r.HostsOK() != 0 {
		t.Fatalf("hosts = %d, failed = %d, changed = %d, no result = %d, ok = %d; want 1/1/0/0/0",
			r.Hosts, r.HostsFailed, r.HostsChanged, r.HostsNoResult, r.HostsOK())
	}
}
