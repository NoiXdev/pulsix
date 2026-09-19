package app

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// TestWorkerJobRequeuerAppliesTheAttemptLimit: the adapter hands the
// configured PULSIX_JOB_MAX_ATTEMPTS to the queue — with a limit of 2, a
// job claimed once goes back to the queue and one claimed twice ends.
func TestWorkerJobRequeuerAppliesTheAttemptLimit(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org, err := gen.New(d.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "app-revoke-org", Name: "app revoke"})
	if err != nil {
		t.Fatal(err)
	}
	zone := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'revoke', 'revoke')`, zone, org.ID); err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewSQLQueue(d, stubBox{}, stubSnapshot{}, stubCreds{}, stubTemplates{}, stubBastions{}, time.Now, rand.Reader)
	store := jobs.NewSQLStore(d)
	worker := uuid.Must(uuid.NewV7())
	for _, attempts := range []int{1, 2} {
		j := jobs.Job{
			ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone, Kind: jobs.KindAdhoc, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())},
			Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerManual, CreatedAt: time.Now(),
		}
		if err := store.CreateJob(db.WithOrgScope(ctx, org.ID), j, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool().Exec(ctx, `UPDATE jobs SET status = 'running', worker_id = $2, attempts = $3, claimed_at = now() WHERE id = $1`, j.ID, worker, attempts); err != nil {
			t.Fatal(err)
		}
	}
	requeued, ended, err := workerJobRequeuer{q: queue, maxAttempts: 2}.RequeueWorkerJobs(ctx, org.ID, worker, "worker revoked")
	if err != nil || requeued != 1 || ended != 1 {
		t.Fatalf("RequeueWorkerJobs = %d, %d, %v; want 1 requeued, 1 ended", requeued, ended, err)
	}
}
