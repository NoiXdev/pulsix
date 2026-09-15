package jobs_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
)

var ctx = context.Background()

// --- fakes ---

type storedJob struct {
	j       jobs.Job
	payload json.RawMessage
}

type fakeStore struct {
	mu        sync.Mutex
	jobs      map[uuid.UUID]storedJob
	cancelled map[uuid.UUID]jobs.Status
	events    []jobs.Event
	eventsErr error
	syncs     []fakeSyncResult
	syncErr   error
	nextErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{jobs: map[uuid.UUID]storedJob{}, cancelled: map[uuid.UUID]jobs.Status{}}
}

func (f *fakeStore) CreateJob(_ context.Context, j jobs.Job, payload json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nextErr != nil {
		return f.nextErr
	}
	f.jobs[j.ID] = storedJob{j: j, payload: payload}
	return nil
}

func (f *fakeStore) GetJob(_ context.Context, orgID, id uuid.UUID) (jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.jobs[id]
	if !ok || s.j.OrgID != orgID {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return s.j, nil
}

func (f *fakeStore) ListJobs(_ context.Context, orgID uuid.UUID, filter jobs.Filter, cursor string, limit int) ([]jobs.Job, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []jobs.Job
	for _, s := range f.jobs {
		if s.j.OrgID != orgID {
			continue
		}
		if filter.Status != nil && s.j.Status != *filter.Status {
			continue
		}
		if filter.Kind != nil && s.j.Kind != *filter.Kind {
			continue
		}
		if filter.ZoneID != nil && s.j.ZoneID != *filter.ZoneID {
			continue
		}
		out = append(out, s.j)
	}
	next := ""
	if cursor == "more" && len(out) > 0 {
		next = "tail"
	}
	return out, next, nil
}

func (f *fakeStore) CancelJob(_ context.Context, orgID, id uuid.UUID) (jobs.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.jobs[id]
	if !ok || s.j.OrgID != orgID {
		return "", jobs.ErrNotFound
	}
	if s.j.Status != jobs.StatusQueued && s.j.Status != jobs.StatusRunning {
		return "", jobs.ErrNotRunning
	}
	next := jobs.StatusCancelling
	if s.j.Status == jobs.StatusQueued {
		next = jobs.StatusCancelled
	}
	s.j.Status = next
	f.jobs[id] = s
	f.cancelled[id] = next
	return next, nil
}

func (f *fakeStore) ListJobEvents(_ context.Context, orgID, jobID uuid.UUID, after int64) ([]jobs.Event, error) {
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	var out []jobs.Event
	for _, e := range f.events {
		if e.Seq > uint64(after) {
			out = append(out, e)
		}
	}
	return out, nil
}

// syncResults records RecordSyncResult calls; syncErr is what the store
// reports back (e.g. ErrNotFound for a repo deleted mid-run).
type fakeSyncResult struct {
	orgID, repoID uuid.UUID
	at            time.Time
	errText       string
}

func (f *fakeStore) RecordSyncResult(_ context.Context, orgID, repoID uuid.UUID, at time.Time, errText string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.syncErr != nil {
		return f.syncErr
	}
	f.syncs = append(f.syncs, fakeSyncResult{orgID, repoID, at, errText})
	return nil
}

type fakeQueue struct {
	mu       sync.Mutex
	notes    []time.Time
	claimErr error
}

func (f *fakeQueue) Claim(context.Context, []uuid.UUID, string) (*jobs.Claim, error) {
	return nil, f.claimErr
}

func (f *fakeQueue) BindStream(context.Context, uuid.UUID, string) (jobs.Stream, error) {
	return nil, jobs.ErrUnknownStream
}

func (f *fakeQueue) Finish(context.Context, uuid.UUID, jobs.Status, map[string]any, string) error {
	return nil
}

func (f *fakeQueue) NoteCancelRequested(_ uuid.UUID, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, at)
}

type fakeTemplates struct {
	byID map[uuid.UUID]jobs.TemplateRow
	err  error
}

func (f *fakeTemplates) TemplateLookup(_ context.Context, _, id uuid.UUID) (jobs.TemplateRow, error) {
	if f.err != nil {
		return jobs.TemplateRow{}, f.err
	}
	t, ok := f.byID[id]
	if !ok {
		return jobs.TemplateRow{}, jobs.ErrNotFound
	}
	return t, nil
}

