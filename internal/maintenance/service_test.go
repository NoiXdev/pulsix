package maintenance_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/maintenance"
)

var ctx = context.Background()

type fakeStore struct {
	mu   sync.Mutex
	byID map[uuid.UUID]maintenance.Window
	err  error
}

func newFakeStore() *fakeStore { return &fakeStore{byID: map[uuid.UUID]maintenance.Window{}} }

func (f *fakeStore) Create(_ context.Context, w maintenance.Window) (maintenance.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Revision = 1
	f.byID[w.ID] = w
	return w, nil
}

func (f *fakeStore) Get(_ context.Context, orgID, id uuid.UUID) (maintenance.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.byID[id]
	if !ok || w.OrgID != orgID {
		return maintenance.Window{}, maintenance.ErrNotFound
	}
	return w, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]maintenance.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []maintenance.Window
	for _, w := range f.byID {
		if w.OrgID == orgID {
			out = append(out, w)
		}
	}
	return out, nil
}

func (f *fakeStore) Update(_ context.Context, w maintenance.Window, revision int64) (maintenance.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.byID[w.ID]
	if !ok {
		return maintenance.Window{}, maintenance.ErrNotFound
	}
	if cur.Revision != revision {
		return maintenance.Window{}, maintenance.ErrRevisionMismatch
	}
	w.Revision = revision + 1
	f.byID[w.ID] = w
	return w, nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.byID[id]; !ok || w.OrgID != orgID {
		return maintenance.ErrNotFound
	}
	delete(f.byID, id)
	return nil
}

type fakeGroups struct {
	members map[uuid.UUID][]uuid.UUID
	err     error
}

