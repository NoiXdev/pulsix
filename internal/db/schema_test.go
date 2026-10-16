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

// seedAlertRoute inserts one enabled, organization-wide route in org and
// returns its id — the minimal fixture the escalation tables' own
// composite FK to alert_routes needs; scope and name carry no meaning for
// the tests that call this.
func seedAlertRoute(t *testing.T, d *db.DB, org uuid.UUID) uuid.UUID {
	t.Helper()
	name := "r-" + uuid.Must(uuid.NewV7()).String()
	var id uuid.UUID
	err := d.Pool().QueryRow(context.Background(), `
		INSERT INTO alert_routes (id, org_id, name, enabled, scope, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, true, 'org', 1, now(), now())
		RETURNING id`, org, name).Scan(&id)
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

func TestOncallSchema(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	chA, chB := seedChannel(t, d, org), seedChannel(t, d, org)

	var rot uuid.UUID
	err := d.Pool().QueryRow(ctx, `
		INSERT INTO oncall_rotations (id, org_id, name, period_days, anchor, timezone, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'db team', 7, '2026-01-05 09:00:00+01', 'Europe/Berlin', 1, now(), now())
		RETURNING id`, org).Scan(&rot)
	if err != nil {
		t.Fatalf("insert rotation: %v", err)
	}

	// The period is whole days and must be positive: a zero or negative
	// cycle has no meaning and would divide by zero in ChannelAt.
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO oncall_rotations (id, org_id, name, period_days, anchor, timezone, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'bad', 0, now(), 'UTC', 1, now(), now())`, org); err == nil {
		t.Error("a period of zero days must be rejected")
	}

	// Channels are ordered, and the position is unique within a rotation.
	for i, ch := range []uuid.UUID{chA, chB} {
		if _, err := d.Pool().Exec(ctx, `
			INSERT INTO oncall_rotation_channels (org_id, rotation_id, position, channel_id)
			VALUES ($1, $2, $3, $4)`, org, rot, i, ch); err != nil {
			t.Fatalf("insert rotation channel %d: %v", i, err)
		}
	}
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO oncall_rotation_channels (org_id, rotation_id, position, channel_id)
		VALUES ($1, $2, 0, $3)`, org, rot, chB); err == nil {
		t.Error("two channels at one position must be rejected")
	}

	// An exception's span must be a real interval.
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO oncall_exceptions (id, org_id, rotation_id, starts_at, ends_at, channel_id, created_at)
		VALUES (gen_random_uuid(), $1, $2, now(), now(), $3, now())`, org, rot, chA); err == nil {
		t.Error("a zero-length exception span must be rejected")
	}

	// Deleting a channel takes it out of the rotation and out of any
	// exception naming it, rather than orphaning either.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM notification_channels WHERE id = $1`, chB); err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM oncall_rotation_channels WHERE rotation_id = $1`, rot).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rotation channels after one was deleted = %d, want 1", n)
	}

	// A deleted rotation must leave its routes standing with no channel
	// source, not take them with it: the route still names a scope and a
	// severity set someone configured, and the GUI's "reaches nobody"
	// indicator is how they find out it needs a new source.
	var route uuid.UUID
	if err := d.Pool().QueryRow(ctx, `
		INSERT INTO alert_routes (id, org_id, name, enabled, scope, rotation_id, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'via rotation', true, 'org', $2, 1, now(), now())
		RETURNING id`, org, rot).Scan(&route); err != nil {
		t.Fatalf("insert route with a rotation: %v", err)
	}
	if _, err := d.Pool().Exec(ctx, `DELETE FROM oncall_rotations WHERE id = $1`, rot); err != nil {
		t.Fatalf("delete rotation: %v", err)
	}
	var stillThere bool
	var src *uuid.UUID
	if err := d.Pool().QueryRow(ctx,
		`SELECT true, rotation_id FROM alert_routes WHERE id = $1`, route).Scan(&stillThere, &src); err != nil {
		t.Fatalf("the route must survive its rotation: %v", err)
	}
	if src != nil {
		t.Errorf("rotation_id after the rotation was deleted = %v, want NULL", src)
	}
}

func TestAlertRouteScheduleColumns(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)

	var route uuid.UUID
	if err := d.Pool().QueryRow(ctx, `
		INSERT INTO alert_routes (id, org_id, name, enabled, scope, window_cron, window_duration_seconds, window_timezone, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'business hours', true, 'org', '0 9 * * 1-5', 32400, 'Europe/Berlin', 1, now(), now())
		RETURNING id`, org).Scan(&route); err != nil {
		t.Fatalf("insert route with a window: %v", err)
	}

	// The three window columns are one fact: either all set or none.
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO alert_routes (id, org_id, name, enabled, scope, window_cron, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'half a window', true, 'org', '0 9 * * *', 1, now(), now())`, org); err == nil {
		t.Error("a window with a cron but no duration or timezone must be rejected")
	}

	// A route with no window at all is still legal — that is the default.
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO alert_routes (id, org_id, name, enabled, scope, revision, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'always', true, 'org', 1, now(), now())`, org); err != nil {
		t.Fatalf("a route without a window must stay legal: %v", err)
	}
}

func TestDeferredLedgerState(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	ev := seedAlertEvent(t, d, org, alert, "opened")

	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO alert_notify_state (org_id, alert_event_id, state, decided_at)
		VALUES ($1, $2, 'deferred', now())`, org, ev); err != nil {
		t.Fatalf("the ledger must accept 'deferred': %v", err)
	}
}