type fakeResolver struct {
	targets []jobs.ZoneTargets
	err     error
	scopes  []render.Scope
}

func (f *fakeResolver) Resolve(_ context.Context, _ uuid.UUID, scope render.Scope) ([]jobs.ZoneTargets, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.scopes = append(f.scopes, scope)
	return f.targets, nil
}

type fakeHosts struct{ byID map[uuid.UUID]hosts.Host }

func (f *fakeHosts) GetHost(_ context.Context, _, id uuid.UUID) (hosts.Host, error) {
	h, ok := f.byID[id]
	if !ok {
		return hosts.Host{}, hosts.ErrNotFound
	}
	return h, nil
}

// fakeZones is the DefaultZoneSource stand-in: zones per org, with an
// optional error.
type fakeZones struct {
	byOrg map[uuid.UUID][]hosts.Zone
	err   error
}

func (f *fakeZones) ListZones(_ context.Context, orgID uuid.UUID) ([]hosts.Zone, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byOrg[orgID], nil
}

func (f *fakeZones) add(orgID, zoneID uuid.UUID, slug string) {
	if f.byOrg == nil {
		f.byOrg = map[uuid.UUID][]hosts.Zone{}
	}
	f.byOrg[orgID] = append(f.byOrg[orgID], hosts.Zone{ID: zoneID, OrgID: orgID, Slug: slug})
}

type fakeSealer struct{}

func (fakeSealer) SealAAD(plain, _ []byte) ([]byte, error) {
	return append([]byte("sealed:"), plain...), nil
}

func (fakeSealer) OpenAAD(sealed, _ []byte) ([]byte, error) {
	rest, ok := strings.CutPrefix(string(sealed), "sealed:")
	if !ok {
		return nil, errors.New("not sealed")
	}
	return []byte(rest), nil
}

func newService(store *fakeStore, q *fakeQueue, tmpl *fakeTemplates, res *fakeResolver, hs *fakeHosts, zones ...jobs.DefaultZoneSource) *jobs.Service {
	var zs jobs.DefaultZoneSource
	for _, z := range zones {
		zs = z
	}
	return jobs.NewService(store, q, tmpl, res, hs, zs, fakeSealer{}, func() time.Time {
		return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	}, rand.Reader)
}

// --- tests ---

func TestEnqueueRunSplitsOneJobPerZone(t *testing.T) {
	store, queue := newFakeStore(), &fakeQueue{}
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	zoneA, zoneB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		templateID: {ID: templateID, OrgID: org, Scope: json.RawMessage(`{"group_id":null,"host_ids":[]}`)},
	}}
	res := &fakeResolver{targets: []jobs.ZoneTargets{
		{ZoneID: zoneA, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}},
		{ZoneID: zoneB, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}},
	}}
	actor := uuid.Must(uuid.NewV7())
	svc := newService(store, queue, tmpl, res, &fakeHosts{})

	created, err := svc.EnqueueRun(ctx, org, templateID, &actor)
	if err != nil {
		t.Fatalf("EnqueueRun: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("jobs = %d, want one per zone", len(created))
	}
	if created[0].RunGroupID != created[1].RunGroupID {
		t.Fatal("per-zone jobs must share a run group")
	}
	if created[0].RunGroupID == uuid.Nil {
		t.Fatal("run group must be a real uuid")
	}
	for i, j := range created {
		if j.Kind != jobs.KindRun || j.Status != jobs.StatusQueued || j.Trigger != jobs.TriggerManual {
			t.Fatalf("job %d = %+v", i, j)
		}
		if j.TemplateID == nil || *j.TemplateID != templateID {
			t.Fatalf("job %d template = %v", i, j.TemplateID)
		}
		if j.ActorID == nil || *j.ActorID != actor {
			t.Fatalf("job %d actor = %v", i, j.ActorID)
		}
		if j.ZoneID != res.targets[i].ZoneID || len(j.HostIDs) != len(res.targets[i].HostIDs) {
			t.Fatalf("job %d zone/targets = %+v, want %+v", i, j, res.targets[i])
		}
		if !j.CreatedAt.Equal(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)) {
			t.Fatalf("job %d created at = %v", i, j.CreatedAt)
		}
		stored := store.jobs[j.ID]
		if stored.payload != nil {
			t.Fatalf("run jobs store no payload: %v", stored.payload)
		}
	}
	if len(res.scopes) != 1 || res.scopes[0].GroupID != nil {
		t.Fatalf("resolver scopes = %+v", res.scopes)
	}
}