func (f fakeGroups) HostsInGroup(_ context.Context, _, groupID uuid.UUID) ([]uuid.UUID, error) {
	return f.members[groupID], f.err
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestHostsInMaintenance(t *testing.T) {
	now := at("2026-09-22T10:30:00Z")
	store := newFakeStore()
	org := uuid.Must(uuid.NewV7())
	hA, hB, hC, group := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	svc := maintenance.NewService(store, fakeGroups{members: map[uuid.UUID][]uuid.UUID{group: {hB}}}, func() time.Time { return now }, nil, quiet)
	active := maintenance.Input{Name: "a", TargetKind: maintenance.TargetHost, TargetID: &hA, StartsAt: ptr(now.Add(-time.Hour)), EndsAt: ptr(now.Add(time.Hour))}
	if _, err := svc.Create(ctx, org, uuid.Must(uuid.NewV7()), active); err != nil {
		t.Fatal(err)
	}
	later := maintenance.Input{Name: "b", TargetKind: maintenance.TargetHost, TargetID: &hC, StartsAt: ptr(now.Add(time.Hour)), EndsAt: ptr(now.Add(2 * time.Hour))}
	if _, err := svc.Create(ctx, org, uuid.Must(uuid.NewV7()), later); err != nil {
		t.Fatal(err)
	}
	groupWin := maintenance.Input{Name: "g", TargetKind: maintenance.TargetGroup, TargetID: &group, Cron: "30 12 * * *", Duration: "1h", Timezone: "Europe/Berlin"}
	if _, err := svc.Create(ctx, org, uuid.Must(uuid.NewV7()), groupWin); err != nil {
		t.Fatal(err)
	}

	got, err := svc.HostsInMaintenance(ctx, org, []uuid.UUID{hA, hB, hC}, now)
	if err != nil || len(got) != 2 || !got[hA] || !got[hB] || got[hC] {
		t.Fatalf("in maintenance = %v, %v; want hA (host window) and hB (group window)", got, err)
	}
	orgWin := maintenance.Input{Name: "all", TargetKind: maintenance.TargetOrg, StartsAt: ptr(now.Add(-time.Minute)), EndsAt: ptr(now.Add(time.Minute))}
	if _, err := svc.Create(ctx, org, uuid.Must(uuid.NewV7()), orgWin); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.HostsInMaintenance(ctx, org, []uuid.UUID{hA, hB, hC}, now); len(got) != 3 {
		t.Fatalf("an organization window covers every host, got %v", got)
	}
	failing := maintenance.NewService(store, fakeGroups{err: errors.New("inventory down")}, nil, nil, quiet)
	if _, err := failing.HostsInMaintenance(ctx, org, []uuid.UUID{hB}, now); err == nil {
		t.Fatal("a group resolution failure must surface")
	}
	store.err = errors.New("db down")
	if _, err := svc.HostsInMaintenance(ctx, org, []uuid.UUID{hA}, now); err == nil {
		t.Fatal("a store failure must surface")
	}
}

// TestHostsInMaintenanceFailsClosedOnAnUnevaluableWindow: a window whose
// cron or timezone no longer evaluates must fail the whole call, not be
// silently skipped — the guard's contract is absolute (spec §6), and a
// window that goes uncovered because it was ignored is indistinguishable
// from no window at all. Create/Update always validate the cron and
// timezone (validate.go), so this can only happen to a row that predates
// or bypasses that validation (a hand-edited row, or a later evaluator
// change); the fake store lets the test plant one directly.
func TestHostsInMaintenanceFailsClosedOnAnUnevaluableWindow(t *testing.T) {
	now := at("2026-09-22T10:30:00Z")
	store := newFakeStore()
	org := uuid.Must(uuid.NewV7())
	svc := maintenance.NewService(store, fakeGroups{}, func() time.Time { return now }, nil, quiet)
	broken := uuid.Must(uuid.NewV7())
	store.byID[broken] = maintenance.Window{ID: broken, OrgID: org, TargetKind: maintenance.TargetOrg, Cron: "broken", Duration: time.Hour, Timezone: "UTC"}
	_, err := svc.HostsInMaintenance(ctx, org, []uuid.UUID{uuid.Must(uuid.NewV7())}, now)
	if err == nil || !strings.Contains(err.Error(), broken.String()) {
		t.Fatalf("HostsInMaintenance = %v, want an error naming window %s", err, broken)
	}
}

// TestHostsInMaintenanceOrgScoped confirms a window belonging to another
// organization never covers a host, even when that host id collides with
// one of the caller's own hosts (the store's List is org-scoped, and
// HostsInMaintenance never reasons about a host outside the ids it was
// asked about).
func TestHostsInMaintenanceOrgScoped(t *testing.T) {
	now := at("2026-09-22T10:30:00Z")
	store := newFakeStore()
	orgA, orgB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	host := uuid.Must(uuid.NewV7())
	svcA := maintenance.NewService(store, fakeGroups{}, func() time.Time { return now }, nil, quiet)
	svcB := maintenance.NewService(store, fakeGroups{}, func() time.Time { return now }, nil, quiet)
	// An org-wide window in orgB covers every host of orgB, never orgA's.
	if _, err := svcB.Create(ctx, orgB, uuid.Must(uuid.NewV7()), maintenance.Input{
		Name: "b-wide", TargetKind: maintenance.TargetOrg, StartsAt: ptr(now.Add(-time.Hour)), EndsAt: ptr(now.Add(time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := svcA.HostsInMaintenance(ctx, orgA, []uuid.UUID{host}, now)
	if err != nil || len(got) != 0 {
		t.Fatalf("orgA must not see orgB's maintenance window: %v, %v", got, err)
	}
	// The same host id, targeted by name in orgA's own window, IS covered.
	if _, err := svcA.Create(ctx, orgA, uuid.Must(uuid.NewV7()), maintenance.Input{
		Name: "a-host", TargetKind: maintenance.TargetHost, TargetID: &host, StartsAt: ptr(now.Add(-time.Hour)), EndsAt: ptr(now.Add(time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}
	got, err = svcA.HostsInMaintenance(ctx, orgA, []uuid.UUID{host}, now)
	if err != nil || len(got) != 1 || !got[host] {
		t.Fatalf("orgA's own window must cover its host: %v, %v", got, err)
	}
}

func TestListMarksActiveWindowsAndDurationText(t *testing.T) {
	now := at("2026-09-22T10:30:00Z")
	store := newFakeStore()
	org := uuid.Must(uuid.NewV7())
	svc := maintenance.NewService(store, fakeGroups{}, func() time.Time { return now }, nil, quiet)
	on, err := svc.Create(ctx, org, uuid.Must(uuid.NewV7()), maintenance.Input{Name: "on", TargetKind: maintenance.TargetOrg, Cron: "0 10 * * *", Duration: "90m", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	off, err := svc.Create(ctx, org, uuid.Must(uuid.NewV7()), maintenance.Input{Name: "off", TargetKind: maintenance.TargetOrg, Cron: "0 20 * * *", Duration: "2h", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	_, active, err := svc.List(ctx, org)
	if err != nil || !active[on.ID] || active[off.ID] {
		t.Fatalf("active = %v, %v", active, err)
	}
	if on.DurationText() != "1h30m" || off.DurationText() != "2h" || (maintenance.Window{}).DurationText() != "" {
		t.Fatalf("duration texts = %q, %q", on.DurationText(), off.DurationText())
	}
	store.err = errors.New("db down")
	if _, _, err := svc.List(ctx, org); err == nil {
		t.Fatal("a store failure must surface")
	}
}

func TestOccurrencesBetweenIsSorted(t *testing.T) {
	store := newFakeStore()
	org := uuid.Must(uuid.NewV7())
	svc := maintenance.NewService(store, fakeGroups{}, func() time.Time { return at("2026-09-22T00:00:00Z") }, nil, quiet)
	for _, in := range []maintenance.Input{
		{Name: "late", TargetKind: maintenance.TargetOrg, StartsAt: ptr(at("2026-09-24T10:00:00Z")), EndsAt: ptr(at("2026-09-24T11:00:00Z"))},
		{Name: "daily", TargetKind: maintenance.TargetOrg, Cron: "0 6 * * *", Duration: "30m", Timezone: "UTC"},
	} {
		if _, err := svc.Create(ctx, org, uuid.Must(uuid.NewV7()), in); err != nil {
			t.Fatal(err)
		}
	}
	broken := uuid.Must(uuid.NewV7())
	store.byID[broken] = maintenance.Window{ID: broken, OrgID: org, TargetKind: maintenance.TargetOrg}
	got, truncated, err := svc.OccurrencesBetween(ctx, org, at("2026-09-23T00:00:00Z"), at("2026-09-25T00:00:00Z"))
	if err != nil || len(got) != 3 || truncated {
		t.Fatalf("occurrences = %+v, truncated %v, %v", got, truncated, err)
	}
	// 23 Sep 06:00 daily, 24 Sep 06:00 daily, 24 Sep 10:00 late.
	if got[0].Name != "daily" || got[1].Name != "daily" || got[2].Name != "late" || !got[1].Start.Before(got[2].Start) {
		t.Fatalf("order = %s, %s, %s", got[0].Name, got[1].Name, got[2].Name)
	}
}

func TestCreateUpdateDelete(t *testing.T) {
	store := newFakeStore()
	org, actor := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	// Fixed at noon UTC: the daily 1h UTC window below runs 00:00-01:00,
	// so a real clock would make "active" flaky in that hour.
	now := func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	svc := maintenance.NewService(store, fakeGroups{}, now, nil, quiet)
	in := maintenance.Input{Name: "nightly", TargetKind: maintenance.TargetOrg, Cron: "@daily", Duration: "1h", Timezone: "UTC"}
	w, err := svc.Create(ctx, org, actor, in)
	if err != nil || *w.CreatedBy != actor || w.OrgID != org {
		t.Fatalf("create = %+v, %v", w, err)
	}
	if _, err := svc.Create(ctx, org, actor, maintenance.Input{}); !errors.Is(err, maintenance.ErrInvalidInput) {
		t.Fatalf("invalid create = %v", err)
	}
	in.Name = "nightly patching"
	up, err := svc.Update(ctx, org, w.ID, w.Revision, in)
	if err != nil || up.Name != "nightly patching" || up.Revision != 2 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := svc.Update(ctx, org, w.ID, 1, in); !errors.Is(err, maintenance.ErrRevisionMismatch) {
		t.Fatalf("stale update = %v", err)
	}
	if _, err := svc.Update(ctx, org, w.ID, 2, maintenance.Input{}); !errors.Is(err, maintenance.ErrInvalidInput) {
		t.Fatalf("invalid update = %v", err)
	}
	if list, active, err := svc.List(ctx, org); err != nil || len(list) != 1 || len(active) != 0 {
		t.Fatalf("list = %v, %v, %v", list, active, err)
	}
	if got, err := svc.Get(ctx, org, w.ID); err != nil || got.ID != w.ID {
		t.Fatalf("get = %v", err)
	}
	if err := svc.Delete(ctx, org, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, org, w.ID); !errors.Is(err, maintenance.ErrNotFound) {
		t.Fatalf("after delete = %v", err)
	}
}

// TestOccurrencesBetweenReportsAWindowsCap is the final review's
// Important #3 at the service layer: one frequent window is enough to
// truncate the answer, and the caller must be told.
func TestOccurrencesBetweenReportsAWindowsCap(t *testing.T) {
	store := newFakeStore()
	org := uuid.Must(uuid.NewV7())
	svc := maintenance.NewService(store, fakeGroups{}, func() time.Time { return at("2026-09-22T00:00:00Z") }, nil, quiet)
	in := maintenance.Input{Name: "agent restart", TargetKind: maintenance.TargetOrg, Cron: "*/15 * * * *", Duration: "5m", Timezone: "UTC"}
	if _, err := svc.Create(ctx, org, uuid.Must(uuid.NewV7()), in); err != nil {
		t.Fatal(err)
	}
	// Six weeks of a */15 window is 4032 stretches against a 500 cap.
	got, truncated, err := svc.OccurrencesBetween(ctx, org, at("2026-09-22T00:00:00Z"), at("2026-11-03T00:00:00Z"))
	if err != nil || len(got) != 500 {
		t.Fatalf("occurrences = %d, %v", len(got), err)
	}
	if !truncated {
		t.Fatal("truncated = false although the window hit its per-window cap")
	}
	// A quiet week of the same window fits and is not reported truncated.
	if _, capped, err := svc.OccurrencesBetween(ctx, org, at("2026-09-22T00:00:00Z"), at("2026-09-23T00:00:00Z")); err != nil || capped {
		t.Fatalf("a range the window fits in = truncated %v, %v", capped, err)
	}
}
