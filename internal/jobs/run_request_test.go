package jobs_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

func runFixture(t *testing.T) (*jobs.Service, *fakeStore, uuid.UUID, uuid.UUID) {
	t.Helper()
	store := newFakeStore()
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		templateID: {ID: templateID, OrgID: org, Scope: json.RawMessage(`{"group_id":null,"host_ids":[]}`)},
	}}
	res := &fakeResolver{targets: []jobs.ZoneTargets{
		{ZoneID: uuid.Must(uuid.NewV7()), HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
		{ZoneID: uuid.Must(uuid.NewV7()), HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
	}}
	return newService(store, &fakeQueue{}, tmpl, res, &fakeHosts{}), store, org, templateID
}

func TestEnqueueRunWithRecordsTriggerAndSealsOverrides(t *testing.T) {
	svc, store, org, templateID := runFixture(t)
	scheduleID := uuid.Must(uuid.NewV7())
	limit := "web*"
	created, err := svc.EnqueueRunWith(ctx, org, templateID, jobs.RunRequest{
		Trigger: jobs.TriggerSchedule, TriggerRef: &scheduleID,
		Overrides: &jobs.RunOverrides{Limit: &limit, ExtraVars: map[string]any{"token": "s3cret-override"}},
	})
	if err != nil {
		t.Fatalf("EnqueueRunWith: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("jobs = %d", len(created))
	}
	for _, j := range created {
		if j.Trigger != jobs.TriggerSchedule || j.TriggerRef == nil || *j.TriggerRef != scheduleID || j.ActorID != nil {
			t.Fatalf("job = %+v", j)
		}
		payload := string(store.jobs[j.ID].payload)
		if strings.Contains(payload, "s3cret-override") {
			t.Fatalf("payload stores the override in the clear: %s", payload)
		}
		// fakeSealer marks sealed bytes; the stored form is a JSON string.
		var enc string
		if err := json.Unmarshal(store.jobs[j.ID].payload, &enc); err != nil || enc == "" {
			t.Fatalf("payload %s is not the sealed-string form: %v", payload, err)
		}
	}
}

func TestEnqueueRunWithoutOverridesStoresNoPayload(t *testing.T) {
	svc, store, org, templateID := runFixture(t)
	trig := uuid.Must(uuid.NewV7())
	created, err := svc.EnqueueRunWith(ctx, org, templateID, jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &trig, Overrides: &jobs.RunOverrides{}})
	if err != nil {
		t.Fatal(err)
	}
	if p := store.jobs[created[0].ID].payload; p != nil {
		t.Fatalf("payload = %s, want none for empty overrides", p)
	}
}

func TestEnqueueRunWithRejectsUnknownTrigger(t *testing.T) {
	svc, _, org, templateID := runFixture(t)
	for _, trig := range []string{"", "cron", "MANUAL"} {
		if _, err := svc.EnqueueRunWith(ctx, org, templateID, jobs.RunRequest{Trigger: trig}); !errors.Is(err, jobs.ErrInvalidInput) {
			t.Errorf("trigger %q: %v", trig, err)
		}
	}
}

// TestEnqueueRunWithResolvesTheEffectiveLimit pins the limit string the
// scope resolution gates zones on (backlog 2.45): a run request's own limit
// replaces the template's — the exact precedence the claim applies when it
// hands the limit to ansible — while RunTargets (the maintenance guard's
// look-ahead) never gates and always resolves without one.
func TestEnqueueRunWithResolvesTheEffectiveLimit(t *testing.T) {
	const tmplLimit = "template-limit"
	ovLimit := "override-limit"
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	scope := json.RawMessage(`{"group_id":null,"host_ids":[]}`)
	targets := []jobs.ZoneTargets{
		{ZoneID: uuid.Must(uuid.NewV7()), HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
	}

	setup := func() (*fakeResolver, *jobs.Service) {
		res := &fakeResolver{targets: targets}
		tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
			templateID: {ID: templateID, OrgID: org, Scope: scope, Limit: tmplLimit},
		}}
		return res, newService(newFakeStore(), &fakeQueue{}, tmpl, res, &fakeHosts{})
	}

	t.Run("template limit", func(t *testing.T) {
		res, svc := setup()
		if _, err := svc.EnqueueRun(ctx, org, templateID, nil); err != nil {
			t.Fatalf("EnqueueRun: %v", err)
		}
		if len(res.limits) != 1 || res.limits[0] != tmplLimit {
			t.Fatalf("resolver limits = %v, want [%q]", res.limits, tmplLimit)
		}
	})

	t.Run("override limit wins", func(t *testing.T) {
		res, svc := setup()
		ref := uuid.Must(uuid.NewV7())
		if _, err := svc.EnqueueRunWith(ctx, org, templateID, jobs.RunRequest{
			Trigger: jobs.TriggerWebhook, TriggerRef: &ref,
			Overrides: &jobs.RunOverrides{Limit: &ovLimit},
		}); err != nil {
			t.Fatalf("EnqueueRunWith: %v", err)
		}
		if len(res.limits) != 1 || res.limits[0] != ovLimit {
			t.Fatalf("resolver limits = %v, want [%q]", res.limits, ovLimit)
		}
	})

	t.Run("override without limit keeps the template's", func(t *testing.T) {
		res, svc := setup()
		ref := uuid.Must(uuid.NewV7())
		if _, err := svc.EnqueueRunWith(ctx, org, templateID, jobs.RunRequest{
			Trigger: jobs.TriggerWebhook, TriggerRef: &ref, Overrides: &jobs.RunOverrides{},
		}); err != nil {
			t.Fatalf("EnqueueRunWith: %v", err)
		}
		if len(res.limits) != 1 || res.limits[0] != tmplLimit {
			t.Fatalf("resolver limits = %v, want [%q]", res.limits, tmplLimit)
		}
	})

	t.Run("RunTargets never gates", func(t *testing.T) {
		res, svc := setup()
		if _, err := svc.RunTargets(ctx, org, templateID); err != nil {
			t.Fatalf("RunTargets: %v", err)
		}
		if len(res.limits) != 1 || res.limits[0] != "" {
			t.Fatalf("resolver limits = %v, want [\"\"]", res.limits)
		}
	})
}

