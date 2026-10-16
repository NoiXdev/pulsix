// SQLStore's tests exercise Create, Get, List, Update and Delete against a
// real database, the same way internal/alerts' own store_sql_test.go and
// internal/notify's own store_sql_test.go do. seedOrg and seedChannel mirror
// internal/db/schema_test.go's own fixtures — that file proved migration
// 00052's shape before this package existed to build a store on it; these
// tests prove the store itself.
package alertroutes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// seedOrg creates a bare organization with a unique slug, the minimal
// fixture every test below starts from.
func seedOrg(t *testing.T, d *db.DB) uuid.UUID {
	t.Helper()
	slug := "alert-routes-" + uuid.Must(uuid.NewV7()).String()
	org, err := gen.New(d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{
		ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug,
	})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// seedChannel creates a bare webhook notification channel in org — the
// target alert_route_channels' own composite FK needs.
func seedChannel(t *testing.T, d *db.DB, org uuid.UUID) uuid.UUID {
	t.Helper()
	ch, err := gen.New(d.Pool()).CreateNotificationChannel(context.Background(), gen.CreateNotificationChannelParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: "webhook", Name: "ops-" + uuid.Must(uuid.NewV7()).String(),
		Config: []byte(`{}`), BodyJson: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ch.ID
}

// seedMonitoringGroup creates a bare monitoring group in org — the target
// alert_routes' own scope_group_id composite FK needs, the same fixture
// internal/db's own schema_test.go builds for the same table.
func seedMonitoringGroup(t *testing.T, d *db.DB, org uuid.UUID) uuid.UUID {
	t.Helper()
	name := "g-" + uuid.Must(uuid.NewV7()).String()
	grp, err := gen.New(d.Pool()).CreateMonitoringGroup(context.Background(), gen.CreateMonitoringGroupParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: name, Selector: "role=" + name,
	})
	if err != nil {
		t.Fatal(err)
	}
	return grp.ID
}

func TestStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	in := alertroutes.Input{
		Name: "ops", Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: []rules.State{rules.StateCrit, rules.StateUnknown},
		Events:     []alerts.EventKind{alerts.EventOpened, alerts.EventClosed},
		Channels:   []uuid.UUID{ch},
	}
	created, err := s.Create(ctx, org, uuid.New(), in, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Severities) != 2 || len(got.Events) != 2 || len(got.Channels) != 1 {
		t.Errorf("round trip lost selections: %+v", got)
	}

	// A stale revision loses, and says so.
	if _, err := s.Update(ctx, org, created.ID, created.Revision+99, in, now); !errors.Is(err, alertroutes.ErrRevisionMismatch) {
		t.Errorf("Update with a stale revision = %v, want ErrRevisionMismatch", err)
	}
}

func TestStoreIsolatesOrganizations(t *testing.T) {
	// Review Focus 1. Two organizations, one route each; neither may see
	// the other's, in either direction.
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)
	chA, chB := seedChannel(t, d, orgA), seedChannel(t, d, orgB)
	s := alertroutes.NewSQLStore(d)
	now := time.Now()

	mk := func(org, ch uuid.UUID, name string) alertroutes.Route {
		t.Helper()
		r, err := s.Create(ctx, org, uuid.New(), alertroutes.Input{
			Name: name, Enabled: true, Scope: alertroutes.ScopeOrg,
			Severities: []rules.State{rules.StateCrit},
			Events:     []alerts.EventKind{alerts.EventOpened},
			Channels:   []uuid.UUID{ch},
		}, now)
		if err != nil {
			t.Fatalf("Create in %v: %v", org, err)
		}
		return r
	}
	ra, rb := mk(orgA, chA, "a"), mk(orgB, chB, "b")

	for _, tc := range []struct{ org, id uuid.UUID }{{orgA, rb.ID}, {orgB, ra.ID}} {
		if _, err := s.Get(ctx, tc.org, tc.id); !errors.Is(err, alertroutes.ErrNotFound) {
			t.Errorf("Get(%v, other org's route) = %v, want ErrNotFound", tc.org, err)
		}
	}
	list, err := s.List(ctx, orgA)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != ra.ID {
		t.Errorf("List(orgA) = %d routes, want exactly orgA's one", len(list))
	}
}

