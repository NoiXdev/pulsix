package checks_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// seedHost creates a bare zone and host in org (check_definitions' host
// binding needs a real host to reference through its composite FK).
func seedHost(t *testing.T, d *db.DB, org uuid.UUID, name string) uuid.UUID {
	t.Helper()
	q := gen.New(d.Pool())
	zone, err := q.CreateZone(context.Background(), gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: name, Name: name})
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

func orgDefinition(org uuid.UUID, key string) checks.Definition {
	return checks.Definition{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Key: key, Plugin: checks.PluginCPU,
		Interval: time.Minute, Enabled: true, Origin: "custom", BindingKind: checks.BindingOrg,
	}
}

// TestDefinitionStoreNullsNotDistinct is the constraint the brief singles
// out for a real-database test: ordinary UNIQUE treats every NULL as
// distinct, so without NULLS NOT DISTINCT two organization-bound
// definitions (group_id and host_id both NULL) with the same key would
// never collide. This proves PostgreSQL 18 (deploy/compose/docker-
// compose.test.yml) actually enforces it, not merely that the DDL parses.
func TestDefinitionStoreNullsNotDistinct(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-defs-nulls-not-distinct")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	if _, err := st.CreateDefinition(scoped, orgDefinition(org, "cpu")); err != nil {
		t.Fatal(err)
	}
	// Same org, same binding_kind ("org"), both group_id and host_id NULL,
	// same key: this must collide — the entire point of NULLS NOT
	// DISTINCT (spec's Data model).
	_, err := st.CreateDefinition(scoped, orgDefinition(org, "cpu"))
	if !errors.Is(err, checks.ErrKeyTaken) {
		t.Fatalf("second org-bound cpu definition = %v, want ErrKeyTaken", err)
	}

	// A different key, or a different organization, never collides.
	if _, err := st.CreateDefinition(scoped, orgDefinition(org, "cpu-2")); err != nil {
		t.Fatalf("a different key must not collide: %v", err)
	}
	other := seedOrg(t, d, "checks-defs-nulls-not-distinct-2")
	if _, err := st.CreateDefinition(db.WithOrgScope(ctx, other), orgDefinition(other, "cpu")); err != nil {
		t.Fatalf("a different organization must not collide: %v", err)
	}
}

// TestDefinitionStoreBindingCheck exercises the CHECK constraint directly
// against SQLStore (bypassing the service, which would refuse the same
// input itself): exactly the column matching binding_kind may be set.
func TestDefinitionStoreBindingCheck(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-defs-binding-check")
	group := uuid.Must(uuid.NewV7())
	// The group must exist for the FK, not just for the CHECK, so the test
	// isolates the CHECK's own failure from an FK failure on the
	// mis-bound rows below.
	q := gen.New(d.Pool())
	if _, err := q.CreateMonitoringGroup(context.Background(), gen.CreateMonitoringGroupParams{
		ID: group, OrgID: org, Name: "g", Selector: "role=web",
	}); err != nil {
		t.Fatal(err)
	}
	host := seedHost(t, d, org, "h")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	// Valid: each binding_kind with exactly its own column set.
	orgDef := orgDefinition(org, "cpu")
	if _, err := st.CreateDefinition(scoped, orgDef); err != nil {
		t.Fatalf("org binding: %v", err)
	}
	groupDef := orgDefinition(org, "cpu")
	groupDef.ID, groupDef.BindingKind, groupDef.GroupID = uuid.Must(uuid.NewV7()), checks.BindingGroup, &group
	if _, err := st.CreateDefinition(scoped, groupDef); err != nil {
		t.Fatalf("group binding: %v", err)
	}
	hostDef := orgDefinition(org, "cpu")
	hostDef.ID, hostDef.BindingKind, hostDef.HostID = uuid.Must(uuid.NewV7()), checks.BindingHost, &host
	if _, err := st.CreateDefinition(scoped, hostDef); err != nil {
		t.Fatalf("host binding: %v", err)
	}

	// Invalid: binding_kind "org" but group_id set anyway.
	bad := orgDefinition(org, "bad-1")
	bad.BindingKind, bad.GroupID = checks.BindingOrg, &group
	if _, err := st.CreateDefinition(scoped, bad); err == nil {
		t.Fatal("org binding with a group set must violate the CHECK constraint")
	}
	// Invalid: binding_kind "group" but no group_id set.
	bad2 := orgDefinition(org, "bad-2")
	bad2.BindingKind = checks.BindingGroup
	if _, err := st.CreateDefinition(scoped, bad2); err == nil {
		t.Fatal("group binding without a group must violate the CHECK constraint")
	}
	// Invalid: binding_kind "host" but group_id set instead of host_id.
	bad3 := orgDefinition(org, "bad-3")
	bad3.BindingKind, bad3.GroupID = checks.BindingHost, &group
	if _, err := st.CreateDefinition(scoped, bad3); err == nil {
		t.Fatal("host binding with a group set instead of a host must violate the CHECK constraint")
	}
}

