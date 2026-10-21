// Package db_test exercises migration 00052 directly, through raw SQL
// against the bare pool — there is no alert-routes Go package yet (a later
// task adds the domain types and store), so these tests are the only thing
// proving the schema itself is shaped the way every later task's SQL
// assumes, the same posture internal/alerts/schema_test.go already takes
// for migration 00049.
package db_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

// seedVaultCredential inserts one vault_password credential in org and
// returns its id — the kind 00055 added, which the vault identity link
// tables' own composite foreign key needs something of to point at (the
// schema itself does not check kind at the link table, only existence and
// tenancy).
func seedVaultCredential(t *testing.T, d *db.DB, org uuid.UUID, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := d.Pool().QueryRow(context.Background(), `
		INSERT INTO credentials (id, org_id, name, kind, secret, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, 'vault_password', '\x00'::bytea, now(), now())
		RETURNING id`, org, name).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedGitRepo inserts a bare repository in org, with no vault credential of
// its own — git_repo_vault_credentials' own composite FK needs a real
// repository to point at, and the name carries no meaning for the tests
// that call this.
func seedGitRepo(t *testing.T, d *db.DB, org uuid.UUID, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := d.Pool().QueryRow(context.Background(), `
		INSERT INTO git_repos (id, org_id, name, url)
		VALUES (gen_random_uuid(), $1, $2, 'https://example.test/' || $2 || '.git')
		RETURNING id`, org, name).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedJobTemplate inserts a bare job template in org, through a throwaway
// editor playbook (job_templates.playbook_id is NOT NULL) —
// job_template_vault_credentials' own composite FK needs a real template to
// point at.
func seedJobTemplate(t *testing.T, d *db.DB, org uuid.UUID, name string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var playbookID uuid.UUID
	if err := d.Pool().QueryRow(ctx, `
		INSERT INTO playbooks (id, org_id, source, inline)
		VALUES (gen_random_uuid(), $1, 'editor', '- hosts: all\n  tasks: []\n')
		RETURNING id`, org).Scan(&playbookID); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := d.Pool().QueryRow(ctx, `
		INSERT INTO job_templates (id, org_id, name, playbook_id, scope)
		VALUES (gen_random_uuid(), $1, $2, $3, '{}'::jsonb)
		RETURNING id`, org, name, playbookID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestGitRepoVaultCredentialsSchema pins migration 00059's first table
// against the two schema facts the spec's Data section exists to make
// facts rather than service rules: a label may not repeat under one
// repository, and a link row may not name another organization's
// credential. It also pins the deliberate foreign-key asymmetry the spec
// calls out by name — the parent cascades, the credential does not — the
// same guarantee git_repos.vault_credential_id (00055) already had, now
// re-proven for the table that replaces it.
func TestGitRepoVaultCredentialsSchema(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	repo := seedGitRepo(t, d, org, "infra")
	credA := seedVaultCredential(t, d, org, "prod vault")
	credB := seedVaultCredential(t, d, org, "staging vault")

	// The same credential may sit behind two different labels — the spec's
	// own example is "prod" in one repository and "production" in another,
	// because the label describes what was typed at encryption time, not
	// the password itself.
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO git_repo_vault_credentials (org_id, repo_id, credential_id, label)
		VALUES ($1, $2, $3, 'prod')`, org, repo, credA); err != nil {
		t.Fatalf("insert first label: %v", err)
	}
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO git_repo_vault_credentials (org_id, repo_id, credential_id, label)
		VALUES ($1, $2, $3, 'staging')`, org, repo, credA); err != nil {
		t.Fatalf("same credential under a second label: %v", err)
	}

	// But a label may not repeat under one repository — PRIMARY KEY
	// (repo_id, label) is what makes "one password per label" a schema
	// fact rather than a service rule.
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO git_repo_vault_credentials (org_id, repo_id, credential_id, label)
		VALUES ($1, $2, $3, 'prod')`, org, repo, credB); err == nil {
		t.Error("a repeated label under one repository must be rejected")
	}

	// A link row may not name another organization's credential — the
	// composite foreign key, the same guarantee the access credential
	// beside it already has.
	other := seedOrg(t, d)
	foreignCred := seedVaultCredential(t, d, other, "their vault")
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO git_repo_vault_credentials (org_id, repo_id, credential_id, label)
		VALUES ($1, $2, $3, 'leak')`, org, repo, foreignCred); err == nil {
		t.Error("a link row must not name another organization's credential")
	}

	// The asymmetry the spec calls deliberate: the credential a link still
	// names cannot be deleted out from under it...
	if _, err := d.Pool().Exec(ctx, `DELETE FROM credentials WHERE id = $1`, credA); err == nil {
		t.Error("deleting a vault credential a repository still names must be refused")
	}
	// ...but deleting the repository takes its links with it.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM git_repos WHERE id = $1`, repo); err != nil {
		t.Fatalf("delete repo: %v", err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM git_repo_vault_credentials WHERE repo_id = $1`, repo).Scan(&n); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if n != 0 {
		t.Errorf("links after repo delete = %d, want 0", n)
	}
}

// TestJobTemplateVaultCredentialsSchema re-proves the label-uniqueness and
// cross-tenant guarantees above hold for the second table too — it is "the
// same shape" (spec's own words) rather than a copy a future edit could
// drift out of sync with unnoticed.
func TestJobTemplateVaultCredentialsSchema(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	tmpl := seedJobTemplate(t, d, org, "deploy")
	cred := seedVaultCredential(t, d, org, "team vault")

	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO job_template_vault_credentials (org_id, template_id, credential_id, label)
		VALUES ($1, $2, $3, 'prod')`, org, tmpl, cred); err != nil {
		t.Fatalf("insert link: %v", err)
	}

	// A label may not repeat under one template, the same primary key as
	// the repository table.
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO job_template_vault_credentials (org_id, template_id, credential_id, label)
		VALUES ($1, $2, $3, 'prod')`, org, tmpl, cred); err == nil {
		t.Error("a repeated label under one template must be rejected")
	}

	// A link row may not name another organization's credential.
	other := seedOrg(t, d)
	foreignCred := seedVaultCredential(t, d, other, "their vault")
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO job_template_vault_credentials (org_id, template_id, credential_id, label)
		VALUES ($1, $2, $3, 'leak')`, org, tmpl, foreignCred); err == nil {
		t.Error("a link row must not name another organization's credential")
	}

	// The same deliberate asymmetry as the repository table: the credential
	// a link still names cannot be deleted out from under it...
	if _, err := d.Pool().Exec(ctx, `DELETE FROM credentials WHERE id = $1`, cred); err == nil {
		t.Error("deleting a vault credential a template still names must be refused")
	}
	// ...but deleting the template takes its links with it. Without this
	// pair, a future edit dropping ON DELETE CASCADE here (or adding it to
	// the credential side) would pass every other test in this file.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM job_templates WHERE id = $1`, tmpl); err != nil {
		t.Fatalf("delete template: %v", err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM job_template_vault_credentials WHERE template_id = $1`, tmpl).Scan(&n); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if n != 0 {
		t.Errorf("links after template delete = %d, want 0", n)
	}
}

// backfillStatement extracts the git_repo_vault_credentials backfill INSERT
// out of 00059_vault_identities.sql's own text, between the two markers that
// bound it in the migration file, so TestVaultCredentialBackfillStatement
// below runs the real statement rather than a hand-copied one that could
// drift from it unnoticed.
func backfillStatement(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("migrations/00059_vault_identities.sql")
	if err != nil {
		t.Fatalf("read migration file: %v", err)
	}
	const start = "INSERT INTO git_repo_vault_credentials (org_id, repo_id, credential_id, label)"
	i := strings.Index(string(src), start)
	if i < 0 {
		t.Fatalf("backfill INSERT not found in migration file")
	}
	rest := string(src)[i:]
	end := strings.Index(rest, ";")
	if end < 0 {
		t.Fatalf("backfill INSERT has no terminating ';' in migration file")
	}
	return rest[:end]
}

// TestVaultCredentialBackfillStatement pins migration 00059's backfill
// statement directly, standing in for "seed before migrating, then
// migrate". testdb.Open cannot do that: start() in internal/db/testdb
// migrates the one shared container once per test binary before any test's
// fixtures exist, and offers no hook to seed a row first — see that
// package's own comment on why (migrating once per binary, not once per
// test, is a deliberate cost tradeoff). Running the exact INSERT ... SELECT
// the migration file uses, over a row this test seeds itself with the
// pre-v0.2e column already set, proves the statement is correct without a
// second, unmigrated database — the thing Review Focus 4 asks to protect:
// an existing installation's vault password must survive the upgrade as
// the link labelled 'default'.
func TestVaultCredentialBackfillStatement(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	cred := seedVaultCredential(t, d, org, "legacy vault")
	repo := seedGitRepo(t, d, org, "legacy")

	// The pre-v0.2e shape: a repository naming its one vault password
	// through the column 00055 added, exactly as every installation
	// upgrading from before this migration has one.
	if _, err := d.Pool().Exec(ctx,
		`UPDATE git_repos SET vault_credential_id = $1 WHERE id = $2`, cred, repo); err != nil {
		t.Fatalf("set legacy vault column: %v", err)
	}

	// Read straight out of the migration file on disk rather than retyping
	// it, so this test cannot silently drift from the statement the
	// migration actually runs: internal/db's own embed.FS is unexported, so
	// this package (db_test) cannot read through it, but go test's working
	// directory is the package directory, and the migration file lives at
	// the relative path below either way.
	if _, err := d.Pool().Exec(ctx, backfillStatement(t)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var label string
	var gotCred uuid.UUID
	if err := d.Pool().QueryRow(ctx,
		`SELECT label, credential_id FROM git_repo_vault_credentials WHERE repo_id = $1`, repo).
		Scan(&label, &gotCred); err != nil {
		t.Fatalf("read backfilled link: %v", err)
	}
	if label != "default" || gotCred != cred {
		t.Errorf("backfilled link = (%s, %s), want (default, %s)", label, gotCred, cred)
	}

	var n int
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM git_repo_vault_credentials WHERE repo_id = $1`, repo).Scan(&n); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if n != 1 {
		t.Errorf("link rows for the repo = %d, want exactly 1", n)
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

// TestSharedReposSchema pins migration 00060's split between installation
// data and tenant data (spec decision 1): shared_repos is installation-
// level, so it must NOT have org_id and must NOT carry row-level security
// — both asserted here, because a later migration copying a neighbour's
// policy block onto it would silently make the table invisible to every
// organization at once, the same failure mode TestEveryOrgOwnedTableForces
// RowLevelSecurity (rls_catalog_test.go) exists to catch in the other
// direction. git_repos.shared_repo_id is checked separately: nullable,
// because a git_repos row that is not a projection has no definition to
// point at, and ON DELETE RESTRICT, because deleting a definition out from
// under a customer's schedules is what spec decision 5 refuses.
func TestSharedReposSchema(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()

	var hasOrgID bool
	if err := d.Pool().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			WHERE c.relname = 'shared_repos' AND a.attname = 'org_id' AND NOT a.attisdropped
		)`).Scan(&hasOrgID); err != nil {
		t.Fatalf("check org_id column: %v", err)
	}
	if hasOrgID {
		t.Error("shared_repos must not have an org_id column; it is installation-level, not tenant data")
	}

	var rls bool
	if err := d.Pool().QueryRow(ctx, `
		SELECT c.relrowsecurity FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		WHERE c.relname = 'shared_repos'`).Scan(&rls); err != nil {
		t.Fatalf("check relrowsecurity: %v", err)
	}
	if rls {
		t.Error("shared_repos must not carry row-level security; it is visible to every organization by design")
	}

	var nullable string
	if err := d.Pool().QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_name = 'git_repos' AND column_name = 'shared_repo_id'`).Scan(&nullable); err != nil {
		t.Fatalf("check git_repos.shared_repo_id exists: %v", err)
	}
	if nullable != "YES" {
		t.Errorf("git_repos.shared_repo_id is_nullable = %q, want YES — a non-projecting row names no definition", nullable)
	}

	var deleteRule string
	if err := d.Pool().QueryRow(ctx, `
		SELECT confdeltype FROM pg_constraint
		WHERE conrelid = 'git_repos'::regclass AND confrelid = 'shared_repos'::regclass`).Scan(&deleteRule); err != nil {
		t.Fatalf("check git_repos.shared_repo_id foreign key: %v", err)
	}
	if deleteRule != "r" {
		t.Errorf("git_repos.shared_repo_id foreign key confdeltype = %q, want %q (restrict) — "+
			"deleting a definition still projected into an organization must be refused, not cascade", deleteRule, "r")
	}
}

// TestSharedJobTemplatesSchema is TestSharedReposSchema's own test for
// migration 00061, the second application of the mechanism: shared_job_templates
// must carry neither org_id nor row-level security (installation-level
// data, the same posture shared_repos has had since 00060); job_templates.
// shared_template_id must be nullable with ON DELETE RESTRICT (spec
// decision 5, reported as ErrInUse before the database ever has to); and
// job_templates.playbook_id — NOT NULL since migration 00018, for every
// template this schema has ever held before this one — must have become
// nullable, the one column this migration loosens rather than adds
// (migration 00061's own comment: a projection may be written before its
// organization has synced a matching playbook).
func TestSharedJobTemplatesSchema(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()

	var hasOrgID bool
	if err := d.Pool().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			WHERE c.relname = 'shared_job_templates' AND a.attname = 'org_id' AND NOT a.attisdropped
		)`).Scan(&hasOrgID); err != nil {
		t.Fatalf("check org_id column: %v", err)
	}
	if hasOrgID {
		t.Error("shared_job_templates must not have an org_id column; it is installation-level, not tenant data")
	}

	var rls bool
	if err := d.Pool().QueryRow(ctx, `
		SELECT c.relrowsecurity FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		WHERE c.relname = 'shared_job_templates'`).Scan(&rls); err != nil {
		t.Fatalf("check relrowsecurity: %v", err)
	}
	if rls {
		t.Error("shared_job_templates must not carry row-level security; it is visible to every organization by design")
	}

	var sharedRepoFKCount int
	if err := d.Pool().QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'shared_job_templates'::regclass AND confrelid = 'shared_repos'::regclass`).Scan(&sharedRepoFKCount); err != nil {
		t.Fatalf("check shared_job_templates.shared_repo_id foreign key: %v", err)
	}
	if sharedRepoFKCount != 1 {
		t.Errorf("shared_job_templates -> shared_repos foreign key count = %d, want 1 — a shared template always names which shared repository its playbook lives in", sharedRepoFKCount)
	}

	var sharedRepoNullable string
	if err := d.Pool().QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_name = 'shared_job_templates' AND column_name = 'shared_repo_id'`).Scan(&sharedRepoNullable); err != nil {
		t.Fatalf("check shared_job_templates.shared_repo_id exists: %v", err)
	}
	if sharedRepoNullable != "NO" {
		t.Errorf("shared_job_templates.shared_repo_id is_nullable = %q, want NO — a path alone names nothing without the repository it is relative to", sharedRepoNullable)
	}

	var templateIDNullable string
	if err := d.Pool().QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_name = 'job_templates' AND column_name = 'shared_template_id'`).Scan(&templateIDNullable); err != nil {
		t.Fatalf("check job_templates.shared_template_id exists: %v", err)
	}
	if templateIDNullable != "YES" {
		t.Errorf("job_templates.shared_template_id is_nullable = %q, want YES — a non-projecting row names no definition", templateIDNullable)
	}

	var templateIDDeleteRule string
	if err := d.Pool().QueryRow(ctx, `
		SELECT confdeltype FROM pg_constraint
		WHERE conrelid = 'job_templates'::regclass AND confrelid = 'shared_job_templates'::regclass`).Scan(&templateIDDeleteRule); err != nil {
		t.Fatalf("check job_templates.shared_template_id foreign key: %v", err)
	}
	if templateIDDeleteRule != "r" {
		t.Errorf("job_templates.shared_template_id foreign key confdeltype = %q, want %q (restrict) — "+
			"deleting a definition still projected into an organization must be refused, not cascade", templateIDDeleteRule, "r")
	}

	var playbookIDNullable string
	if err := d.Pool().QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_name = 'job_templates' AND column_name = 'playbook_id'`).Scan(&playbookIDNullable); err != nil {
		t.Fatalf("check job_templates.playbook_id exists: %v", err)
	}
	if playbookIDNullable != "YES" {
		t.Errorf("job_templates.playbook_id is_nullable = %q, want YES — migration 00061 loosens this for a projection "+
			"written before its organization has synced a matching playbook; an ordinary template still always resolves one "+
			"through internal/templates/service.go's own playbooks.Get before writing", playbookIDNullable)
	}
}

// TestSharedCheckDefinitionsSchema is TestSharedJobTemplatesSchema's own test
// for migration 00062, the third and last application of the mechanism:
// shared_check_definitions must carry neither org_id nor row-level security
// (installation-level data, the same posture shared_repos and
// shared_job_templates already have); check_definitions.shared_definition_id
// must be nullable with ON DELETE RESTRICT, the same shape the other two
// kinds' own projection column carries; and origin's own CHECK constraint
// must now admit 'shared' alongside 'seed' and 'custom' — the one column
// this migration widens rather than adds, verified here by reading the
// constraint back from the live catalog rather than trusting that the
// DROP/ADD in the migration file did what it says.
func TestSharedCheckDefinitionsSchema(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()

	var hasOrgID bool
	if err := d.Pool().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			WHERE c.relname = 'shared_check_definitions' AND a.attname = 'org_id' AND NOT a.attisdropped
		)`).Scan(&hasOrgID); err != nil {
		t.Fatalf("check org_id column: %v", err)
	}
	if hasOrgID {
		t.Error("shared_check_definitions must not have an org_id column; it is installation-level, not tenant data")
	}

	var rls bool
	if err := d.Pool().QueryRow(ctx, `
		SELECT c.relrowsecurity FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		WHERE c.relname = 'shared_check_definitions'`).Scan(&rls); err != nil {
		t.Fatalf("check relrowsecurity: %v", err)
	}
	if rls {
		t.Error("shared_check_definitions must not carry row-level security; it is visible to every organization by design")
	}

	var sharedDefIDNullable string
	if err := d.Pool().QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_name = 'check_definitions' AND column_name = 'shared_definition_id'`).Scan(&sharedDefIDNullable); err != nil {
		t.Fatalf("check check_definitions.shared_definition_id exists: %v", err)
	}
	if sharedDefIDNullable != "YES" {
		t.Errorf("check_definitions.shared_definition_id is_nullable = %q, want YES — a non-projecting row names no definition", sharedDefIDNullable)
	}

	var deleteRule string
	if err := d.Pool().QueryRow(ctx, `
		SELECT confdeltype FROM pg_constraint
		WHERE conrelid = 'check_definitions'::regclass AND confrelid = 'shared_check_definitions'::regclass`).Scan(&deleteRule); err != nil {
		t.Fatalf("check check_definitions.shared_definition_id foreign key: %v", err)
	}
	if deleteRule != "r" {
		t.Errorf("check_definitions.shared_definition_id foreign key confdeltype = %q, want %q (restrict) — "+
			"deleting a definition still projected into an organization must be refused, not cascade", deleteRule, "r")
	}

	var originCheckDef string
	if err := d.Pool().QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'check_definitions'::regclass AND conname = 'check_definitions_origin_check'`).Scan(&originCheckDef); err != nil {
		t.Fatalf("check check_definitions_origin_check exists: %v", err)
	}
	if !strings.Contains(originCheckDef, "'shared'") {
		t.Errorf("check_definitions_origin_check = %q, want it to admit 'shared' alongside 'seed' and 'custom'", originCheckDef)
	}

	// Pins the brief's own first structural claim: nothing in this schema
	// references check_definitions by foreign key, so DeleteWithProjections
	// for this kind (internal/shared) has no dependant table to check before
	// removing a projection — check_samples and check_current are keyed by
	// (org_id, host_id, check_key), not by check_definitions.id, exactly so
	// editing or deleting a definition never orphans or rewrites history
	// (migration 00043's own Definition doc comment). If this count is ever
	// non-zero, internal/shared's delete path needs an explicit check for
	// that new table, the same way 00060/00061 added theirs for their own
	// referencing tables.
	var referencingFKCount int
	if err := d.Pool().QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint WHERE confrelid = 'check_definitions'::regclass AND contype = 'f'`).Scan(&referencingFKCount); err != nil {
		t.Fatalf("count foreign keys referencing check_definitions: %v", err)
	}
	if referencingFKCount != 0 {
		t.Errorf("foreign keys referencing check_definitions = %d, want 0 — a new referencing table means "+
			"shared.SQLStore's check-definition delete path needs its own explicit dependant check, the same way "+
			"git_repos and job_templates already have theirs", referencingFKCount)
	}
}

// seedMonitoringGroupForCascadeTest and seedHostForCascadeTest are this
// file's own minimal fixtures — check_definitions.group_id/host_id's own
// composite FKs need a real monitoring_groups/hosts row to reference, and
// nothing above already builds one of either.
func seedMonitoringGroupForCascadeTest(t *testing.T, d *db.DB, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(context.Background(),
		`INSERT INTO monitoring_groups (id, org_id, name, selector, priority) VALUES ($1, $2, 'g', 'true', 0)`,
		id, orgID); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedHostForCascadeTest(t *testing.T, d *db.DB, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	zoneID := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx,
		`INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'z', 'z')`, zoneID, orgID); err != nil {
		t.Fatal(err)
	}
	hostID := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx,
		`INSERT INTO hosts (id, org_id, zone_id, name, facts) VALUES ($1, $2, $3, 'h', '{}'::jsonb)`,
		hostID, orgID, zoneID); err != nil {
		t.Fatal(err)
	}
	return hostID
}

func seedCheckDefinitionForCascadeTest(
	t *testing.T, d *db.DB, orgID uuid.UUID, key, bindingKind string, groupID, hostID *uuid.UUID, origin string,
) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(context.Background(),
		`INSERT INTO check_definitions (id, org_id, key, plugin, interval_seconds, binding_kind, group_id, host_id, origin)
		 VALUES ($1, $2, $3, 'cpu', 60, $4, $5, $6, $7)`,
		id, orgID, key, bindingKind, groupID, hostID, origin); err != nil {
		t.Fatal(err)
	}
	return id
}

func checkDefinitionBindingNow(t *testing.T, d *db.DB, id uuid.UUID) (bindingKind string, groupID, hostID *uuid.UUID, exists bool) {
	t.Helper()
	var gid, hid *uuid.UUID
	err := d.Pool().QueryRow(context.Background(),
		`SELECT binding_kind, group_id, host_id FROM check_definitions WHERE id = $1`, id).Scan(&bindingKind, &gid, &hid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return bindingKind, gid, hid, true
}

// TestDeletingABoundGroupPreservesASharedProjectionAtOrgScope is migration
// 00063's own test, for the Critical a review found after this task's own
// implementation: check_definitions.group_id is ON DELETE CASCADE off
// monitoring_groups (migration 00043), and nothing about that cascade knows
// or cares whether the row it is about to remove is a customer's own
// definition or an installation-owned projection — so a customer rebinding
// a shared projection to a group (this task's own feature) and then
// deleting that group would otherwise silently and permanently destroy the
// provider's check for that one organization, with checks.Service.
// DeleteDefinition's own origin == "shared" refusal never in the call path
// at all (the delete happens against monitoring_groups, not
// check_definitions). Migration 00063's trigger is what this test proves:
// the shared row survives, rebound to the default ('org', nil, nil) scope
// it was born at — not merely that the group's own delete succeeded.
//
// An ordinary CUSTOM check bound to the same group is seeded alongside the
// shared one specifically to prove the trigger's WHEN (origin = 'shared')
// clause does not change that row's own, already-correct behaviour: it
// still cascades away with the group, exactly as migration 00043's own
// comment says it should ("deleting a group or host takes its bindings
// with it").
func TestDeletingABoundGroupPreservesASharedProjectionAtOrgScope(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d)
	group := seedMonitoringGroupForCascadeTest(t, d, org)

	sharedID := seedCheckDefinitionForCascadeTest(t, d, org, "disk-data", "group", &group, nil, "shared")
	customID := seedCheckDefinitionForCascadeTest(t, d, org, "custom-check", "group", &group, nil, "custom")

	if _, err := d.Pool().Exec(context.Background(), `DELETE FROM monitoring_groups WHERE id = $1`, group); err != nil {
		t.Fatalf("delete the bound group: %v", err)
	}

	bindingKind, groupID, _, exists := checkDefinitionBindingNow(t, d, sharedID)
	if !exists {
		t.Fatal("the shared projection was deleted along with its group, want it to survive rebound to org scope")
	}
	if bindingKind != "org" || groupID != nil {
		t.Fatalf("shared projection after its group was deleted = (binding_kind=%q, group_id=%v), want (org, nil)", bindingKind, groupID)
	}

	if _, _, _, exists := checkDefinitionBindingNow(t, d, customID); exists {
		t.Fatal("the ORDINARY custom check bound to the same group survived its deletion, want it to still cascade away unchanged")
	}
}

// TestDeletingABoundHostPreservesASharedProjectionAtOrgScope is
// TestDeletingABoundGroupPreservesASharedProjectionAtOrgScope's own test for
// a host binding — check_definitions.host_id carries the identical ON
// DELETE CASCADE off hosts (migration 00043), and hosts.Service.DeleteHost
// is the second call site the brief's own Go-only audit missed for the same
// reason: the cascade never reaches checks.Service.DeleteDefinition either.
func TestDeletingABoundHostPreservesASharedProjectionAtOrgScope(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d)
	host := seedHostForCascadeTest(t, d, org)

	sharedID := seedCheckDefinitionForCascadeTest(t, d, org, "cpu-watch", "host", nil, &host, "shared")

	if _, err := d.Pool().Exec(context.Background(), `DELETE FROM hosts WHERE id = $1`, host); err != nil {
		t.Fatalf("delete the bound host: %v", err)
	}

	bindingKind, _, hostID, exists := checkDefinitionBindingNow(t, d, sharedID)
	if !exists {
		t.Fatal("the shared projection was deleted along with its host, want it to survive rebound to org scope")
	}
	if bindingKind != "org" || hostID != nil {
		t.Fatalf("shared projection after its host was deleted = (binding_kind=%q, host_id=%v), want (org, nil)", bindingKind, hostID)
	}
}

// TestDeletingTheOrganizationStillRemovesSharedProjections is the trigger's
// own negative case, and the one a naive fix (gating on pg_trigger_depth()
// alone, rather than checking whether the organization itself survives)
// would have gotten wrong: check_definitions.org_id is ALSO ON DELETE
// CASCADE off organizations, nested inside the identical trigger depth a
// group or host cascade reaches, and MUST be allowed to actually remove
// every row — rebinding instead of deleting here would leave orphaned
// check_definitions rows referencing a deleted organization, which
// Postgres does not independently re-verify once an application trigger
// has suppressed part of a cascade (reproduced once, by hand, while
// developing this migration, before the EXISTS-based condition replaced
// the depth-only one — see 00063's own migration comment for the full
// story).
func TestDeletingTheOrganizationStillRemovesSharedProjections(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d)
	group := seedMonitoringGroupForCascadeTest(t, d, org)

	groupBound := seedCheckDefinitionForCascadeTest(t, d, org, "disk-data", "group", &group, nil, "shared")
	orgBound := seedCheckDefinitionForCascadeTest(t, d, org, "org-scope-shared", "org", nil, nil, "shared")

	if _, err := d.Pool().Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, org); err != nil {
		t.Fatalf("delete the organization: %v", err)
	}

	for _, id := range []uuid.UUID{groupBound, orgBound} {
		if _, _, _, exists := checkDefinitionBindingNow(t, d, id); exists {
			t.Fatalf("check definition %s survived its own organization's deletion, want it gone — rebinding only "+
				"applies when the organization itself is still there to rebind within", id)
		}
	}
}