func TestStoreRejectsAForeignChannel(t *testing.T) {
	// The composite foreign key, not the application, is what makes this
	// impossible — so the test goes through the store and expects a
	// failure rather than trusting a Go-side check.
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)
	foreign := seedChannel(t, d, orgB)
	s := alertroutes.NewSQLStore(d)

	_, err := s.Create(ctx, orgA, uuid.New(), alertroutes.Input{
		Name: "leak", Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: []rules.State{rules.StateCrit},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{foreign},
	}, time.Now())
	if err == nil {
		t.Fatal("a route must not be able to reference another organization's channel")
	}
}

func TestStoreCreateRejectsADuplicateName(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)
	in := alertroutes.Input{
		Name: "dup", Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: []rules.State{rules.StateCrit},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{ch},
	}
	if _, err := s.Create(ctx, org, uuid.New(), in, time.Now()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := s.Create(ctx, org, uuid.New(), in, time.Now()); !errors.Is(err, alertroutes.ErrNameTaken) {
		t.Fatalf("second Create with the same name = %v, want ErrNameTaken", err)
	}
}

func TestStoreUpdateSucceedsAndMovesScopeFromOrgToHost(t *testing.T) {
	// Exercises both Update's own success path (Create's own round trip
	// only ever drives the stale-revision failure above) and
	// scopeColumns/scopeIDFromColumns' ScopeHost branch — every other test
	// in this file stays at ScopeOrg, which touches neither nullable
	// column at all.
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	chOld, chNew := seedChannel(t, d, org), seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)
	now := time.Now()

	created, err := s.Create(ctx, org, uuid.New(), alertroutes.Input{
		Name: "moves", Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: []rules.State{rules.StateWarn},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{chOld},
	}, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := s.Update(ctx, org, created.ID, created.Revision, alertroutes.Input{
		Name: "moved", Enabled: false, Scope: alertroutes.ScopeHost, ScopeID: &host,
		Severities: []rules.State{rules.StateCrit, rules.StateUnknown},
		Events:     []alerts.EventKind{alerts.EventClosed},
		Channels:   []uuid.UUID{chNew},
	}, now)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "moved" || updated.Enabled || updated.Scope != alertroutes.ScopeHost {
		t.Fatalf("Update() = %+v, want the new name, enabled=false and scope host", updated)
	}
	if updated.ScopeID == nil || *updated.ScopeID != host {
		t.Fatalf("Update() scope id = %v, want the host %v", updated.ScopeID, host)
	}
	if len(updated.Severities) != 2 || len(updated.Events) != 1 || len(updated.Channels) != 1 || updated.Channels[0] != chNew {
		t.Fatalf("Update() did not replace the selections: %+v", updated)
	}

	got, err := s.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.Scope != alertroutes.ScopeHost || got.ScopeID == nil || *got.ScopeID != host {
		t.Fatalf("Get() after Update = %+v, want the host scope to have persisted", got)
	}
}

func TestStoreScopeMonitoringGroupRoundTrips(t *testing.T) {
	// The one scope TestStoreRoundTrip and TestStoreUpdateSucceeds... do
	// not already cover: scopeColumns/scopeIDFromColumns' own
	// ScopeMonitoringGroup branch.
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	grp := seedMonitoringGroup(t, d, org)
	ch := seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)

	created, err := s.Create(ctx, org, uuid.New(), alertroutes.Input{
		Name: "group-scoped", Enabled: true, Scope: alertroutes.ScopeMonitoringGroup, ScopeID: &grp,
		Severities: []rules.State{rules.StateCrit},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{ch},
	}, time.Now())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Scope != alertroutes.ScopeMonitoringGroup || created.ScopeID == nil || *created.ScopeID != grp {
		t.Fatalf("Create() = %+v, want scope monitoring_group targeting %v", created, grp)
	}

	got, err := s.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Scope != alertroutes.ScopeMonitoringGroup || got.ScopeID == nil || *got.ScopeID != grp {
		t.Fatalf("Get() = %+v, want the group scope to round-trip", got)
	}
}