func TestEnqueueRunKeepsManualAttribution(t *testing.T) {
	svc, _, org, templateID := runFixture(t)
	actor := uuid.Must(uuid.NewV7())
	created, err := svc.EnqueueRun(ctx, org, templateID, &actor)
	if err != nil {
		t.Fatal(err)
	}
	if created[0].Trigger != jobs.TriggerManual || created[0].TriggerRef != nil || *created[0].ActorID != actor {
		t.Fatalf("job = %+v", created[0])
	}
}

func TestEnqueueSyncWithRecordsTheWebhook(t *testing.T) {
	store := newFakeStore()
	org, repo, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	zones := &fakeZones{}
	zones.add(org, zone, "default")
	svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{}, zones)
	trig := uuid.Must(uuid.NewV7())
	j, err := svc.EnqueueSyncWith(ctx, org, repo, jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &trig})
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != jobs.KindSyncRepo || j.Trigger != jobs.TriggerWebhook || *j.TriggerRef != trig {
		t.Fatalf("job = %+v", j)
	}
	if _, err := svc.EnqueueSyncWith(ctx, org, repo, jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &trig, Overrides: &jobs.RunOverrides{ExtraVars: map[string]any{"a": 1}}}); !errors.Is(err, jobs.ErrInvalidInput) {
		t.Fatalf("a sync takes no overrides: %v", err)
	}
}

func TestHasActiveDelegatesToTheStore(t *testing.T) {
	svc, store, org, templateID := runFixture(t)
	store.activeTemplates = map[uuid.UUID]bool{templateID: true}
	if active, err := svc.HasActiveRun(ctx, org, templateID); err != nil || !active {
		t.Fatalf("HasActiveRun = %v, %v", active, err)
	}
	repo := uuid.Must(uuid.NewV7())
	if active, err := svc.HasActiveSync(ctx, org, repo); err != nil || active {
		t.Fatalf("HasActiveSync = %v, %v", active, err)
	}
}

func TestHasActivePropagatesStoreError(t *testing.T) {
	svc, store, org, templateID := runFixture(t)
	store.activeErr = errors.New("db unavailable")
	if _, err := svc.HasActiveRun(ctx, org, templateID); err == nil {
		t.Fatal("HasActiveRun: want the store error, got nil")
	}
	if _, err := svc.HasActiveSync(ctx, org, uuid.Must(uuid.NewV7())); err == nil {
		t.Fatal("HasActiveSync: want the store error, got nil")
	}
}

func TestEnqueueRunWithPropagatesTemplateLookupError(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	wantErr := errors.New("template lookup exploded")
	svc := newService(newFakeStore(), &fakeQueue{}, &fakeTemplates{err: wantErr}, &fakeResolver{}, &fakeHosts{})
	trig := uuid.Must(uuid.NewV7())
	if _, err := svc.EnqueueRunWith(ctx, org, templateID, jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &trig}); !errors.Is(err, wantErr) {
		t.Fatalf("EnqueueRunWith error = %v, want %v", err, wantErr)
	}
}

