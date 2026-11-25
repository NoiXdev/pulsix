package runrequests_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/triggers"
)

// A trigger request of a repository sync has no template: it is stored
// without one and keeps its change and target.
func TestSQLStoreRepoSyncTriggerRequestHasNoTemplate(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-trig")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	target, repo := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	r := runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindTrigger,
		Reason: "push hook", RequestedByUser: f.user, Status: runrequests.StatusPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		TargetID: &target,
		Trigger: &runrequests.TriggerChange{Action: runrequests.TriggerCreate, Spec: &runrequests.TriggerSpec{
			Kind: triggers.KindRepoSync, RepoID: &repo, Name: "push", Enabled: true, Preset: triggers.PresetGitHub,
			AllowCIDRs: []string{"192.0.2.0/24"}, OnActive: triggers.OnActiveSkip, RateLimit: triggers.RateLimit{Count: 5, Per: "1m"},
		}},
	}
	if err := store.Insert(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(f.ctx, f.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != runrequests.KindTrigger || got.TemplateID != uuid.Nil || got.TargetID == nil || *got.TargetID != target || got.Trigger == nil || got.Schedule != nil {
		t.Fatalf("Get = %+v", got)
	}
	s := got.Trigger.Spec
	if got.Trigger.Action != runrequests.TriggerCreate || s.Kind != triggers.KindRepoSync || s.RepoID == nil || *s.RepoID != repo ||
		s.Preset != triggers.PresetGitHub || len(s.AllowCIDRs) != 1 || s.OnActive != triggers.OnActiveSkip || s.RateLimit.Count != 5 {
		t.Fatalf("trigger = %+v", s)
	}
	list, err := store.List(f.ctx, f.org, nil, now)
	if err != nil || len(list) != 1 || list[0].TemplateID != uuid.Nil {
		t.Fatalf("List = %+v %v", list, err)
	}
}

// Only a trigger request goes without a template: a run, a retry or a
// schedule request without one is refused by the table.
func TestSQLStoreOtherKindsNeedATemplate(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-trig-notmpl")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC()
	for _, kind := range []runrequests.Kind{runrequests.KindRun, runrequests.KindSchedule} {
		r := runrequests.Request{
			ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: kind, Reason: "x", RequestedByUser: f.user,
			Status: runrequests.StatusPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		}
		if kind == runrequests.KindSchedule {
			r.Schedule = &runrequests.ScheduleChange{Action: runrequests.ScheduleResume}
		}
		if err := store.Insert(f.ctx, r); err == nil {
			t.Errorf("a %s request without a template was stored", kind)
		}
	}
}