func TestEnqueueRunFailsClosed(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	cases := []struct {
		name string
		tmpl *fakeTemplates
		res  *fakeResolver
		want error
	}{
		{"unknown template", &fakeTemplates{}, &fakeResolver{}, jobs.ErrNotFound},
		{"broken scope", &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
			templateID: {ID: templateID, Scope: json.RawMessage(`nope`)},
		}}, &fakeResolver{}, jobs.ErrInvalidInput},
		{"empty scope", &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
			templateID: {ID: templateID, Scope: json.RawMessage(`{}`)},
		}}, &fakeResolver{}, jobs.ErrEmptyScope},
		{"resolver error", &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
			templateID: {ID: templateID, Scope: json.RawMessage(`{}`)},
		}}, &fakeResolver{err: errors.New("boom")}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := newService(newFakeStore(), &fakeQueue{}, c.tmpl, c.res, &fakeHosts{})
			_, err := svc.EnqueueRun(ctx, org, templateID, nil)
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.want == nil && err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestEnqueueHostJobSplitsByZoneAndSealsPayload(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	zoneA, zoneB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	h := func(zone uuid.UUID, managed bool) hosts.Host {
		return hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Managed: managed}
	}
	h1, h2, h3 := h(zoneA, true), h(zoneA, true), h(zoneB, true)
	fh := &fakeHosts{byID: map[uuid.UUID]hosts.Host{h1.ID: h1, h2.ID: h2, h3.ID: h3}}
	store := newFakeStore()
	svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, fh)

	payload := json.RawMessage(`{"argv":["uptime"]}`)
	first, err := svc.EnqueueHostJob(ctx, org, jobs.KindAdhoc, []uuid.UUID{h1.ID, h2.ID, h3.ID}, payload, jobs.TriggerManual, nil)
	if err != nil {
		t.Fatalf("EnqueueHostJob: %v", err)
	}
	if first.ZoneID != zoneA {
		t.Fatalf("first job zone = %v", first.ZoneID)
	}
	var groupJobs []jobs.Job
	for _, s := range store.jobs {
		if s.j.RunGroupID == first.RunGroupID {
			groupJobs = append(groupJobs, s.j)
		}
	}
	if len(groupJobs) != 2 {
		t.Fatalf("run group has %d jobs, want one per zone", len(groupJobs))
	}
	for _, j := range groupJobs {
		if j.Kind != jobs.KindAdhoc || j.Status != jobs.StatusQueued {
			t.Fatalf("job = %+v", j)
		}
		// The payload is sealed: a JSON string carrying the base64
		// ciphertext, openable only under the job's own AAD.
		var b64 string
		if err := json.Unmarshal(store.jobs[j.ID].payload, &b64); err != nil {
			t.Fatalf("payload of %s is not a JSON string: %v", j.ID, err)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := fakeSealer{}.OpenAAD(raw, jobs.JobAAD(org, j.ID))
		if err != nil || string(plain) != string(payload) {
			t.Fatalf("sealed payload round trip = %q, %v", plain, err)
		}
	}
	// The first zone's job carries both of its hosts, deduplicated.
	for _, j := range groupJobs {
		if j.ZoneID == zoneA && len(j.HostIDs) != 2 {
			t.Fatalf("zone A job hosts = %v", j.HostIDs)
		}
	}
}

