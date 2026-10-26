package jobs_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

func TestHasActiveTemplateJobAndRepoSync(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-active")
	zone := seedZone(t, d, org)
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	templateID := uuid.Must(uuid.NewV7())

	if active, err := store.HasActiveTemplateJob(scoped, org, templateID); err != nil || active {
		t.Fatalf("empty queue: %v, %v", active, err)
	}
	ref := uuid.Must(uuid.NewV7())
	j := jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: jobs.KindRun, TemplateID: &templateID,
		HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()),
		Trigger: jobs.TriggerSchedule, TriggerRef: &ref}
	if err := store.CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	if active, err := store.HasActiveTemplateJob(scoped, org, templateID); err != nil || !active {
		t.Fatalf("queued run: %v, %v", active, err)
	}
	got, err := store.GetJob(scoped, org, j.ID)
	if err != nil || got.TriggerRef == nil || *got.TriggerRef != ref || got.Trigger != jobs.TriggerSchedule {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	// The CHECK refuses an unknown trigger kind.
	bad := j
	bad.ID, bad.Trigger = uuid.Must(uuid.NewV7()), "cron"
	if err := store.CreateJob(scoped, bad, nil); err == nil {
		t.Fatal("jobs_trigger_check accepted an unknown trigger")
	}
	repo := uuid.Must(uuid.NewV7())
	sync := jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: jobs.KindSyncRepo, HostIDs: []uuid.UUID{},
		Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerWebhook}
	if err := store.CreateJob(scoped, sync, json.RawMessage(`{"repo_id":"`+repo.String()+`"}`)); err != nil {
		t.Fatal(err)
	}
	if active, err := store.HasActiveRepoSync(scoped, org, repo); err != nil || !active {
		t.Fatalf("queued sync: %v, %v", active, err)
	}
	if active, _ := store.HasActiveRepoSync(scoped, org, uuid.Must(uuid.NewV7())); active {
		t.Fatal("another repo's sync counted")
	}
}

// TestActiveRepoSyncIndexServesItsQuery is backlog 2.68 (T4). Two things
// can silently break an index over an expression, and neither shows up as
// an error: the key inside payload can be spelled differently from the
// query's, and the partial predicate can drift from the query's own. In
// both cases the schema still looks right and Postgres quietly scans.
//
// The planner's UNFORCED choice is not the assertion. On a handful of rows
// a real planner rightly prefers a sequential scan whatever indexes exist,
// so asserting on the chosen plan would test the cost estimator on toy
// data — the trap schedule_runs_org_due_idx's own test names. Turning
// seqscan off asks the question this test actually has: can this index
// serve this query at all? If org_id or the partial predicate did not line
// up, the only plan left would still be a scan.
//
// The expression needs its own check, because forcing the index does not
// prove the expression was used: with org_id leading, the index is chosen
// for org_id alone and a mis-spelled key would still produce this plan.
// Under row-level security the expression cannot be an Index Cond in any
// case — `->>` is not leakproof, so it may not run before the policy
// predicate — so it is pinned where it is decidable, in the index's own
// definition.
func TestActiveRepoSyncIndexServesItsQuery(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-repo-sync-idx")

	// The statement sqlc generates for HasActiveRepoSync, verbatim: a copy
	// that drifted from the real one would make this test pass about
	// nothing.
	const query = `SELECT EXISTS (
  SELECT 1 FROM jobs
  WHERE org_id = $1 AND kind = 'sync_repo'
    AND payload->>'repo_id' = $2::text
    AND status NOT IN ('succeeded', 'failed', 'cancelled', 'error')
)`

	tx, err := d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only probe
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+query, org, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	var plan string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "Index Scan using jobs_active_repo_sync_idx") {
		t.Fatalf("the guard's query cannot use its index (migration 00071); plan was:\n%s", plan)
	}

	var def string
	if err := tx.QueryRow(ctx,
		`SELECT indexdef FROM pg_indexes WHERE tablename = 'jobs' AND indexname = 'jobs_active_repo_sync_idx'`,
	).Scan(&def); err != nil {
		t.Fatalf("jobs_active_repo_sync_idx missing (migration 00071): %v", err)
	}
	for _, want := range []string{"payload ->> 'repo_id'::text", "kind = 'sync_repo'::text"} {
		if !strings.Contains(def, want) {
			t.Fatalf("index does not cover %q — the query would fall back to a filter over every active sync:\n%s", want, def)
		}
	}
}

// TestCreateJobRefusesAJobWithoutATrigger is the other half of backlog
// 2.68's T4. createJobParams used to substitute "manual" for an empty
// trigger, which existed only so integration-test fixtures could leave it
// out — and the substitution is not neutral: the trigger is how a run is
// ATTRIBUTED, so calling an unattributed job manual writes a claim about
// who started it into the run list and the audit trail. The store refuses
// it now, with an error that names the job rather than a check
// constraint, and the fixtures say what they mean.
func TestCreateJobRefusesAJobWithoutATrigger(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-no-trigger")
	zone := seedZone(t, d, org)
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	j := jobs.Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: jobs.KindRun,
		Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()),
	}
	err := store.CreateJob(scoped, j, nil)
	if !errors.Is(err, jobs.ErrInvalidInput) {
		t.Fatalf("CreateJob = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), j.ID.String()) {
		t.Fatalf("error must name the job so the caller can find it: %v", err)
	}

	// The group insert refuses it too, and before anything is written: the
	// two paths are separate call sites, so a guard on one of them only
	// moves the problem.
	good := j
	good.ID, good.Trigger = uuid.Must(uuid.NewV7()), jobs.TriggerManual
	if err := store.CreateJobGroup(ctx, org, []jobs.Job{good, j},
		map[uuid.UUID]json.RawMessage{good.ID: nil, j.ID: nil}); !errors.Is(err, jobs.ErrInvalidInput) {
		t.Fatalf("CreateJobGroup = %v, want ErrInvalidInput", err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM jobs WHERE id = $1`, good.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the group's good job was written despite the refusal (%d rows)", n)
	}
}