func TestVaultPasswordCredentialAndRepoReference(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)

	// The fifth kind. Ansible Vault is the one secret Pulsix could not
	// hold: the worker writes its own ansible.cfg and sets ANSIBLE_CONFIG
	// to it, so a vault_password_file in the repository's own config is
	// ignored, and nothing ever passed --vault-password-file. A vault
	// marker therefore reached Ansible and Ansible could not decrypt it.
	var cred uuid.UUID
	if err := d.Pool().QueryRow(ctx, `
		INSERT INTO credentials (id, org_id, name, kind, secret, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'prod vault', 'vault_password', '\x00'::bytea, now(), now())
		RETURNING id`, org).Scan(&cred); err != nil {
		t.Fatalf("insert vault_password credential: %v", err)
	}

	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO credentials (id, org_id, name, kind, secret, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'nonsense', 'vault_key', '\x00'::bytea, now(), now())`, org); err == nil {
		t.Error("an unknown credential kind must still be rejected")
	}

	// A repository names its vault password separately from the
	// credential that reaches the repository itself: one gets at the
	// repository, the other at its contents.
	var repo uuid.UUID
	if err := d.Pool().QueryRow(ctx, `
		INSERT INTO git_repos (id, org_id, name, url, vault_credential_id)
		VALUES (gen_random_uuid(), $1, 'infra', 'https://example.test/infra.git', $2)
		RETURNING id`, org, cred).Scan(&repo); err != nil {
		t.Fatalf("insert repo with a vault credential: %v", err)
	}

	// Another organization's credential cannot be named, the same way the
	// access credential already cannot be.
	other := seedOrg(t, d)
	var foreign uuid.UUID
	if err := d.Pool().QueryRow(ctx, `
		INSERT INTO credentials (id, org_id, name, kind, secret, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'their vault', 'vault_password', '\x00'::bytea, now(), now())
		RETURNING id`, other).Scan(&foreign); err != nil {
		t.Fatalf("insert foreign credential: %v", err)
	}
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO git_repos (id, org_id, name, url, vault_credential_id)
		VALUES (gen_random_uuid(), $1, 'leak', 'https://example.test/x.git', $2)`, org, foreign); err == nil {
		t.Error("a repository must not name another organization's vault credential")
	}

	// A credential a repository still points at cannot be deleted out
	// from under it — the same guarantee the access credential has.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM credentials WHERE id = $1`, cred); err == nil {
		t.Error("deleting a vault credential a repository still names must be refused")
	}
}

func TestEscalationTablesEnforceTenancy(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)

	routeA := seedAlertRoute(t, d, orgA)
	// A step belongs to its route's organization. The composite foreign
	// key is what makes the cross-tenant version of this impossible
	// rather than merely unused.
	_, err := d.Pool().Exec(ctx,
		`INSERT INTO alert_route_steps (org_id, route_id, position, delay_seconds)
		 VALUES ($1, $2, 1, 300)`, orgB, routeA)
	if err == nil {
		t.Fatal("inserted a step into another organization's route")
	}
}

func TestAlertEventsAcceptEscalated(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)

	// The seventh kind was the last one 00049 allowed. 'escalated' is the
	// eighth, and a chain that cannot write its own event is invisible on
	// the alert page.
	if _, err := d.Pool().Exec(ctx,
		`INSERT INTO alert_events (id, org_id, alert_id, at, kind, payload)
		 VALUES (gen_random_uuid(), $1, $2, now(), 'escalated', '{"position":1}'::jsonb)`,
		org, alert); err != nil {
		t.Fatalf("alert_events rejected kind 'escalated': %v", err)
	}
}