// TestDefinitionStoreCascadesOnGroupDelete and ...OnHostDelete pin "Composite
// FKs ... ON DELETE CASCADE: deleting a group or host takes its bindings
// with it" (spec's Data model) against the real database.
func TestDefinitionStoreCascadesOnGroupDelete(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-defs-cascade-group")
	groupSt := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	group, err := groupSt.Create(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatal(err)
	}
	def := orgDefinition(org, "cpu")
	def.BindingKind, def.GroupID = checks.BindingGroup, &group.ID
	created, err := groupSt.CreateDefinition(scoped, def)
	if err != nil {
		t.Fatal(err)
	}

	if err := groupSt.Delete(scoped, org, group.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := groupSt.GetDefinition(scoped, org, created.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("definition bound to a deleted group must be gone too, got %v", err)
	}
}

func TestDefinitionStoreCascadesOnHostDelete(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-defs-cascade-host")
	host := seedHost(t, d, org, "h")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	def := orgDefinition(org, "cpu")
	def.BindingKind, def.HostID = checks.BindingHost, &host
	created, err := st.CreateDefinition(scoped, def)
	if err != nil {
		t.Fatal(err)
	}

	q := gen.New(d.Pool())
	if _, err := q.DeleteHost(context.Background(), gen.DeleteHostParams{OrgID: org, ID: host}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDefinition(scoped, org, created.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("definition bound to a deleted host must be gone too, got %v", err)
	}
}

// TestDefinitionStoreRoundTrip exercises CreateDefinition/GetDefinition/
// ListDefinitions/UpdateDefinition/DeleteDefinition directly, including
// params round-tripping through jsonb and the org-scoping guard Update/
// Delete already prove for groups.
func TestDefinitionStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-defs-roundtrip")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	def := checks.Definition{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Key: "service", Plugin: checks.PluginService,
		Params: map[string]any{"units": []string{"sshd", "cron"}}, Interval: time.Minute, Enabled: true,
		Origin: "custom", BindingKind: checks.BindingOrg,
	}
	created, err := st.CreateDefinition(scoped, def)
	if err != nil {
		t.Fatal(err)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created/updated_at must be stamped by the database, got %+v", created)
	}
	units, ok := created.Params["units"].([]any)
	if !ok || len(units) != 2 || units[0] != "sshd" || units[1] != "cron" {
		t.Fatalf("params round trip = %+v", created.Params)
	}

	got, err := st.GetDefinition(scoped, org, created.ID)
	if err != nil || got.Key != "service" {
		t.Fatalf("get = %+v, %v", got, err)
	}

	list, err := st.ListDefinitions(scoped, org)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	created.Interval, created.Enabled = 5*time.Minute, false
	updated, err := st.UpdateDefinition(scoped, created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Interval != 5*time.Minute || updated.Enabled {
		t.Fatalf("updated = %+v", updated)
	}
	if !updated.UpdatedAt.After(created.CreatedAt.Add(-time.Nanosecond)) {
		t.Fatalf("UpdatedAt = %v", updated.UpdatedAt)
	}

	if _, err := st.UpdateDefinition(scoped, checks.Definition{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Key: "ghost", Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg,
	}); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("update unknown id = %v, want ErrNotFound", err)
	}

	if err := st.DeleteDefinition(scoped, org, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDefinition(scoped, org, created.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("deleting twice = %v, want ErrNotFound", err)
	}
}

// TestDefinitionStoreOrgIsolation: two organizations may reuse the same key
// (the unique constraint includes org_id), and neither can get, update or
// delete the other's definition, exactly the guarantee groups already
// carry.
func TestDefinitionStoreOrgIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "checks-defs-org-a")
	orgB := seedOrg(t, d, "checks-defs-org-b")
	st := checks.NewSQLStore(d)

	a, err := st.CreateDefinition(db.WithOrgScope(ctx, orgA), orgDefinition(orgA, "cpu"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDefinition(db.WithOrgScope(ctx, orgB), orgDefinition(orgB, "cpu")); err != nil {
		t.Fatalf("a key taken in another org must not collide: %v", err)
	}

	if _, err := st.GetDefinition(db.WithOrgScope(ctx, orgB), orgB, a.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("cross-org get = %v, want ErrNotFound", err)
	}
	if err := st.DeleteDefinition(db.WithOrgScope(ctx, orgB), orgB, a.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("cross-org delete = %v, want ErrNotFound", err)
	}
	listA, err := st.ListDefinitions(db.WithOrgScope(ctx, orgA), orgA)
	if err != nil || len(listA) != 1 {
		t.Fatalf("orgA's definition must survive orgB's attempts: %+v, %v", listA, err)
	}
}

// TestBackfillSeedsExistingOrganizations proves migration 00043's backfill
// INSERT: an organization that already has no check_definitions rows (as
// every organization created before this migration did) gets the same
// four "Linux base" rows a newly created one gets from SeedForOrg. This
// statement is copied from the migration's own Up section (they must stay
// in sync by hand — a migration cannot call Go code, and there is no way
// to re-run "only the backfill part" of an already-applied migration
// through goose, which is why this test re-executes the SQL directly
// rather than re-running the migration).
func TestBackfillSeedsExistingOrganizations(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-defs-backfill")

	// This organization currently has zero check_definitions rows — the
	// same state a pre-00043 organization was in when the migration ran.
	st := checks.NewSQLStore(d)
	before, err := st.ListDefinitions(db.WithOrgScope(ctx, org), org)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("before = %+v, want none", before)
	}

	const backfillSQL = `
INSERT INTO check_definitions (id, org_id, key, plugin, params, interval_seconds, enabled, origin, binding_kind)
SELECT gen_random_uuid(), o.id, v.key, v.plugin, v.params::jsonb, v.interval_seconds, true, 'seed', 'org'
FROM organizations o
CROSS JOIN (VALUES
  ('cpu',     'cpu',     '{}',                  60),
  ('memory',  'memory',  '{}',                  60),
  ('disk',    'disk',    '{}',                  300),
  ('service', 'service', '{"units":["sshd"]}',  60)
) AS v(key, plugin, params, interval_seconds)
WHERE o.id = $1
`
	if _, err := d.Pool().Exec(context.Background(), backfillSQL, org); err != nil {
		t.Fatal(err)
	}

	after, err := st.ListDefinitions(db.WithOrgScope(ctx, org), org)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 4 {
		t.Fatalf("after = %+v, want 4 rows", after)
	}
	seen := map[string]bool{}
	for _, d := range after {
		if d.Origin != "seed" || d.BindingKind != checks.BindingOrg {
			t.Fatalf("backfilled row %+v, want origin=seed binding_kind=org", d)
		}
		seen[d.Key] = true
	}
	for _, want := range []string{"cpu", "memory", "disk", "service"} {
		if !seen[want] {
			t.Fatalf("backfill missing %q, got %+v", want, after)
		}
	}
}

