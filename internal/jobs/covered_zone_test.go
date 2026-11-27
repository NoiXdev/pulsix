package jobs_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// coveredZoneFixture is a two-zone template whose first keyed enqueue
// created zone 1's job and failed at zone 2's, as an approval's enqueue
// interrupted between two zones; then a host joined zone 1.
type coveredZoneFixture struct {
	svc          *jobs.Service
	store        *fakeStore
	org, tmpl    uuid.UUID
	zone1, zone2 uuid.UUID
	joined       uuid.UUID
}

func newCoveredZoneFixture(t *testing.T, req jobs.RunRequest, grow bool) coveredZoneFixture {
	t.Helper()
	f := coveredZoneFixture{store: newFakeStore(), org: uuid.Must(uuid.NewV7()), tmpl: uuid.Must(uuid.NewV7()),
		zone1: uuid.Must(uuid.NewV7()), zone2: uuid.Must(uuid.NewV7()), joined: uuid.Must(uuid.NewV7())}
	tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		f.tmpl: {ID: f.tmpl, OrgID: f.org, Scope: json.RawMessage(`{"group_id":null,"host_ids":[]}`)},
	}}
	first := uuid.Must(uuid.NewV7())
	res := &fakeResolver{targets: []jobs.ZoneTargets{
		{ZoneID: f.zone1, HostIDs: []uuid.UUID{first}},
		{ZoneID: f.zone2, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
	}}
	f.svc = newService(f.store, &fakeQueue{}, tmpl, res, &fakeHosts{})
	f.store.failCreateForZone = f.zone2
	if _, err := f.svc.EnqueueRunWith(ctx, f.org, f.tmpl, req); err == nil {
		t.Fatal("first attempt: want the simulated zone 2 failure")
	}
	f.store.failCreateForZone = uuid.Nil
	if grow {
		res.targets[0].HostIDs = []uuid.UUID{first, f.joined}
	}
	return f
}

// TestEnqueueRunWithRefusesGrowthOfACoveredZone is backlog 2.142: a run
// request approved again after its enqueue failed partway replays the
// first attempt's jobs. A host that joined a zone one of them covers would
// never run there; the replay refuses the whole call, naming the zone and
// the host, and creates no job, not even for the zone nothing covers.
func TestEnqueueRunWithRefusesGrowthOfACoveredZone(t *testing.T) {
	actor := uuid.Must(uuid.NewV7())
	req := jobs.RunRequest{Trigger: jobs.TriggerManual, Actor: &actor, IdempotencyKey: "run-request:x", RefuseCoveredZoneGrowth: true}
	f := newCoveredZoneFixture(t, req, true)

	created, err := f.svc.EnqueueRunWith(ctx, f.org, f.tmpl, req)
	var grew *jobs.CoveredZoneGrowthError
	if !errors.As(err, &grew) || !errors.Is(err, jobs.ErrCoveredZoneGrew) {
		t.Fatalf("replay = %v, want a CoveredZoneGrowthError", err)
	}
	if len(grew.Zones) != 1 || grew.Zones[0].ZoneID != f.zone1 || !slices.Equal(grew.Zones[0].HostIDs, []uuid.UUID{f.joined}) {
		t.Fatalf("grown zones = %+v, want zone 1 with the joined host", grew.Zones)
	}
	if len(created) != 0 || len(f.store.jobs) != 1 {
		t.Fatalf("replay created %d, stored %d; want nothing new beyond zone 1's first job", len(created), len(f.store.jobs))
	}
}

// TestEnqueueRunWithScheduleReplayIgnoresGrowth: replays of schedules,
// webhooks and follow-ups do not opt in; they fill in the missing zone as
// before (backlog 2.142 is about run request approvals only).
func TestEnqueueRunWithScheduleReplayIgnoresGrowth(t *testing.T) {
	ref := uuid.Must(uuid.NewV7())
	req := jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &ref, IdempotencyKey: "schedule:x"}
	f := newCoveredZoneFixture(t, req, true)

	created, err := f.svc.EnqueueRunWith(ctx, f.org, f.tmpl, req)
	if err != nil {
		t.Fatalf("replay = %v, want the missing zone filled in", err)
	}
	if len(created) != 2 || len(f.store.jobs) != 2 {
		t.Fatalf("replay returned %d, stored %d; want both zones", len(created), len(f.store.jobs))
	}
}

// TestEnqueueRunWithReplayWithoutGrowthFillsTheZone: a re-approval that
// adds no host to a covered zone behaves as before.
func TestEnqueueRunWithReplayWithoutGrowthFillsTheZone(t *testing.T) {
	actor := uuid.Must(uuid.NewV7())
	req := jobs.RunRequest{Trigger: jobs.TriggerManual, Actor: &actor, IdempotencyKey: "run-request:y", RefuseCoveredZoneGrowth: true}
	f := newCoveredZoneFixture(t, req, false)

	created, err := f.svc.EnqueueRunWith(ctx, f.org, f.tmpl, req)
	if err != nil {
		t.Fatalf("replay = %v, want the missing zone filled in", err)
	}
	if len(created) != 2 || len(f.store.jobs) != 2 {
		t.Fatalf("replay returned %d, stored %d; want both zones", len(created), len(f.store.jobs))
	}
}

// TestRetryReplayOfAnApprovedRetryRefusesGrowth: a retry an approved
// retry request keyed refuses a covered zone that grew on its replay, as
// a run request's run does (backlog 2.142).
func TestRetryReplayOfAnApprovedRetryRefusesGrowth(t *testing.T) {
	f := newChainFixture(t)
	orig := id7()
	f.store.jobs[orig] = storedJob{j: jobs.Job{
		ID: orig, OrgID: f.org, Kind: jobs.KindRun, Status: jobs.StatusFailed,
		TemplateID: &f.tpl, HostIDs: []uuid.UUID{f.h1, f.h2}, RunGroupID: id7(),
		Stats: map[string]any{jobs.StatsFailedHosts: []any{f.h1.String(), f.h2.String()}},
	}}
	group := id7()
	if _, err := f.svc.RetryFailedInto(ctx, f.org, orig, nil, "run-request:r", group, nil, jobs.ApprovedRun{}, nil); err != nil {
		t.Fatal(err)
	}
	f.res.targets = []jobs.ZoneTargets{{ZoneID: f.zone, HostIDs: []uuid.UUID{f.h1, f.h2, f.h3}}}
	_, err := f.svc.RetryFailedInto(ctx, f.org, orig, nil, "run-request:r", group, nil, jobs.ApprovedRun{}, nil)
	if !errors.Is(err, jobs.ErrCoveredZoneGrew) {
		t.Fatalf("replay of the approved retry = %v, want ErrCoveredZoneGrew", err)
	}
}