func TestEnqueueHostJobValidatesInput(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	zone := uuid.Must(uuid.NewV7())
	managed := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Managed: true}
	unmanaged := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Managed: false}
	fh := &fakeHosts{byID: map[uuid.UUID]hosts.Host{managed.ID: managed, unmanaged.ID: unmanaged}}
	svc := newService(newFakeStore(), &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, fh)

	cases := []struct {
		name string
		kind jobs.Kind
		ids  []uuid.UUID
		want error
	}{
		{"run kind", jobs.KindRun, []uuid.UUID{managed.ID}, jobs.ErrInvalidInput},
		{"unknown kind", jobs.Kind("nope"), []uuid.UUID{managed.ID}, jobs.ErrInvalidInput},
		{"no hosts", jobs.KindAdhoc, nil, jobs.ErrInvalidInput},
		{"unknown host", jobs.KindAdhoc, []uuid.UUID{uuid.Must(uuid.NewV7())}, jobs.ErrNotFound},
		{"unmanaged host", jobs.KindConnectionTest, []uuid.UUID{unmanaged.ID}, jobs.ErrInvalidInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.EnqueueHostJob(ctx, org, c.kind, c.ids, nil, jobs.TriggerManual, nil)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestCancel(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	newJob := func(status jobs.Status) (uuid.UUID, *fakeStore) {
		store := newFakeStore()
		id := uuid.Must(uuid.NewV7())
		store.jobs[id] = storedJob{j: jobs.Job{ID: id, OrgID: org, Status: status, Kind: jobs.KindRun, RunGroupID: uuid.Must(uuid.NewV7())}}
		return id, store
	}

	t.Run("queued cancels outright", func(t *testing.T) {
		id, store := newJob(jobs.StatusQueued)
		queue := &fakeQueue{}
		svc := newService(store, queue, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{})
		if err := svc.Cancel(ctx, org, id); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		if store.cancelled[id] != jobs.StatusCancelled {
			t.Fatalf("status = %v", store.cancelled[id])
		}
		if len(queue.notes) != 0 {
			t.Fatal("a queued job needs no cancel deadline")
		}
	})

	t.Run("running enters cancelling and notes the deadline", func(t *testing.T) {
		id, store := newJob(jobs.StatusRunning)
		queue := &fakeQueue{}
		svc := newService(store, queue, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{})
		if err := svc.Cancel(ctx, org, id); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		if store.cancelled[id] != jobs.StatusCancelling {
			t.Fatalf("status = %v", store.cancelled[id])
		}
		if len(queue.notes) != 1 {
			t.Fatalf("cancel requests noted = %d", len(queue.notes))
		}
	})

	t.Run("terminal jobs refuse", func(t *testing.T) {
		for _, status := range []jobs.Status{jobs.StatusSucceeded, jobs.StatusFailed, jobs.StatusCancelled, jobs.StatusError, jobs.StatusCancelling} {
			id, store := newJob(status)
			svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{})
			if err := svc.Cancel(ctx, org, id); !errors.Is(err, jobs.ErrNotRunning) {
				t.Fatalf("Cancel on %s: %v", status, err)
			}
		}
	})

	t.Run("unknown job", func(t *testing.T) {
		svc := newService(newFakeStore(), &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{})
		if err := svc.Cancel(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, jobs.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRetryFailedEnqueuesOnlyTheFailedHosts(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	h1, h2, h3 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.jobs[id] = storedJob{j: jobs.Job{
		ID: id, OrgID: org, Kind: jobs.KindRun, Status: jobs.StatusFailed,
		TemplateID: &templateID, HostIDs: []uuid.UUID{h1, h2, h3}, RunGroupID: uuid.Must(uuid.NewV7()),
		Stats: map[string]any{
			jobs.StatsFailedHosts:      []any{h1.String()},
			jobs.StatsUnreachableHosts: []any{h2.String(), h2.String(), "not-a-uuid", h3.String()},
		},
	}}
	tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		templateID: {ID: templateID, OrgID: org, Scope: json.RawMessage(`{}`)},
	}}
	res := &fakeResolver{targets: []jobs.ZoneTargets{{ZoneID: uuid.Must(uuid.NewV7()), HostIDs: []uuid.UUID{h1, h2, h3}}}}
	actor := uuid.Must(uuid.NewV7())
	svc := newService(store, &fakeQueue{}, tmpl, res, &fakeHosts{})

	retried, err := svc.RetryFailed(ctx, org, id, &actor)
	if err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	if len(res.scopes) != 1 {
		t.Fatalf("resolver calls = %d", len(res.scopes))
	}
	got := map[uuid.UUID]bool{}
	for _, h := range res.scopes[0].HostIDs {
		got[h] = true
	}
	if len(got) != 3 || !got[h1] || !got[h2] || !got[h3] {
		t.Fatalf("retry scope = %v, want exactly the failed and unreachable hosts", res.scopes[0].HostIDs)
	}
	if len(retried) != 1 || retried[0].RunGroupID == store.jobs[id].j.RunGroupID {
		t.Fatal("the retry must be a new run group")
	}
	if retried[0].ActorID == nil || *retried[0].ActorID != actor {
		t.Fatalf("retry actor = %v", retried[0].ActorID)
	}
}

func TestRetryFailedRefuses(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	base := func(status jobs.Status, kind jobs.Kind, stats map[string]any) (uuid.UUID, *fakeStore) {
		store := newFakeStore()
		id := uuid.Must(uuid.NewV7())
		j := jobs.Job{ID: id, OrgID: org, Kind: kind, Status: status, RunGroupID: uuid.Must(uuid.NewV7()), Stats: stats}
		if kind == jobs.KindRun {
			j.TemplateID = &templateID
			j.HostIDs = []uuid.UUID{uuid.Must(uuid.NewV7())}
		}
		store.jobs[id] = storedJob{j: j}
		return id, store
	}
	cases := []struct {
		name   string
		kind   jobs.Kind
		status jobs.Status
		stats  map[string]any
		want   error
	}{
		{"adhoc job", jobs.KindAdhoc, jobs.StatusFailed, nil, jobs.ErrInvalidInput},
		{"still running", jobs.KindRun, jobs.StatusRunning, nil, jobs.ErrNotRunning},
		{"succeeded run", jobs.KindRun, jobs.StatusSucceeded, nil, jobs.ErrNothingToRetry},
		{"no failed hosts", jobs.KindRun, jobs.StatusFailed,
			map[string]any{jobs.StatsFailedHosts: []any{}}, jobs.ErrNothingToRetry},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, store := base(c.status, c.kind, c.stats)
			svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{})
			_, err := svc.RetryFailed(ctx, org, id, nil)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}

	t.Run("unknown template", func(t *testing.T) {
		// The stats name one of the job's own hosts, so the retry gets as
		// far as the template lookup before failing.
		store := newFakeStore()
		id, hostID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		store.jobs[id] = storedJob{j: jobs.Job{
			ID: id, OrgID: org, Kind: jobs.KindRun, Status: jobs.StatusFailed,
			TemplateID: &templateID, HostIDs: []uuid.UUID{hostID}, RunGroupID: uuid.Must(uuid.NewV7()),
			Stats: map[string]any{jobs.StatsFailedHosts: []any{hostID.String()}},
		}}
		svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{})
		if _, err := svc.RetryFailed(ctx, org, id, nil); !errors.Is(err, jobs.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("unknown job", func(t *testing.T) {
		svc := newService(newFakeStore(), &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{})
		if _, err := svc.RetryFailed(ctx, org, uuid.Must(uuid.NewV7()), nil); !errors.Is(err, jobs.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestListClampsLimitAndDecodesCursor(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	for i := 0; i < 3; i++ {
		id := uuid.Must(uuid.NewV7())
		store.jobs[id] = storedJob{j: jobs.Job{ID: id, OrgID: org, Status: jobs.StatusQueued, Kind: jobs.KindRun, RunGroupID: uuid.Must(uuid.NewV7())}}
	}
	svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{})

	if _, next, err := svc.List(ctx, org, jobs.Filter{}, "", 0); err != nil || next != "" {
		t.Fatalf("first page: next=%q, %v", next, err)
	}
	if _, next, err := svc.List(ctx, org, jobs.Filter{}, "more", 1000); err != nil || next != "tail" {
		t.Fatalf("cursor page: next=%q, %v", next, err)
	}
	status := jobs.StatusQueued
	kind := jobs.KindRun
	zone := uuid.Must(uuid.NewV7())
	if _, _, err := svc.List(ctx, org, jobs.Filter{Status: &status, Kind: &kind, ZoneID: &zone}, "", 10); err != nil {
		t.Fatalf("filtered list: %v", err)
	}
}

func TestGetAndEventsCheckTheOrgFirst(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{})

	if _, err := svc.Get(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("Get: %v", err)
	}
	if _, err := svc.Events(ctx, org, uuid.Must(uuid.NewV7()), 0); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("Events: %v", err)
	}
	if len(store.events) != 0 {
		t.Fatal("Events must not read events before the job is resolved")
	}

	id := uuid.Must(uuid.NewV7())
	store.jobs[id] = storedJob{j: jobs.Job{ID: id, OrgID: org, Kind: jobs.KindRun, Status: jobs.StatusQueued, RunGroupID: uuid.Must(uuid.NewV7())}}
	store.events = []jobs.Event{{Seq: 1, State: "ok"}, {Seq: 2, State: "ok"}}
	events, err := svc.Events(ctx, org, id, 1)
	if err != nil || len(events) != 1 || events[0].Seq != 2 {
		t.Fatalf("Events(after 1) = %+v, %v", events, err)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 14, 12, 0, 0, 123456789, time.UTC)
	id := uuid.Must(uuid.NewV7())
	gotAt, gotID, err := jobs.DecodeCursor(jobs.EncodeCursor(at, id))
	if err != nil || !gotAt.Equal(at) || gotID != id {
		t.Fatalf("round trip = %v, %v, %v", gotAt, gotID, err)
	}
	if at, id, err := jobs.DecodeCursor(""); err != nil || !at.IsZero() || id != uuid.Nil {
		t.Fatalf("empty cursor = %v, %v, %v", at, id, err)
	}
	for _, bad := range []string{"!!!", "AAAA", jobs.EncodeCursor(at, uuid.Nil) + "|x"} {
		if _, _, err := jobs.DecodeCursor(bad); !errors.Is(err, jobs.ErrInvalidCursor) {
			t.Errorf("DecodeCursor(%q) = %v, want ErrInvalidCursor", bad, err)
		}
	}
}

// TestEnqueueSyncEnqueuesIntoTheDefaultZone pins the sync_repo semantics the
// task resolved: one job, not host-bound, in the org's default zone, with a
// plain (never sealed) {"repo_id": ...} payload.
func TestEnqueueSyncEnqueuesIntoTheDefaultZone(t *testing.T) {
	org, repoID, actor := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	defaultZone, otherZone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	zs := &fakeZones{}
	zs.add(org, otherZone, "edge")
	zs.add(org, defaultZone, hosts.DefaultZoneSlug)
	store := newFakeStore()
	svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{}, zs)

	job, err := svc.EnqueueSync(ctx, org, repoID, &actor)
	if err != nil {
		t.Fatalf("EnqueueSync: %v", err)
	}
	if job.ZoneID != defaultZone || job.Kind != jobs.KindSyncRepo || job.Status != jobs.StatusQueued ||
		job.Trigger != jobs.TriggerManual || job.TemplateID != nil || len(job.HostIDs) != 0 {
		t.Fatalf("job = %+v", job)
	}
	if job.ActorID == nil || *job.ActorID != actor || job.RunGroupID == uuid.Nil {
		t.Fatalf("job attribution = %+v", job)
	}
	if !job.CreatedAt.Equal(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("created at = %v", job.CreatedAt)
	}
	// The payload is the plain repo reference, NOT sealed: it carries no
	// secret, and the queue passes plain JSON through at claim time.
	stored := store.jobs[job.ID]
	var payload struct {
		RepoID string `json:"repo_id"`
	}
	if err := json.Unmarshal(stored.payload, &payload); err != nil {
		t.Fatalf("payload %s is not plain JSON: %v", stored.payload, err)
	}
	if payload.RepoID != repoID.String() {
		t.Fatalf("payload = %s, want repo_id %s", stored.payload, repoID)
	}
}

func TestEnqueueSyncFailsClosed(t *testing.T) {
	org, repoID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	actor := uuid.Must(uuid.NewV7())

	t.Run("no default zone", func(t *testing.T) {
		zs := &fakeZones{}
		zs.add(org, uuid.Must(uuid.NewV7()), "edge")
		svc := newService(newFakeStore(), &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{}, zs)
		if _, err := svc.EnqueueSync(ctx, org, repoID, &actor); !errors.Is(err, jobs.ErrNoZone) {
			t.Fatalf("err = %v, want ErrNoZone", err)
		}
	})
	t.Run("zone listing error", func(t *testing.T) {
		boom := errors.New("boom")
		zs := &fakeZones{err: boom}
		svc := newService(newFakeStore(), &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{}, zs)
		if _, err := svc.EnqueueSync(ctx, org, repoID, &actor); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want wrapped boom", err)
		}
	})
	t.Run("store error", func(t *testing.T) {
		boom := errors.New("boom")
		zs := &fakeZones{}
		zs.add(org, uuid.Must(uuid.NewV7()), hosts.DefaultZoneSlug)
		store := newFakeStore()
		store.nextErr = boom
		svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{}, zs)
		if _, err := svc.EnqueueSync(ctx, org, repoID, &actor); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want wrapped boom", err)
		}
	})
}

// TestRecordSyncResult proves the org-scoped half records the terminal sync
// outcome on the repository.
func TestRecordSyncResult(t *testing.T) {
	org, repoID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	at := time.Date(2026, 9, 14, 13, 0, 0, 0, time.UTC)
	store := newFakeStore()
	svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{}, &fakeZones{})

	if err := svc.RecordSyncResult(ctx, org, repoID, at, ""); err != nil {
		t.Fatalf("RecordSyncResult: %v", err)
	}
	if err := svc.RecordSyncResult(ctx, org, repoID, at, "git pull failed"); err != nil {
		t.Fatalf("RecordSyncResult: %v", err)
	}
	if len(store.syncs) != 2 {
		t.Fatalf("syncs = %+v", store.syncs)
	}
	success, failure := store.syncs[0], store.syncs[1]
	if success.orgID != org || success.repoID != repoID || !success.at.Equal(at) || success.errText != "" {
		t.Fatalf("success record = %+v", success)
	}
	if failure.errText != "git pull failed" {
		t.Fatalf("failure record = %+v", failure)
	}
	store.syncErr = jobs.ErrNotFound
	if err := svc.RecordSyncResult(ctx, org, repoID, at, ""); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("store failure = %v, want ErrNotFound", err)
	}
}

