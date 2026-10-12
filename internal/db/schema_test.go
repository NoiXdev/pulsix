// Package db_test exercises migration 00052 directly, through raw SQL
// against the bare pool — there is no alert-routes Go package yet (a later
// task adds the domain types and store), so these tests are the only thing
// proving the schema itself is shaped the way every later task's SQL
// assumes, the same posture internal/alerts/schema_test.go already takes
// for migration 00049.
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
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

// seedHost creates a bare zone and host in org — alert_routes' own
// scope_host_id composite FK needs a real host to point at.
func seedHost(t *testing.T, d *db.DB, org uuid.UUID) uuid.UUID {
	t.Helper()
	q := gen.New(d.Pool())
	name := "h-" + uuid.Must(uuid.NewV7()).String()
	zone, err := q.CreateZone(context.Background(), gen.CreateZoneParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: name, Name: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := q.CreateHost(context.Background(), gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: name, Facts: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return host.ID
}

// seedMonitoringGroup creates a bare monitoring group in org —
// alert_routes' own scope_group_id composite FK needs a real group to
// point at.
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

// seedOpenAlert inserts one open, host-scoped alert for host — the minimal
// fixture alert_notify_state's composite chain (alert_events -> alerts)
// needs; the scope and severity values themselves carry no meaning for the
// tests that call this.
func seedOpenAlert(t *testing.T, d *db.DB, org, host uuid.UUID) uuid.UUID {
	t.Helper()
	id, err := gen.New(d.Pool()).InsertAlert(context.Background(), gen.InsertAlertParams{
		OrgID: org, HostID: host, Scope: "host", Severity: "warn",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedAlertEvent inserts one event of the given kind for alert, and returns
// its id — alert_notify_state.alert_event_id is a foreign key straight to
// this table's primary key, not a generated query (InsertAlertEvent carries
// no RETURNING, alerts.sql's own comment explains why: nothing downstream
// reads the id there), so this helper issues the INSERT directly.
func seedAlertEvent(t *testing.T, d *db.DB, org, alert uuid.UUID, kind string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := d.Pool().QueryRow(context.Background(), `
		INSERT INTO alert_events (org_id, alert_id, at, kind)
		VALUES ($1, $2, now(), $3)
		RETURNING id`, org, alert, kind).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAlertRoutesSchema(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()

	org := seedOrg(t, d)
	ch := seedChannel(t, d, org)
	host := seedHost(t, d, org)
	grp := seedMonitoringGroup(t, d, org)

	var id uuid.UUID
	err := d.Pool().QueryRow(ctx, `
		INSERT INTO alert_routes (id, org_id, name, enabled, scope, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'ops', true, 'org', 1, now(), now())
		RETURNING id`, org).Scan(&id)
	if err != nil {
		t.Fatalf("insert route: %v", err)
	}

	// A second route may not take the same name in the same organization.
	_, err = d.Pool().Exec(ctx, `
		INSERT INTO alert_routes (id, org_id, name, enabled, scope, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'ops', true, 'org', 1, now(), now())`, org)
	if err == nil {
		t.Error("duplicate route name in one org must be rejected")
	}

	// An organization-wide route carries no target at all.
	_, err = d.Pool().Exec(ctx, `
		INSERT INTO alert_routes (id, org_id, name, enabled, scope, scope_host_id, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'bad', true, 'org', $2, 1, now(), now())`, org, host)
	if err == nil {
		t.Error("scope 'org' with a target must be rejected")
	}

	// A scoped route must carry exactly one target, not both.
	_, err = d.Pool().Exec(ctx, `
		INSERT INTO alert_routes (id, org_id, name, enabled, scope, scope_group_id, scope_host_id, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'both', true, 'host', $2, $3, 1, now(), now())`, org, grp, host)
	if err == nil {
		t.Error("a route carrying both a group and a host target must be rejected")
	}

	// The spec's promise: a route dies with the thing it points at,
	// through a real foreign key rather than application discipline.
	var scoped uuid.UUID
	if err := d.Pool().QueryRow(ctx, `
		INSERT INTO alert_routes (id, org_id, name, enabled, scope, scope_host_id, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'just this host', true, 'host', $2, 1, now(), now())
		RETURNING id`, org, host).Scan(&scoped); err != nil {
		t.Fatalf("insert host-scoped route: %v", err)
	}
	if _, err := d.Pool().Exec(ctx, `DELETE FROM hosts WHERE id = $1`, host); err != nil {
		t.Fatalf("delete host: %v", err)
	}
	var left int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alert_routes WHERE id = $1`, scoped).Scan(&left); err != nil {
		t.Fatalf("count routes: %v", err)
	}
	if left != 0 {
		t.Errorf("routes after their host was deleted = %d, want 0", left)
	}

	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO alert_route_channels (org_id, route_id, channel_id) VALUES ($1, $2, $3)`,
		org, id, ch); err != nil {
		t.Fatalf("link channel: %v", err)
	}

	// Deleting the channel removes the link rather than orphaning it.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM notification_channels WHERE id = $1`, ch); err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alert_route_channels WHERE route_id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if n != 0 {
		t.Errorf("links after channel delete = %d, want 0", n)
	}
}

func TestAlertNotifyStateSchema(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()

	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	ev := seedAlertEvent(t, d, org, alert, "opened")

	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO alert_notify_state (org_id, alert_event_id, state, decided_at)
		VALUES ($1, $2, 'sent', now())`, org, ev); err != nil {
		t.Fatalf("insert ledger row: %v", err)
	}

	// Only the four states exist.
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO alert_notify_state (org_id, alert_event_id, state, decided_at)
		VALUES ($1, gen_random_uuid(), 'maybe', now())`, org); err == nil {
		t.Error("an unknown ledger state must be rejected")
	}

	// The ledger ages out with the history it describes.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM alert_events WHERE id = $1`, ev); err != nil {
		t.Fatalf("delete event: %v", err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alert_notify_state WHERE alert_event_id = $1`, ev).Scan(&n); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if n != 0 {
		t.Errorf("ledger rows after event delete = %d, want 0", n)
	}
}
