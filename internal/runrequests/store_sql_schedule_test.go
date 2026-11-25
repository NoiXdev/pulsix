package runrequests_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
)

// A schedule request keeps its change (numbers exact) and its target;
// its approval records the fingerprint but takes no run group, since it
// starts no run, so no job's claim ever finds it.
func TestSQLStoreScheduleRequest(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-sched")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	target := uuid.Must(uuid.NewV7())
	limit := "web*"
	r := runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindSchedule, TemplateID: f.template, TemplateRevision: 2,
		Reason: "nightly", RequestedByUser: f.user, Status: runrequests.StatusPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		TargetID: &target,
		Schedule: &runrequests.ScheduleChange{Action: runrequests.ScheduleCreate, AssistantLimit: true, AssistantExtraVars: true, Spec: &runrequests.ScheduleSpec{
			TemplateID: f.template, Name: "nightly", Mode: schedules.ModeCron, Cron: "0 3 * * *", Timezone: "UTC",
			Guards: schedules.Guards{Overlap: schedules.OverlapQueue}, Limit: &limit,
			ExtraVars: map[string]any{"build": json.Number("9007199254740993")},
		}},
	}
	if err := store.Insert(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(f.ctx, f.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != runrequests.KindSchedule || got.TargetID == nil || *got.TargetID != target || got.Schedule == nil || got.Overrides != nil {
		t.Fatalf("Get = %+v", got)
	}
	s := got.Schedule
	if s.Action != runrequests.ScheduleCreate || !s.AssistantLimit || !s.AssistantExtraVars || s.Spec.Name != "nightly" || *s.Spec.Limit != "web*" ||
		s.Spec.Guards.Overlap != schedules.OverlapQueue || s.Spec.ExtraVars["build"] != json.Number("9007199254740993") {
		t.Fatalf("schedule = %+v", s.Spec)
	}
	ok, err := store.Decide(f.ctx, f.org, r.ID, runrequests.StatusApproved, f.user, now, "fp", jobs.ApprovedRun{})
	if err != nil || !ok {
		t.Fatalf("Decide = %v %v", ok, err)
	}
	got, err = store.Get(f.ctx, f.org, r.ID)
	if err != nil || got.Status != runrequests.StatusApproved || got.ApprovedFingerprint != "fp" || got.RunGroupID != nil {
		t.Fatalf("approved = %+v %v", got, err)
	}
	n, err := store.CountPending(f.ctx, f.org, nil, now)
	if err != nil || n != 0 {
		t.Fatalf("pending after the approval = %d %v", n, err)
	}
}

// A request of another kind than run or retry must carry its payload.
func TestSQLStoreScheduleRequestNeedsItsPayload(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-sched-nopayload")
	now := time.Now().UTC()
	r := runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindSchedule, TemplateID: f.template,
		Reason: "x", RequestedByUser: f.user, Status: runrequests.StatusPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := runrequests.NewSQLStore(d).Insert(f.ctx, r); err == nil {
		t.Fatal("a schedule request without its payload was stored")
	}
}
