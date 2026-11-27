package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// refuseGuard refuses every run claim with its reason and counts the calls.
type refuseGuard struct {
	reason error
	calls  int
	runs   []jobs.ApprovedRun
}

func (g *refuseGuard) CheckRunClaim(_ context.Context, _, _ uuid.UUID, run jobs.ApprovedRun) error {
	g.calls++
	g.runs = append(g.runs, run)
	return g.reason
}

// ConfirmWithdrawn confirms every withdrawn refusal: the guard's answer
// does not change.
func (g *refuseGuard) ConfirmWithdrawn(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

// TestQueueClaimGuardRefusalMarksTheJob: a run job the claim guard refuses
// as withdrawn fails with the fixed text jobs.WithdrawnAtClaimError, the
// mark a replay of its run group and the group's outcome read; the claim
// answers empty. A job failed for any other reason at claim (here an
// unbuildable payload) does not carry the refusal's prefix.
func TestQueueClaimGuardRefusalMarksTheJob(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-claim-guard-refusal")
	scoped := db.WithOrgScope(ctx, org)
	zone := seedZone(t, d, org)
	playbook, template := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := d.Exec(scoped, `INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, 'x', 'editor')`, playbook, org); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(scoped, `INSERT INTO job_templates (id, org_id, name, playbook_id, scope) VALUES ($1, $2, 'guarded', $3, '{}')`, template, org, playbook); err != nil {
		t.Fatal(err)
	}
	store := jobs.NewSQLStore(d)
	enqueue := func(t *testing.T) jobs.Job {
		t.Helper()
		j := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
		j.TemplateID = &template
		if err := store.CreateJob(scoped, j, nil); err != nil {
			t.Fatal(err)
		}
		return j
	}

	queue := newTestQueue(d)
	guard := &refuseGuard{reason: fmt.Errorf("look up the run: %w", jobs.ErrApprovalWithdrawn)}
	queue.SetClaimGuard(guard)
	refused := enqueue(t)
	claim, err := queue.Claim(ctx, org, []uuid.UUID{zone}, "v1")
	if err != nil || claim != nil {
		t.Fatalf("Claim = %v, %v; want an empty answer", claim, err)
	}
	got, err := store.GetJob(scoped, org, refused.ID)
	if err != nil {
		t.Fatal(err)
	}
	if guard.calls != 1 || got.Status != jobs.StatusError || got.Error != "refused at claim: the run's request is not approved" {
		t.Fatalf("refused job = %s %q after %d guard calls, want error %q", got.Status, got.Error, guard.calls,
			"refused at claim: the run's request is not approved")
	}

	// No guard objection, but the test queue's empty sources cannot build
	// a run's payload: the job fails without the refusal's mark.
	queue.SetClaimGuard(allowAllGuard{})
	broken := enqueue(t)
	if claim, err := queue.Claim(ctx, org, []uuid.UUID{zone}, "v1"); err != nil || claim != nil {
		t.Fatalf("Claim of the unbuildable job = %v, %v; want an empty answer", claim, err)
	}
	got, err = store.GetJob(scoped, org, broken.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != jobs.StatusError || got.Error == "" || strings.HasPrefix(got.Error, "refused at claim: ") {
		t.Fatalf("unbuildable job = %s %q, want an error without the refusal's mark", got.Status, got.Error)
	}
}

// TestQueueClaimGuardSeesTheJobsPin (spec 2026-11-25 A, B): the guard is
// handed the commit the claimed job is pinned to and the hosts it enrols
// agents on, which it compares with what its approval stored, and the
// hosts the job renders, which it compares with the approved picture
// (backlog 2.141).
func TestQueueClaimGuardSeesTheJobsPin(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-claim-guard-pin")
	scoped := db.WithOrgScope(ctx, org)
	zone := seedZone(t, d, org)
	worker := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx, `INSERT INTO workers (id, org_id, zone_id, name, cert_serial) VALUES ($1, $2, $3, 'w', 'serial-pin')`,
		worker, org, zone); err != nil {
		t.Fatal(err)
	}
	j := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	j.PinnedCommit = "0123456789abcdef0123456789abcdef01234567"
	j.EnrollHostIDs = []uuid.UUID{uuid.Must(uuid.NewV7())}
	if err := jobs.NewSQLStore(d).CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	queue := newTestQueue(d)
	guard := &refuseGuard{reason: jobs.ErrApprovalWithdrawn}
	queue.SetClaimGuard(guard)
	if c, err := queue.ClaimFor(ctx, worker, org, []uuid.UUID{zone}, "v1", jobs.ClaimCaps{SupportsPinnedCommit: true}); err != nil || c != nil {
		t.Fatalf("ClaimFor = %v, %v; want the guard's refusal", c, err)
	}
	if len(guard.runs) != 1 || guard.runs[0].PinnedCommit != j.PinnedCommit || !slices.Equal(guard.runs[0].EnrollHostIDs, j.EnrollHostIDs) ||
		len(j.HostIDs) == 0 || !slices.Equal(guard.runs[0].HostIDs, j.HostIDs) {
		t.Fatalf("guard saw %+v, want [{%s %v %v}]", guard.runs, j.PinnedCommit, j.EnrollHostIDs, j.HostIDs)
	}
}

// TestQueueClaimGuardSeesTheChainParent (backlog 2.143, review B1): the
// guard is handed a chained job's parent run group (its trigger_ref) and
// its template, which find the follow-up row by its unique key even
// before the starter's batch names the row's child group. A job of
// another trigger hands no parent.
func TestQueueClaimGuardSeesTheChainParent(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-claim-guard-chain")
	scoped := db.WithOrgScope(ctx, org)
	zone := seedZone(t, d, org)
	worker := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx, `INSERT INTO workers (id, org_id, zone_id, name, cert_serial) VALUES ($1, $2, $3, 'w', 'serial-chain')`,
		worker, org, zone); err != nil {
		t.Fatal(err)
	}
	parent, sched := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	chained := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	chained.Trigger, chained.TriggerRef, chained.ActorID = jobs.TriggerChain, &parent, nil
	scheduled := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	scheduled.Trigger, scheduled.TriggerRef, scheduled.ActorID = jobs.TriggerSchedule, &sched, nil
	scheduled.CreatedAt = chained.CreatedAt.Add(time.Second)
	store := jobs.NewSQLStore(d)
	for _, j := range []jobs.Job{chained, scheduled} {
		if err := store.CreateJob(scoped, j, nil); err != nil {
			t.Fatal(err)
		}
	}
	queue := newTestQueue(d)
	guard := &refuseGuard{reason: errors.New("refused")}
	queue.SetClaimGuard(guard)
	for range 2 {
		if c, err := queue.ClaimFor(ctx, worker, org, []uuid.UUID{zone}, "v1", jobs.ClaimCaps{SupportsPinnedCommit: true}); err != nil || c != nil {
			t.Fatalf("ClaimFor = %v, %v; want the guard's refusal", c, err)
		}
	}
	if len(guard.runs) != 2 {
		t.Fatalf("guard calls = %d, want 2", len(guard.runs))
	}
	got := guard.runs[0]
	if got.TriggerRef == nil || *got.TriggerRef != parent || got.TemplateID == nil || *got.TemplateID != *chained.TemplateID {
		t.Fatalf("guard saw %+v for the chained job, want parent %s and template %s", got, parent, *chained.TemplateID)
	}
	if got := guard.runs[1]; got.TriggerRef != nil || got.TemplateID == nil || *got.TemplateID != *scheduled.TemplateID {
		t.Fatalf("guard saw %+v for the scheduled job, want no parent and its template", got)
	}
}
