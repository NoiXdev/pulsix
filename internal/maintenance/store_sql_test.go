package maintenance_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/maintenance"
)

// seedHost creates an organization with one zone and one host.
func seedHost(t *testing.T, d *db.DB, slug string) (org, host uuid.UUID) {
	t.Helper()
	q := gen.New(d.Pool())
	o, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	zone, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'z', 'z')`, zone, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `INSERT INTO hosts (id, org_id, zone_id, name, managed) VALUES ($1, $2, $3, 'web-1', true)`, host, o.ID, zone); err != nil {
		t.Fatal(err)
	}
	return o.ID, host
}

// seedMonitoringGroup inserts a monitoring group directly, the same way
// seedHost seeds its zone and host.
func seedMonitoringGroup(t *testing.T, d *db.DB, org uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx, `INSERT INTO monitoring_groups (id, org_id, name, selector) VALUES ($1, $2, $3, 'tag:x')`, id, org, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWindowStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d, "maint-store")
	otherOrg, _ := seedHost(t, d, "maint-other")
	st := maintenance.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	start, end := at("2026-09-22T10:00:00Z"), at("2026-09-22T12:00:00Z")
	oneOff, err := st.Create(scoped, maintenance.Window{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "db upgrade", TargetKind: maintenance.TargetHost, TargetID: &host,
		StartsAt: &start, EndsAt: &end, Note: "kernel 6.12",
	})
	if err != nil {
		t.Fatal(err)
	}
	if oneOff.TargetName != "web-1" || *oneOff.TargetID != host || !oneOff.EndsAt.Equal(end) || oneOff.Revision != 1 || oneOff.Recurring() {
		t.Fatalf("one-off = %+v", oneOff)
	}
	// A Window built without a Reason — as every window before this
	// migration existed — still reads back as "maintenance".
	if oneOff.Reason != maintenance.ReasonMaintenance {
		t.Fatalf("reason = %q, want %q", oneOff.Reason, maintenance.ReasonMaintenance)
	}
	silence, err := st.Create(scoped, maintenance.Window{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "silence web-1", TargetKind: maintenance.TargetHost, TargetID: &host,
		Reason: maintenance.ReasonSilence, StartsAt: &start, EndsAt: &end,
	})
	if err != nil || silence.Reason != maintenance.ReasonSilence {
		t.Fatalf("silence = %+v, %v", silence, err)
	}
	group := seedMonitoringGroup(t, d, org, "web tier")
	groupWindow, err := st.Create(scoped, maintenance.Window{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "silence web tier", TargetKind: maintenance.TargetMonitoringGroup, TargetID: &group,
		Reason: maintenance.ReasonSilence, StartsAt: &start, EndsAt: &end,
	})
	if err != nil || groupWindow.TargetName != "web tier" || groupWindow.TargetID == nil || *groupWindow.TargetID != group {
		t.Fatalf("monitoring-group window = %+v, %v", groupWindow, err)
	}
	ghostGroup := uuid.Must(uuid.NewV7())
	if _, err := st.Create(scoped, maintenance.Window{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "ghost group", TargetKind: maintenance.TargetMonitoringGroup, TargetID: &ghostGroup,
		StartsAt: &start, EndsAt: &end}); !errors.Is(err, maintenance.ErrInvalidInput) {
		t.Fatalf("unknown monitoring group = %v", err)
	}
	recurring, err := st.Create(scoped, maintenance.Window{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "nightly", TargetKind: maintenance.TargetOrg,
		Cron: "0 2 * * *", Duration: 90 * time.Minute, Timezone: "Europe/Berlin",
	})
	if err != nil || recurring.Duration != 90*time.Minute || recurring.Timezone != "Europe/Berlin" || recurring.TargetID != nil || recurring.StartsAt != nil {
		t.Fatalf("recurring = %+v, %v", recurring, err)
	}
	if _, err := st.Create(scoped, maintenance.Window{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "nightly", TargetKind: maintenance.TargetOrg,
		Cron: "@daily", Duration: time.Hour, Timezone: "UTC"}); !errors.Is(err, maintenance.ErrNameTaken) {
		t.Fatalf("duplicate name = %v", err)
	}
	ghost := uuid.Must(uuid.NewV7())
	if _, err := st.Create(scoped, maintenance.Window{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "ghost", TargetKind: maintenance.TargetGroup, TargetID: &ghost,
		StartsAt: &start, EndsAt: &end}); !errors.Is(err, maintenance.ErrInvalidInput) {
		t.Fatalf("unknown group = %v", err)
	}
	// Another organization's scope sees nothing.
	if list, err := st.List(db.WithOrgScope(ctx, otherOrg), org); err != nil || len(list) != 0 {
		t.Fatalf("cross-org list = %v, %v", list, err)
	}
	recurring.Name = "nightly patching"
	up, err := st.Update(scoped, recurring, 1)
	if err != nil || up.Name != "nightly patching" || up.Revision != 2 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := st.Update(scoped, recurring, 1); !errors.Is(err, maintenance.ErrRevisionMismatch) {
		t.Fatalf("stale update = %v", err)
	}
	recurring.ID = uuid.Must(uuid.NewV7())
	if _, err := st.Update(scoped, recurring, 1); !errors.Is(err, maintenance.ErrNotFound) {
		t.Fatalf("unknown update = %v", err)
	}
	// Deleting the host deletes its windows.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM hosts WHERE id = $1`, host); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(scoped, org, oneOff.ID); !errors.Is(err, maintenance.ErrNotFound) {
		t.Fatalf("window of a deleted host = %v", err)
	}
	if err := st.Delete(scoped, org, up.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(scoped, org, up.ID); !errors.Is(err, maintenance.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

// TestMonitoringGroupKindColumnPairingIsEnforced holds the database to the
// same pairing invariant migration 00032 already gives host_id and
// group_id: a row cannot say target_kind = 'monitoring_group' while
// leaving monitoring_group_id NULL. normalize() guards the one path the
// app uses to write a window; this guards the table itself — a hand-edited
// row, or a future caller that bypasses normalize() — so the test goes
// around the store and normalize() on purpose, straight to SQL.
func TestMonitoringGroupKindColumnPairingIsEnforced(t *testing.T) {
	d := testdb.Open(t)
	org, _ := seedHost(t, d, "maint-kind-pairing")
	start, end := at("2026-09-22T10:00:00Z"), at("2026-09-22T12:00:00Z")
	_, err := d.Pool().Exec(ctx,
		`INSERT INTO maintenance_windows (id, org_id, name, target_kind, starts_at, ends_at)
		 VALUES ($1, $2, 'bad monitoring group window', 'monitoring_group', $3, $4)`,
		uuid.Must(uuid.NewV7()), org, start, end)
	if !isCheckViolation(err) {
		t.Fatalf("err = %v, want a check_violation", err)
	}
}

// isCheckViolation reports SQLSTATE 23514 (check_violation) — the same
// helper internal/rules and internal/items use for the same reason: db
// package only exposes helpers for unique (23505) and foreign key (23503)
// violations, neither of which is what a deliberately-mismatched row hits.
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// TestUpdateRetargetsAcrossTargetKinds is backlog 4.23. Create was
// covered for the monitoring-group target and Update was not — and
// Update is where the risk actually sits: the three target columns are
// mutually exclusive, so retargeting a window has to SET all three, the
// two it is leaving to NULL and the one it is moving to. A statement
// that only ever set the column it needed would leave the old one
// standing, and the row would then claim two targets at once, with
// Get's own LEFT JOINs happily resolving whichever it reads first.
//
// The round trip the row asked for is the weaker half of this: a
// monitoring-group window created and updated in place would never
// exercise the clearing at all. Each leg here changes the KIND.
func TestUpdateRetargetsAcrossTargetKinds(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d, "maint-retarget")
	st := maintenance.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	start, end := time.Now().UTC().Add(time.Hour).Truncate(time.Second), time.Now().UTC().Add(3*time.Hour).Truncate(time.Second)
	mgroup := seedMonitoringGroup(t, d, org, "web tier")

	w, err := st.Create(scoped, maintenance.Window{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "retarget", TargetKind: maintenance.TargetHost, TargetID: &host,
		Reason: maintenance.ReasonSilence, StartsAt: &start, EndsAt: &end,
	})
	if err != nil {
		t.Fatal(err)
	}

	// host → monitoring group
	w.TargetKind, w.TargetID = maintenance.TargetMonitoringGroup, &mgroup
	w, err = st.Update(scoped, w, w.Revision)
	if err != nil {
		t.Fatalf("retarget to a monitoring group: %v", err)
	}
	if w.TargetKind != maintenance.TargetMonitoringGroup || w.TargetID == nil || *w.TargetID != mgroup || w.TargetName != "web tier" {
		t.Fatalf("after retarget = %+v, want the monitoring group named", w)
	}
	if w.Reason != maintenance.ReasonSilence {
		t.Fatalf("reason = %q, want the silence to survive a retarget", w.Reason)
	}
	assertOneTargetColumn(t, d, w.ID, "monitoring_group_id")

	// monitoring group → the whole organization, which clears all three
	w.TargetKind, w.TargetID = maintenance.TargetOrg, nil
	w, err = st.Update(scoped, w, w.Revision)
	if err != nil {
		t.Fatalf("retarget to the organization: %v", err)
	}
	if w.TargetKind != maintenance.TargetOrg || w.TargetID != nil || w.TargetName != "" {
		t.Fatalf("after retarget to org = %+v, want no target at all", w)
	}
	assertOneTargetColumn(t, d, w.ID, "")

	// An unknown monitoring group is refused on Update exactly as on
	// Create — the foreign key is the same one.
	w.TargetKind, w.TargetID = maintenance.TargetMonitoringGroup, ptrUUID(uuid.Must(uuid.NewV7()))
	if _, err := st.Update(scoped, w, w.Revision); !errors.Is(err, maintenance.ErrInvalidInput) {
		t.Fatalf("retarget to an unknown monitoring group = %v, want ErrInvalidInput", err)
	}
}

// assertOneTargetColumn checks that exactly the named target column is
// set on the row and the other two are NULL; an empty name means all
// three must be NULL. The DTO cannot show this — Get resolves one
// TargetID out of whichever column matches the kind — so the row itself
// is the only place a leftover is visible.
func assertOneTargetColumn(t *testing.T, d *db.DB, id uuid.UUID, want string) {
	t.Helper()
	var hostID, groupID, monitoringGroupID *uuid.UUID
	if err := d.Pool().QueryRow(ctx,
		`SELECT host_id, group_id, monitoring_group_id FROM maintenance_windows WHERE id = $1`, id,
	).Scan(&hostID, &groupID, &monitoringGroupID); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]*uuid.UUID{"host_id": hostID, "group_id": groupID, "monitoring_group_id": monitoringGroupID} {
		if name == want {
			if got == nil {
				t.Errorf("%s is NULL, want it set", name)
			}
			continue
		}
		if got != nil {
			t.Errorf("%s = %s, want NULL — a retarget must clear the column it left", name, got)
		}
	}
}

func ptrUUID(v uuid.UUID) *uuid.UUID { return &v }