// TestEnqueueRunWithIsIdempotentPerKey is backlog 2.60's load-bearing
// guarantee: the schedule ticker's write-back (due_sql.go's processRow)
// commits its enqueue in a separate transaction from the schedule_runs row
// and next_run_at advance it then writes. A failure between those two steps
// leaves the occurrence due, and the next tick decides it again — decide.go
// calls EnqueueRunWith a second time with the SAME IdempotencyKey (the
// schedule and the due instant are unchanged, since the write-back never
// advanced either). Without the reservation this adds, that second call
// would create a second run group for the same occurrence — a duplicate
// playbook run against the customer's servers. With it, the retry is a
// no-op that hands back the first attempt's own jobs.
func TestEnqueueRunWithIsIdempotentPerKey(t *testing.T) {
	svc, store, org, templateID := runFixture(t)
	scheduleID := uuid.Must(uuid.NewV7())
	key := "schedule:" + scheduleID.String() + ":2026-09-18T10:00:00Z"
	req := jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &scheduleID, IdempotencyKey: key}

	first, err := svc.EnqueueRunWith(ctx, org, templateID, req)
	if err != nil {
		t.Fatalf("first EnqueueRunWith: %v", err)
	}
	if len(first) != 2 || len(store.jobs) != 2 {
		t.Fatalf("after the first call: created = %d, stored = %d, want 2 and 2 (one job per zone)", len(first), len(store.jobs))
	}

	second, err := svc.EnqueueRunWith(ctx, org, templateID, req)
	if err != nil {
		t.Fatalf("retried EnqueueRunWith: %v", err)
	}
	if len(store.jobs) != 2 {
		t.Fatalf("stored jobs after the retry = %d, want still 2 — the retry must create nothing", len(store.jobs))
	}
	if len(second) != len(first) {
		t.Fatalf("replayed jobs = %d, want %d (the first attempt's own)", len(second), len(first))
	}
	for _, j := range second {
		if j.RunGroupID != first[0].RunGroupID {
			t.Fatalf("replayed job's group = %s, want %s (the first attempt's)", j.RunGroupID, first[0].RunGroupID)
		}
	}

	// A different occurrence of the same schedule — a different due instant
	// — must still get its own group: the key is per occurrence, not per
	// schedule, so it must not collapse every run of a schedule into one.
	key2 := "schedule:" + scheduleID.String() + ":2026-09-18T11:00:00Z"
	third, err := svc.EnqueueRunWith(ctx, org, templateID, jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &scheduleID, IdempotencyKey: key2})
	if err != nil {
		t.Fatalf("different-occurrence EnqueueRunWith: %v", err)
	}
	if len(store.jobs) != 4 {
		t.Fatalf("stored jobs after a different occurrence = %d, want 4", len(store.jobs))
	}
	if third[0].RunGroupID == first[0].RunGroupID {
		t.Fatal("a different occurrence reused the first one's run group")
	}
}

// TestEnqueueRunWithoutAKeyIsNeverDeduped is the control: RunNow, manual runs,
// webhook deliveries and retries leave IdempotencyKey empty on purpose (each
// click or delivery is deliberately its own occurrence), so two identical
// requests must each get their own run group.
func TestEnqueueRunWithoutAKeyIsNeverDeduped(t *testing.T) {
	svc, store, org, templateID := runFixture(t)
	ref := uuid.Must(uuid.NewV7())
	req := jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &ref}

	first, err := svc.EnqueueRunWith(ctx, org, templateID, req)
	if err != nil {
		t.Fatalf("first EnqueueRunWith: %v", err)
	}
	second, err := svc.EnqueueRunWith(ctx, org, templateID, req)
	if err != nil {
		t.Fatalf("second EnqueueRunWith: %v", err)
	}
	if len(store.jobs) != 4 {
		t.Fatalf("stored jobs = %d, want 4 (two independent calls, two groups)", len(store.jobs))
	}
	if second[0].RunGroupID == first[0].RunGroupID {
		t.Fatal("two unkeyed calls must never share a run group")
	}
}

