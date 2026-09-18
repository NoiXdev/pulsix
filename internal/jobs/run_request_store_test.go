package jobs_test

import (
	"encoding/json"
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