func TestKindAndStatusPredicates(t *testing.T) {
	for _, k := range []jobs.Kind{"run", "adhoc", "connection_test", "bootstrap", "rotate_key", "sync_repo"} {
		if !k.Valid() {
			t.Errorf("kind %q must be valid", k)
		}
	}
	if jobs.Kind("nope").Valid() || jobs.KindRun.IsHostKind() {
		t.Error("kind predicates broken")
	}
	for _, s := range []jobs.Status{"queued", "claimed", "running", "cancelling", "succeeded", "failed", "cancelled", "error"} {
		if !s.Valid() {
			t.Errorf("status %q must be valid", s)
		}
	}
	for _, s := range []jobs.Status{"succeeded", "failed", "cancelled", "error"} {
		if !s.Terminal() {
			t.Errorf("status %q must be terminal", s)
		}
	}
	for _, s := range []jobs.Status{"queued", "claimed", "running", "cancelling"} {
		if s.Terminal() {
			t.Errorf("status %q must not be terminal", s)
		}
	}
	if jobs.Status("nope").Valid() {
		t.Error("unknown status must not be valid")
	}
}

func TestJobAADBindsOrgAndJob(t *testing.T) {
	org, job := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if got := string(jobs.JobAAD(org, job)); got != "job:"+org.String()+":"+job.String() {
		t.Fatalf("JobAAD = %q", got)
	}
}