// TestDefinitionStoreUpdateKeyTaken: renaming a definition's key onto one
// already used at the same scope collides with the unique constraint
// exactly as Create does.
func TestDefinitionStoreUpdateKeyTaken(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-defs-update-key-taken")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	a, err := st.CreateDefinition(scoped, orgDefinition(org, "cpu"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateDefinition(scoped, orgDefinition(org, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	b.Key = a.Key
	if _, err := st.UpdateDefinition(scoped, b); !errors.Is(err, checks.ErrKeyTaken) {
		t.Fatalf("renaming onto a taken key = %v, want ErrKeyTaken", err)
	}
}

// TestDefinitionStoreForeignKeyViolation: binding a definition to a group
// or host id that does not exist in the same organization is refused —
// the composite FKs (org_id, group_id)/(org_id, host_id) at work, surfaced
// as ErrInvalidInput (not a bare database error) on both Create and Update.
func TestDefinitionStoreForeignKeyViolation(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-defs-fk-violation")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	ghost := uuid.Must(uuid.NewV7())

	def := orgDefinition(org, "cpu")
	def.BindingKind, def.GroupID = checks.BindingGroup, &ghost
	if _, err := st.CreateDefinition(scoped, def); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("create with a nonexistent group = %v, want ErrInvalidInput", err)
	}

	created, err := st.CreateDefinition(scoped, orgDefinition(org, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	created.BindingKind, created.HostID = checks.BindingHost, &ghost
	if _, err := st.UpdateDefinition(scoped, created); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("update onto a nonexistent host = %v, want ErrInvalidInput", err)
	}
}