func TestStoreUpdateOnAMissingRouteIsNotFoundNotRevisionMismatch(t *testing.T) {
	// Update's own two zero-rows outcomes: a stale revision on a route
	// that DOES exist (TestStoreRoundTrip) versus an id that never existed
	// at all — the branch this test is the only one to exercise.
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	s := alertroutes.NewSQLStore(d)

	_, err := s.Update(ctx, org, uuid.New(), 1, alertroutes.Input{
		Name: "ghost", Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: []rules.State{rules.StateCrit},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{seedChannel(t, d, org)},
	}, time.Now())
	if !errors.Is(err, alertroutes.ErrNotFound) {
		t.Fatalf("Update(missing id) = %v, want ErrNotFound", err)
	}
}

func TestStoreDelete(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)

	created, err := s.Create(ctx, org, uuid.New(), alertroutes.Input{
		Name: "gone-soon", Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: []rules.State{rules.StateCrit},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{ch},
	}, time.Now())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Delete(ctx, org, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, org, created.ID); !errors.Is(err, alertroutes.ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, org, created.ID); !errors.Is(err, alertroutes.ErrNotFound) {
		t.Fatalf("Delete an already-deleted route = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, org, uuid.New()); !errors.Is(err, alertroutes.ErrNotFound) {
		t.Fatalf("Delete a route that never existed = %v, want ErrNotFound", err)
	}
}

func TestStoreRoundTripsTheWindow(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)
	now := time.Now()

	in := alertroutes.Input{
		Name: "business hours", Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities:   []rules.State{rules.StateCrit, rules.StateWarn},
		Events:       []alerts.EventKind{alerts.EventOpened},
		Channels:     []uuid.UUID{ch},
		Window:       &alertroutes.Window{Cron: "0 9 * * 1-5", Duration: 9 * time.Hour, Timezone: "Europe/Berlin"},
		Breakthrough: []rules.State{rules.StateCrit},
	}
	created, err := s.Create(ctx, org, uuid.New(), in, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Window == nil || got.Window.Cron != in.Window.Cron ||
		got.Window.Duration != in.Window.Duration || got.Window.Timezone != in.Window.Timezone {
		t.Errorf("window lost in the round trip: %+v", got.Window)
	}
	if len(got.Breakthrough) != 1 || got.Breakthrough[0] != rules.StateCrit {
		t.Errorf("breakthrough lost in the round trip: %v", got.Breakthrough)
	}
}

// TestStoreUpdateReplacesBreakthroughOverAnExistingRow exercises
// ReplaceRouteBreakthroughSeverities' WITH-based DELETE-then-INSERT CTE
// against a route whose breakthrough table already has a row — the shape
// this repo's own CHANGELOG records biting twice before: a data-modifying
// CTE's DELETE and the main INSERT have no specified execution order
// against each other when nothing selects from the CTE's output, which
// this query (its own doc comment says so) deliberately does not. The
// first Update below keeps "crit" in both the old and the new set, so the
// value is already present in the table when the CTE runs; the second
// drops it to nothing, exercising the table going fully empty the same
// way ReplaceRouteChannels' own comment calls out for an emptied channel
// list. A real database is the only thing that can prove the statement's
// actual behaviour, not just that it compiles (ReplaceRouteSeverities and
// ReplaceRouteEvents share the exact same shape and already round-trip
// through TestStoreUpdateSucceedsAndMovesScopeFromOrgToHost, but that test
// never re-uses a value across the replace the way this one does).
func TestStoreUpdateReplacesBreakthroughOverAnExistingRow(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)
	now := time.Now()
	win := &alertroutes.Window{Cron: "0 9 * * *", Duration: time.Hour, Timezone: "UTC"}

	in := alertroutes.Input{
		Name: "overlap", Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities:   []rules.State{rules.StateCrit, rules.StateWarn},
		Events:       []alerts.EventKind{alerts.EventOpened},
		Channels:     []uuid.UUID{ch},
		Window:       win,
		Breakthrough: []rules.State{rules.StateCrit, rules.StateWarn},
	}
	created, err := s.Create(ctx, org, uuid.New(), in, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// "crit" survives the replace unchanged; "warn" is dropped. If the
	// CTE's DELETE ran after the INSERT instead of before it, the surviving
	// "crit" row would either vanish (deleted after being re-inserted) or
	// the stale "warn" row would linger (never deleted because the INSERT
	// raced ahead of it) — either way Get below would catch it.
	in.Breakthrough = []rules.State{rules.StateCrit}
	updated, err := s.Update(ctx, org, created.ID, created.Revision, in, now)
	if err != nil {
		t.Fatalf("Update (overlap): %v", err)
	}
	if len(updated.Breakthrough) != 1 || updated.Breakthrough[0] != rules.StateCrit {
		t.Fatalf("Update() breakthrough = %v, want exactly [crit]", updated.Breakthrough)
	}

	// Then empty it out entirely — the table must end up with no rows for
	// this route at all, not a leftover "crit" the DELETE failed to reach.
	in.Breakthrough = nil
	in.Window = nil
	emptied, err := s.Update(ctx, org, created.ID, updated.Revision, in, now)
	if err != nil {
		t.Fatalf("Update (empty): %v", err)
	}
	if len(emptied.Breakthrough) != 0 {
		t.Fatalf("Update() breakthrough = %v, want none left", emptied.Breakthrough)
	}
	if emptied.Window != nil {
		t.Fatalf("Update() window = %+v, want nil after clearing it", emptied.Window)
	}

	got, err := s.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Breakthrough) != 0 || got.Window != nil {
		t.Fatalf("Get() after Update = breakthrough %v window %+v, want both cleared", got.Breakthrough, got.Window)
	}
}

