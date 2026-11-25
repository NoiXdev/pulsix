package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// staticGuard refuses every run claim with err (nil lets it through).
type staticGuard struct{ err error }

func (g staticGuard) CheckRunClaim(context.Context, uuid.UUID, uuid.UUID, ApprovedRun) error {
	return g.err
}

// refuseAtClaim enqueues one run job of group in a zone of its own and has
// the claim guard refuse it with reason at its first claim, the real path
// a job of a withdrawn approval takes. It returns the job as stored. The
// job carries no template: the group world's queue has no template source,
// and the guard refuses before anything reads one.
func (w *groupWorld) refuseAtClaim(t *testing.T, group uuid.UUID, at time.Time, reason error) Job {
	t.Helper()
	j := Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: testZone(t, w.d, w.org), Kind: KindRun,
		HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: StatusQueued, RunGroupID: group, Trigger: TriggerManual, CreatedAt: at,
	}
	scoped := db.WithOrgScope(context.Background(), w.org)
	if err := NewSQLStore(w.d).CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	w.q.SetClaimGuard(staticGuard{err: reason})
	defer w.q.SetClaimGuard(nil)
	if c, err := w.q.Claim(context.Background(), w.org, []uuid.UUID{j.ZoneID}, "v1"); err != nil || c != nil {
		t.Fatalf("claim of the refused job = %v, %v; want an empty answer", c, err)
	}
	got, err := NewSQLStore(w.d).GetJob(scoped, w.org, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// runningJob inserts one running run job of group, created at at, in a zone
// of its own.
func (w *groupWorld) runningJob(t *testing.T, group uuid.UUID, at time.Time) Job {
	t.Helper()
	tmpl := uuid.Must(uuid.NewV7())
	j := Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: testZone(t, w.d, w.org), Kind: KindRun, TemplateID: &tmpl,
		HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: StatusQueued, RunGroupID: group, Trigger: TriggerManual, CreatedAt: at,
	}
	if err := NewSQLStore(w.d).CreateJob(db.WithOrgScope(context.Background(), w.org), j, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'running', started_at = now(), attempts = 1 WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	return j
}

func (w *groupWorld) summary(t *testing.T, group uuid.UUID) (GroupSummary, bool) {
	t.Helper()
	got, err := NewSQLStore(w.d).GroupSummaries(db.WithOrgScope(context.Background(), w.org), w.org, []uuid.UUID{group})
	if err != nil {
		t.Fatal(err)
	}
	g, ok := got[group]
	return g, ok
}

// reportedJobs lists the jobs of group that carry the finish report's claim.
func (w *groupWorld) reportedJobs(t *testing.T, group uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := w.d.Pool().Query(context.Background(), `SELECT id FROM jobs WHERE run_group_id = $1 AND run_group_reported ORDER BY id`, group)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// TestRunGroupReportsOnceWhenARefillFinishesAfterAWithdrawnJob is backlog
// 2.126's final review, Important 1: an approval enqueued zone A and failed
// before zone B, the request went back to pending, and the claim guard
// refused A's job. That job is not the group's outcome: the group is not
// finished by it, and once a re-approval refilled A and B into the same
// group, the group finishes when they do, reports once, with their outcome,
// and its first job is the refill's.
func TestRunGroupReportsOnceWhenARefillFinishesAfterAWithdrawnJob(t *testing.T) {
	w := newGroupWorld(t, "rungroup-withdrawn-refill")
	group, t0 := uuid.Must(uuid.NewV7()), time.Now()

	withdrawn := w.refuseAtClaim(t, group, t0, fmt.Errorf("check the run's request: %w", ErrApprovalWithdrawn))
	if withdrawn.Status != StatusError || withdrawn.Attempts != 1 || withdrawn.Error != WithdrawnAtClaimError {
		t.Fatalf("withdrawn job = %s attempts %d %q, want error attempts 1 %q", withdrawn.Status, withdrawn.Attempts, withdrawn.Error, WithdrawnAtClaimError)
	}
	if got := w.sink.snapshot(); len(got) != 0 {
		t.Fatalf("a group of only a withdrawn job was reported: %+v", got)
	}
	if g, ok := w.summary(t, group); ok {
		t.Fatalf("a group of only a withdrawn job has a summary: %+v", g)
	}

	refillA := w.runningJob(t, group, t0.Add(time.Second))
	refillB := w.runningJob(t, group, t0.Add(2*time.Second))
	if g, ok := w.summary(t, group); !ok || g.Status != StatusRunning || g.FirstJobID != refillA.ID {
		t.Fatalf("summary while the refill runs = %+v (%v), want running from %s", g, ok, refillA.ID)
	}
	for _, j := range []Job{refillA, refillB} {
		if err := w.q.Finish(context.Background(), j.ID, StatusSucceeded, map[string]any{"ok_hosts": []string{j.HostIDs[0].String()}}, ""); err != nil {
			t.Fatal(err)
		}
	}
	got := w.sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("reports = %d (%+v), want 1", len(got), got)
	}
	if r := got[0]; r.Status != StatusSucceeded || r.Jobs != 2 || r.Hosts != 2 || r.HostsOK() != 2 || r.HostsNoResult != 0 || r.FirstJobID != refillA.ID {
		t.Fatalf("result = %+v, want succeeded over the two refilled jobs from %s", r, refillA.ID)
	}
	if g, ok := w.summary(t, group); !ok || g.Status != StatusSucceeded || g.FirstJobID != refillA.ID {
		t.Fatalf("summary = %+v (%v), want succeeded from %s", g, ok, refillA.ID)
	}
	if ids := w.reportedJobs(t, group); len(ids) != 1 || ids[0] != refillA.ID {
		t.Fatalf("report claimed on %v, want the refill's first job %s only", ids, refillA.ID)
	}
	if n, err := w.q.SweepUnreportedRunGroups(context.Background(), time.Now().Add(-time.Hour), 20); err != nil || n != 0 {
		t.Fatalf("sweep after the report = %d, %v; want nothing", n, err)
	}
	if n := len(w.sink.snapshot()); n != 1 {
		t.Fatalf("reports after the sweep = %d, want 1", n)
	}
}

// TestRunGroupOfOnlyWithdrawnJobsIsNeverSwept: a group whose request is
// never approved again keeps only its withdrawn job. It is not a finished
// group, so the lost-report sweep must not pick it up — not once, and not
// on every pass at the head of its batch, where it would crowd out a group
// whose report really was lost.
func TestRunGroupOfOnlyWithdrawnJobsIsNeverSwept(t *testing.T) {
	w := newGroupWorld(t, "rungroup-withdrawn-sweep")
	withdrawnGroup := uuid.Must(uuid.NewV7())
	w.refuseAtClaim(t, withdrawnGroup, time.Now(), ErrApprovalWithdrawn)

	lost, _, js := w.runGroup(t, 1)
	if err := w.q.Finish(context.Background(), js[0].ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	w.sink.reset()
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET run_group_reported = false WHERE run_group_id = $1`, lost); err != nil {
		t.Fatal(err)
	}

	for pass := 0; pass < 2; pass++ {
		if _, err := w.q.SweepUnreportedRunGroups(context.Background(), time.Now().Add(-time.Hour), 1); err != nil {
			t.Fatal(err)
		}
	}
	got := w.sink.snapshot()
	if len(got) != 1 || got[0].GroupID != lost {
		t.Fatalf("swept reports = %+v, want the lost group %s once", got, lost)
	}
	if n, err := w.q.SweepUnreportedRunGroups(context.Background(), time.Now().Add(-time.Hour), 20); err != nil || n != 0 {
		t.Fatalf("a later sweep = %d, %v; want nothing (the withdrawn-only group is not finished)", n, err)
	}
	if g, ok := w.summary(t, withdrawnGroup); ok {
		t.Fatalf("the withdrawn-only group has a summary: %+v", g)
	}
}

// TestRunGroupCountsAJobRefusedForAnotherReason: a refusal that is not a
// withdrawn approval — here the run picture changed after it was approved —
// is an errored run of an approval that still stands: the group finishes
// with it and reports failed.
func TestRunGroupCountsAJobRefusedForAnotherReason(t *testing.T) {
	w := newGroupWorld(t, "rungroup-refused-changed")
	group := uuid.Must(uuid.NewV7())
	j := w.refuseAtClaim(t, group, time.Now(), errors.New("what the run uses changed after it was approved; ask for the run again"))
	if j.Status != StatusError || j.Error != "refused at claim: what the run uses changed after it was approved; ask for the run again" {
		t.Fatalf("refused job = %s %q", j.Status, j.Error)
	}
	got := w.sink.snapshot()
	if len(got) != 1 || got[0].Status != StatusFailed || got[0].Jobs != 1 || got[0].FirstJobID != j.ID {
		t.Fatalf("reports = %+v, want one failed report of the refused job", got)
	}
	if g, ok := w.summary(t, group); !ok || g.Status != StatusFailed {
		t.Fatalf("summary = %+v (%v), want failed", g, ok)
	}
}

// TestWithdrawnAtClaimSQLMatchesGo pins every query that leaves a job
// withdrawn at claim out of its group's outcome to the Go predicate the
// replay uses (withdrawnAtClaim): one terminal job per group, and each
// query must leave it out exactly when the predicate says so.
func TestWithdrawnAtClaimSQLMatchesGo(t *testing.T) {
	w := newGroupWorld(t, "rungroup-withdrawn-pin")
	cases := []struct {
		name     string
		status   Status
		attempts int
		errText  string
	}{
		{"withdrawn at its first claim", StatusError, 1, WithdrawnAtClaimError},
		{"withdrawn at a later claim", StatusError, 2, WithdrawnAtClaimError},
		{"refused for another reason", StatusError, 1, claimRefusedPrefix + "what the run uses changed after it was approved; ask for the run again"},
		{"the text with more after it", StatusError, 1, WithdrawnAtClaimError + " (again)"},
		{"the text later in another error", StatusError, 1, "worker lost: " + WithdrawnAtClaimError},
		{"failed, not errored", StatusFailed, 1, WithdrawnAtClaimError},
		{"errored without the text", StatusError, 1, "build payload: broken"},
		{"succeeded", StatusSucceeded, 1, ""},
	}
	scoped := db.WithOrgScope(context.Background(), w.org)
	groups := make([]uuid.UUID, len(cases))
	for i, tc := range cases {
		groups[i] = uuid.Must(uuid.NewV7())
		if _, err := w.d.Pool().Exec(context.Background(), `INSERT INTO jobs (id, org_id, zone_id, kind, host_ids, status, attempts, error, run_group_id, started_at, finished_at)
			VALUES ($1, $2, $3, 'run', '{}', $4, $5, $6, $7, now(), now())`,
			uuid.Must(uuid.NewV7()), w.org, testZone(t, w.d, w.org), string(tc.status), tc.attempts, tc.errText, groups[i]); err != nil {
			t.Fatal(err)
		}
	}
	unreported, err := gen.New(w.d.Pool()).ListUnreportedFinishedRunGroups(context.Background(), gen.ListUnreportedFinishedRunGroupsParams{
		WithdrawnError: WithdrawnAtClaimError, Since: time.Now().Add(-time.Hour), Lim: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := map[uuid.UUID]bool{}
	for _, r := range unreported {
		listed[r.RunGroupID] = true
	}
	summaries, err := NewSQLStore(w.d).GroupSummaries(scoped, w.org, groups)
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			group := groups[i]
			withdrawn := withdrawnAtClaim(Job{Status: tc.status, Attempts: tc.attempts, Error: tc.errText})
			if listed[group] == withdrawn {
				t.Errorf("ListUnreportedFinishedRunGroups lists it = %v, want %v", listed[group], !withdrawn)
			}
			if _, ok := summaries[group]; ok == withdrawn {
				t.Errorf("groupSummaryQuery summarises it = %v, want %v", ok, !withdrawn)
			}
			out, err := gen.New(w.d.Pool()).RunGroupOutcome(context.Background(), gen.RunGroupOutcomeParams{
				OrgID: w.org, RunGroupID: group, WithdrawnError: WithdrawnAtClaimError,
			})
			if err != nil {
				t.Fatal(err)
			}
			if counted := out.Jobs == 1; counted == withdrawn {
				t.Errorf("RunGroupOutcome counts it = %v, want %v", counted, !withdrawn)
			}
			_, won, err := w.q.readAndClaimRunGroup(context.Background(), RunGroupResult{}, w.org, group)
			if err != nil {
				t.Fatal(err)
			}
			if won == withdrawn {
				t.Errorf("runGroupQuery finishes the group = %v, want %v", won, !withdrawn)
			}
		})
	}
}