// TestDefinitionStoreGenericErrorsSurface mirrors
// TestGroupStoreGenericErrorsSurface: a store failure that is neither
// "no rows" nor a constraint violation (a context already canceled before
// the statement runs) must surface as a plain wrapped error, not be
// mistaken for one of the sentinels.
func TestDefinitionStoreGenericErrorsSurface(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-defs-generic-errors")
	st := checks.NewSQLStore(d)
	g, err := st.CreateDefinition(db.WithOrgScope(ctx, org), orgDefinition(org, "cpu"))
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(db.WithOrgScope(ctx, org))
	cancel()

	isSentinel := func(err error) bool {
		return errors.Is(err, checks.ErrKeyTaken) || errors.Is(err, checks.ErrNotFound) || errors.Is(err, checks.ErrInvalidInput)
	}
	if _, err := st.CreateDefinition(canceled, orgDefinition(org, "other")); err == nil || isSentinel(err) {
		t.Fatalf("Create on a canceled context = %v, want a plain wrapped error", err)
	}
	if _, err := st.GetDefinition(canceled, org, g.ID); err == nil || isSentinel(err) {
		t.Fatalf("Get on a canceled context = %v, want a plain wrapped error", err)
	}
	if _, err := st.ListDefinitions(canceled, org); err == nil {
		t.Fatalf("List on a canceled context must fail")
	}
	if _, err := st.UpdateDefinition(canceled, g); err == nil || isSentinel(err) {
		t.Fatalf("Update on a canceled context = %v, want a plain wrapped error", err)
	}
	if err := st.DeleteDefinition(canceled, org, g.ID); err == nil || isSentinel(err) {
		t.Fatalf("Delete on a canceled context = %v, want a plain wrapped error", err)
	}
}

// TestNetworkUptimeSeedMigrationMatchesSeedForOrg (backlog 3.7, Review
// Focus 5): migration 00084 writes the network and uptime seed rows for an
// organization that existed before it, SeedForOrg writes them for one
// created afterwards, and the two must agree on every column but the ids
// and timestamps — the migration encodes SeedDefinitions() by hand, so this
// is what notices the two drifting apart.
func TestNetworkUptimeSeedMigrationMatchesSeedForOrg(t *testing.T) {
	d, err := db.Open(ctx, testdb.FreshDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateTo(ctx, 83); err != nil {
		t.Fatal(err)
	}
	existing := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx, `INSERT INTO organizations (id, slug, name) VALUES ($1, 'existing', 'existing')`, existing); err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	created := seedOrg(t, d, "created")
	if err := checks.NewService(checks.NewSQLStore(d)).SeedForOrg(db.WithOrgScope(ctx, created), created); err != nil {
		t.Fatal(err)
	}

	rowsOf := func(org uuid.UUID) map[string]string {
		t.Helper()
		rows, err := d.Pool().Query(ctx, `
SELECT key, (to_jsonb(c) - 'id' - 'org_id' - 'created_at' - 'updated_at')::text
FROM check_definitions c WHERE org_id = $1 AND key IN ('network', 'uptime')`, org)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var key, row string
			if err := rows.Scan(&key, &row); err != nil {
				t.Fatal(err)
			}
			got[key] = row
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	fromMigration, fromSeed := rowsOf(existing), rowsOf(created)
	if len(fromSeed) != 2 {
		t.Fatalf("SeedForOrg wrote %v, want network and uptime", fromSeed)
	}
	for key, want := range fromSeed {
		if fromMigration[key] != want {
			t.Errorf("%s from migration 00084 = %s\nwant SeedForOrg's %s", key, fromMigration[key], want)
		}
	}
}