// TestEnqueueRunWithRetryFillsInZonesAPartialAttemptMissed covers the case
// in between TestEnqueueRunWithIsIdempotentPerKey's full replay and
// TestEnqueueRunWithRetriesIntoAnAlreadyReservedEmptyGroup's empty one: the
// first attempt's multi-zone enqueue is interrupted after creating zone1's
// job but before zone2's (a manager shutdown mid-loop is the realistic
// cause). The retry must create zone2's missing job — reusing the same
// group — not treat "zone1 already has a job" as "this occurrence is done"
// and silently leave zone2 un-enqueued forever, and not create a second job
// for zone1 either.
func TestEnqueueRunWithRetryFillsInZonesAPartialAttemptMissed(t *testing.T) {
	store := newFakeStore()
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		templateID: {ID: templateID, OrgID: org, Scope: json.RawMessage(`{"group_id":null,"host_ids":[]}`)},
	}}
	zone1, zone2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	res := &fakeResolver{targets: []jobs.ZoneTargets{
		{ZoneID: zone1, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
		{ZoneID: zone2, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
	}}
	svc := newService(store, &fakeQueue{}, tmpl, res, &fakeHosts{})
	scheduleID := uuid.Must(uuid.NewV7())
	key := "schedule:" + scheduleID.String() + ":2026-09-18T10:00:00Z"
	req := jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &scheduleID, IdempotencyKey: key}

	store.failCreateForZone = zone2
	if _, err := svc.EnqueueRunWith(ctx, org, templateID, req); err == nil {
		t.Fatal("first attempt: want the simulated zone2 failure to propagate, got nil")
	}
	if len(store.jobs) != 1 {
		t.Fatalf("stored jobs after the interrupted first attempt = %d, want 1 (zone1 only)", len(store.jobs))
	}

	store.failCreateForZone = uuid.Nil
	created, err := svc.EnqueueRunWith(ctx, org, templateID, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(store.jobs) != 2 {
		t.Fatalf("stored jobs after the retry = %d, want 2 (zone1's survives, zone2's now created)", len(store.jobs))
	}
	if len(created) != 2 {
		t.Fatalf("retry returned %d jobs, want 2 (both zones, not just the one it created this time)", len(created))
	}
	zones := map[uuid.UUID]bool{}
	group := uuid.Nil
	for _, j := range created {
		zones[j.ZoneID] = true
		if group == uuid.Nil {
			group = j.RunGroupID
		} else if j.RunGroupID != group {
			t.Fatalf("job %s has group %s, want every job of this occurrence sharing one group %s", j.ID, j.RunGroupID, group)
		}
	}
	if !zones[zone1] || !zones[zone2] {
		t.Fatalf("zones covered = %v, want both %s and %s", zones, zone1, zone2)
	}
}

// TestEnqueueRunWithRetriesIntoAnAlreadyReservedEmptyGroup covers the other
// half of the reservation: the first attempt claims the key but fails before
// creating any job at all (an empty scope here — a dead template lookup or a
// transient store error would do the same). The claim must not wedge the
// key shut: a retry with the scope now resolving has to actually enqueue
// into the group the first attempt reserved, not silently report success
// with nothing created.
func TestEnqueueRunWithRetriesIntoAnAlreadyReservedEmptyGroup(t *testing.T) {
	store := newFakeStore()
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		templateID: {ID: templateID, OrgID: org, Scope: json.RawMessage(`{"group_id":null,"host_ids":[]}`)},
	}}
	res := &fakeResolver{}
	svc := newService(store, &fakeQueue{}, tmpl, res, &fakeHosts{})
	scheduleID := uuid.Must(uuid.NewV7())
	key := "schedule:" + scheduleID.String() + ":2026-09-18T10:00:00Z"
	req := jobs.RunRequest{Trigger: jobs.TriggerSchedule, TriggerRef: &scheduleID, IdempotencyKey: key}

	if _, err := svc.EnqueueRunWith(ctx, org, templateID, req); !errors.Is(err, jobs.ErrEmptyScope) {
		t.Fatalf("first attempt = %v, want ErrEmptyScope", err)
	}
	if len(store.jobs) != 0 {
		t.Fatalf("stored jobs after the failed attempt = %d, want 0", len(store.jobs))
	}

	res.targets = []jobs.ZoneTargets{{ZoneID: uuid.Must(uuid.NewV7()), HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}}}
	created, err := svc.EnqueueRunWith(ctx, org, templateID, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(created) != 1 || len(store.jobs) != 1 {
		t.Fatalf("retry created = %d, stored = %d, want 1 and 1", len(created), len(store.jobs))
	}
}

func TestRunRequestValidateRejectsMismatchedTriggerRef(t *testing.T) {
	svc, _, org, templateID := runFixture(t)
	ref := uuid.Must(uuid.NewV7())
	cases := []jobs.RunRequest{
		{Trigger: jobs.TriggerSchedule},                 // schedule without a ref
		{Trigger: jobs.TriggerWebhook},                  // webhook without a ref
		{Trigger: jobs.TriggerManual, TriggerRef: &ref}, // manual with a ref
		{Trigger: jobs.TriggerSystem, TriggerRef: &ref}, // system with a ref
	}
	for _, req := range cases {
		if _, err := svc.EnqueueRunWith(ctx, org, templateID, req); !errors.Is(err, jobs.ErrInvalidInput) {
			t.Errorf("request %+v: err = %v, want ErrInvalidInput", req, err)
		}
	}
}