// TestStoreRoundTripsSteps proves Route.Steps is actually read and written
// alongside the route's four selections (Task 5,
// docs/superpowers/plans/2026-10-16-v0.4d-escalation-chains.md):
// ReplaceSteps and ListEscalationStepsForRoutes both existed since Task 4,
// but nothing before this test exercised them through Create, Get, List and
// Update on a real Route rather than directly against the store's own
// escalation methods (escalate_sql_test.go).
func TestStoreRoundTripsSteps(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch, escalateCh := seedChannel(t, d, org), seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)
	now := time.Now()

	in := alertroutes.Input{
		Name: "ladder", Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: []rules.State{rules.StateCrit},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{ch},
		Steps: []alertroutes.Step{
			{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{escalateCh}},
			{Position: 2, Delay: time.Hour, ChannelIDs: []uuid.UUID{ch, escalateCh}},
		},
	}
	created, err := s.Create(ctx, org, uuid.New(), in, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.Steps) != 2 {
		t.Fatalf("Create() steps = %+v, want 2", created.Steps)
	}

	got, err := s.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Steps) != 2 ||
		got.Steps[0].Position != 1 || got.Steps[0].Delay != 5*time.Minute || len(got.Steps[0].ChannelIDs) != 1 ||
		got.Steps[1].Position != 2 || len(got.Steps[1].ChannelIDs) != 2 {
		t.Fatalf("Get() steps = %+v, want the ladder Create wrote", got.Steps)
	}

	list, err := s.List(ctx, org)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || len(list[0].Steps) != 2 {
		t.Fatalf("List() steps = %+v, want 2", list[0].Steps)
	}

	// Replacing the ladder with a shorter one must drop the removed rung,
	// not merely leave it stale alongside the new one — the same
	// delete-then-insert correctness
	// TestStoreUpdateReplacesBreakthroughOverAnExistingRow already pins for
	// the breakthrough table, pinned here for alert_route_steps too.
	in.Steps = []alertroutes.Step{{Position: 1, Delay: time.Minute, ChannelIDs: []uuid.UUID{ch}}}
	updated, err := s.Update(ctx, org, created.ID, created.Revision, in, now)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.Steps) != 1 || updated.Steps[0].Delay != time.Minute {
		t.Fatalf("Update() steps = %+v, want exactly the one replaced step", updated.Steps)
	}

	// Clearing the ladder entirely — a route that stops escalating — must
	// leave Steps empty, not a stale row DeleteRouteSteps somehow missed.
	in.Steps = nil
	cleared, err := s.Update(ctx, org, created.ID, updated.Revision, in, now)
	if err != nil {
		t.Fatalf("Update (clear): %v", err)
	}
	if len(cleared.Steps) != 0 {
		t.Fatalf("Update() steps = %+v, want none left", cleared.Steps)
	}
}

