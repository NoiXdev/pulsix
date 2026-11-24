package jobs_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

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
}

func (g *refuseGuard) CheckRunClaim(context.Context, uuid.UUID, uuid.UUID) error {
	g.calls++
	return g.reason
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