// Compile-time interface checks for the fakes.
var (
	_ jobs.Store             = (*fakeStore)(nil)
	_ jobs.Queue             = (*fakeQueue)(nil)
	_ jobs.CancelRecorder    = (*fakeQueue)(nil)
	_ jobs.TemplateSource    = (*fakeTemplates)(nil)
	_ jobs.ScopeResolver     = (*fakeResolver)(nil)
	_ jobs.HostLookup        = (*fakeHosts)(nil)
	_ jobs.DefaultZoneSource = (*fakeZones)(nil)
)

// guardedHosts is a fakeHosts that also implements the KeyGuardSource port:
// the changed set EnqueueRun's block-on-mismatch excludes.
type guardedHosts struct {
	fakeHosts
	changed map[uuid.UUID]bool
	err     error
}

func (g *guardedHosts) ChangedHosts(_ context.Context, _ uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if g.err != nil {
		return nil, g.err
	}
	out := map[uuid.UUID]bool{}
	for _, id := range hostIDs {
		if g.changed[id] {
			out[id] = true
		}
	}
	return out, nil
}

func TestEnqueueRunBlocksOnChangedHostKey(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	good, bad := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	zone := uuid.Must(uuid.NewV7())

	template := func() (jobs.Job, *fakeTemplates) {
		id := uuid.Must(uuid.NewV7())
		return jobs.Job{Kind: jobs.KindRun, TemplateID: &id}, &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
			id: {ID: id, OrgID: org, PlaybookID: uuid.Must(uuid.NewV7()), PlaybookInline: "- hosts: all", Scope: json.RawMessage(`{"host_ids":["` + good.String() + `","` + bad.String() + `"]}`)},
		}}
	}

	t.Run("one changed host in two keeps the good one", func(t *testing.T) {
		j, tmpl := template()
		fh := &guardedHosts{fakeHosts: fakeHosts{byID: map[uuid.UUID]hosts.Host{
			good: {ID: good, OrgID: org, ZoneID: zone, Managed: true},
			bad:  {ID: bad, OrgID: org, ZoneID: zone, Managed: true},
		}}, changed: map[uuid.UUID]bool{bad: true}}
		res := &fakeResolver{targets: []jobs.ZoneTargets{{ZoneID: zone, HostIDs: []uuid.UUID{good, bad}}}}
		svc := jobs.NewService(newFakeStore(), &fakeQueue{}, tmpl, res, fh, nil, fakeSealer{}, time.Now, rand.Reader)
		jobsOut, err := svc.EnqueueRun(ctx, org, *j.TemplateID, nil)
		if err != nil {
			t.Fatalf("EnqueueRun: %v", err)
		}
		if len(jobsOut) != 1 || !slices.Equal(jobsOut[0].HostIDs, []uuid.UUID{good}) {
			t.Fatalf("jobs = %+v, want only the good host", jobsOut)
		}
	})

	t.Run("all changed fails the enqueue", func(t *testing.T) {
		j, tmpl := template()
		fh := &guardedHosts{fakeHosts: fakeHosts{byID: map[uuid.UUID]hosts.Host{
			good: {ID: good, OrgID: org, ZoneID: zone, Managed: true},
			bad:  {ID: bad, OrgID: org, ZoneID: zone, Managed: true},
		}}, changed: map[uuid.UUID]bool{good: true, bad: true}}
		res := &fakeResolver{targets: []jobs.ZoneTargets{{ZoneID: zone, HostIDs: []uuid.UUID{good, bad}}}}
		svc := jobs.NewService(newFakeStore(), &fakeQueue{}, tmpl, res, fh, nil, fakeSealer{}, time.Now, rand.Reader)
		if _, err := svc.EnqueueRun(ctx, org, *j.TemplateID, nil); !errors.Is(err, jobs.ErrHostKeyChanged) {
			t.Fatalf("EnqueueRun = %v, want ErrHostKeyChanged", err)
		}
	})

	t.Run("a guard failure fails the enqueue closed", func(t *testing.T) {
		j, tmpl := template()
		fh := &guardedHosts{fakeHosts: fakeHosts{byID: map[uuid.UUID]hosts.Host{
			good: {ID: good, OrgID: org, ZoneID: zone, Managed: true},
		}}, err: errors.New("locked")}
		res := &fakeResolver{targets: []jobs.ZoneTargets{{ZoneID: zone, HostIDs: []uuid.UUID{good}}}}
		svc := jobs.NewService(newFakeStore(), &fakeQueue{}, tmpl, res, fh, nil, fakeSealer{}, time.Now, rand.Reader)
		if _, err := svc.EnqueueRun(ctx, org, *j.TemplateID, nil); err == nil || !strings.Contains(err.Error(), "locked") {
			t.Fatalf("EnqueueRun = %v, want the guard failure", err)
		}
	})
}