// TestStoreListEnabledFiltersDisabledRoutesAndCarriesSteps closes a gap that
// predates this task: ListEnabled had no test of its own at all before this
// one, for steps or anything else, despite being the one method Task 6's
// follower calls on every tick. ListEnabled shares its whole read path with
// List except for the enabled filter, so this pins exactly that
// difference — a disabled route absent, an enabled one present — together
// with the step-loading this task added to both, rather than two separate
// tests that would each only prove half of what ListEnabled actually needs
// to get right. beginTx (ledger_sql_test.go) is reused as-is: ListEnabled
// takes a caller-owned transaction, the same shape the follower's own tick
// runs it under.
func TestStoreListEnabledFiltersDisabledRoutesAndCarriesSteps(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)
	now := time.Now()

	mk := func(name string, enabled bool) alertroutes.Route {
		t.Helper()
		r, err := s.Create(ctx, org, uuid.New(), alertroutes.Input{
			Name: name, Enabled: enabled, Scope: alertroutes.ScopeOrg,
			Severities: []rules.State{rules.StateCrit},
			Events:     []alerts.EventKind{alerts.EventOpened},
			Channels:   []uuid.UUID{ch},
			Steps:      []alertroutes.Step{{Position: 1, Delay: time.Minute, ChannelIDs: []uuid.UUID{ch}}},
		}, now)
		if err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
		return r
	}
	on := mk("on", true)
	mk("off", false)

	tx := beginTx(t, d)
	got, err := s.ListEnabled(ctx, tx, org)
	if err != nil {
		t.Fatalf("ListEnabled: %v", err)
	}
	if len(got) != 1 || got[0].ID != on.ID {
		t.Fatalf("ListEnabled() = %+v, want exactly the one enabled route", got)
	}
	if len(got[0].Steps) != 1 || got[0].Steps[0].Delay != time.Minute {
		t.Fatalf("ListEnabled() steps = %+v, want the one step Create wrote", got[0].Steps)
	}
}

// Create and Update mapped a unique violation but not a foreign-key one,
// so alert_routes_rotation_fkey, alert_routes_scope_group_fkey and
// alert_routes_scope_host_fkey all surfaced unmapped and the API answered
// 500. Easy to reach without doing anything wrong: delete a rotation in
// one tab, save a route still naming it in another (whole-branch review,
// Minor #5). replaceSelections and oncall.CreateException already map it;
// the route row itself did not.
func TestStoreRejectsAnUnknownRotationOrScopeTarget(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)

	base := func() alertroutes.Input {
		return alertroutes.Input{
			Name: "route-" + uuid.NewString(), Enabled: true, Scope: alertroutes.ScopeOrg,
			Severities: []rules.State{rules.StateCrit},
			Events:     []alerts.EventKind{alerts.EventOpened},
			Channels:   []uuid.UUID{ch},
		}
	}

	gone := uuid.New()
	withRotation := base()
	withRotation.Channels = nil
	withRotation.RotationID = &gone
	if _, err := s.Create(ctx, org, uuid.New(), withRotation, time.Now()); !errors.Is(err, alertroutes.ErrInvalidInput) {
		t.Errorf("Create naming a rotation that no longer exists = %v, want ErrInvalidInput", err)
	}

	withHost := base()
	withHost.Scope, withHost.ScopeID = alertroutes.ScopeHost, &gone
	if _, err := s.Create(ctx, org, uuid.New(), withHost, time.Now()); !errors.Is(err, alertroutes.ErrInvalidInput) {
		t.Errorf("Create naming a host that does not exist = %v, want ErrInvalidInput", err)
	}

	withGroup := base()
	withGroup.Scope, withGroup.ScopeID = alertroutes.ScopeMonitoringGroup, &gone
	if _, err := s.Create(ctx, org, uuid.New(), withGroup, time.Now()); !errors.Is(err, alertroutes.ErrInvalidInput) {
		t.Errorf("Create naming a monitoring group that does not exist = %v, want ErrInvalidInput", err)
	}

	// The same hole on the update path, which is the one an operator
	// actually walks into: the route was saved while its rotation still
	// existed.
	saved := base()
	created, err := s.Create(ctx, org, uuid.New(), saved, time.Now())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stale := saved
	stale.Channels = nil
	stale.RotationID = &gone
	if _, err := s.Update(ctx, org, created.ID, created.Revision, stale, time.Now()); !errors.Is(err, alertroutes.ErrInvalidInput) {
		t.Errorf("Update naming a rotation that no longer exists = %v, want ErrInvalidInput", err)
	}
}
