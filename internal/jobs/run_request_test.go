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
