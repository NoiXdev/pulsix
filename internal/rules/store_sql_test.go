package rules_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// seedOrg creates a bare organization, the same minimal fixture
// internal/checks' own store tests use.
func seedOrg(t *testing.T, d *db.DB, slug string) uuid.UUID {
	t.Helper()
	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// seedHost creates a bare zone and host in org — check_rules' host binding
// needs a real host to satisfy its composite FK, exactly like
// checks.definitions_store_test.go's own seedHost.
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

// seedGroup creates a bare monitoring group in org — check_rules' group
// binding needs one for the same reason seedHost exists.
func seedGroup(t *testing.T, d *db.DB, org uuid.UUID, name string) uuid.UUID {
	t.Helper()
	q := gen.New(d.Pool())
	group, err := q.CreateMonitoringGroup(context.Background(), gen.CreateMonitoringGroupParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: name, Selector: "role=" + name,
	})
	if err != nil {
		t.Fatal(err)
	}
	return group.ID
}

// hostRule returns a Rule bound to hostID, ready for SQLStore.Create — the
// baseline every store test mutates.
func hostRule(org, hostID uuid.UUID) rules.Rule {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return rules.Rule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "high cpu", Enabled: true, Origin: "user",
		Scope: rules.ScopeHost, HostID: &hostID, CheckKey: "cpu", Metric: "cpu.used_pct",
		Severity: rules.SeverityWarn, Mode: "form", Source: "cpu.used_pct > 90",
		Trigger: rules.Node{Kind: rules.KindCompare, Op: ">", Args: []rules.Node{
			{Kind: rules.KindMetric, Name: "cpu.used_pct"},
			{Kind: rules.KindNumber, Num: 90},
		}},
		For: 5 * time.Minute, CreatedAt: now, UpdatedAt: now,
	}
}

// TestRuleStoreRoundTrip exercises SQLStore directly (no Service): create,
// get, list, update (every mutable column changes; created_at survives, the
// clock-stamped updated_at from the caller is what is stored, not a fresh
// DB-side now()), delete, and the ErrNotFound a missing row produces.
func TestRuleStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-roundtrip")
	host := seedHost(t, d, org, "h1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	r := hostRule(org, host)
	if err := st.Create(scoped, r); err != nil {
		t.Fatal(err)
	}

	got, err := st.Get(scoped, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != r.Name || got.CheckKey != r.CheckKey || got.Metric != r.Metric ||
		got.Severity != r.Severity || got.Mode != r.Mode || got.Source != r.Source {
		t.Fatalf("got = %+v, want fields matching %+v", got, r)
	}
	if got.For != r.For {
		t.Fatalf("For = %v, want %v", got.For, r.For)
	}
	if !got.CreatedAt.Equal(r.CreatedAt) || !got.UpdatedAt.Equal(r.UpdatedAt) {
		t.Fatalf("timestamps = %v/%v, want %v/%v (the caller's own clock, not the database's)", got.CreatedAt, got.UpdatedAt, r.CreatedAt, r.UpdatedAt)
	}
	if got.HostID == nil || *got.HostID != host {
		t.Fatalf("HostID = %v, want %v", got.HostID, host)
	}
	if got.Trigger.Kind != rules.KindCompare || len(got.Trigger.Args) != 2 {
		t.Fatalf("Trigger = %+v", got.Trigger)
	}
	if got.Clear.Kind != "" {
		t.Fatalf("Clear = %+v, want the zero Node (no clear_ast stored)", got.Clear)
	}

	list, err := st.List(scoped, org, rules.Filter{})
	if err != nil || len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}

	updated := got
	updated.Name = "high cpu (renamed)"
	updated.Enabled = false
	updated.Severity = rules.SeverityCrit
	updated.For = time.Hour
	updated.Source = "cpu.used_pct > 95"
	updated.Trigger = rules.Node{Kind: rules.KindCompare, Op: ">", Args: []rules.Node{
		{Kind: rules.KindMetric, Name: "cpu.used_pct"},
		{Kind: rules.KindNumber, Num: 95},
	}}
	updated.UpdatedAt = time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	if err := st.Update(scoped, updated); err != nil {
		t.Fatal(err)
	}
	reGot, err := st.Get(scoped, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reGot.Name != updated.Name || reGot.Enabled || reGot.Severity != rules.SeverityCrit || reGot.For != time.Hour {
		t.Fatalf("updated = %+v", reGot)
	}
	if !reGot.CreatedAt.Equal(r.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want unchanged %v", reGot.CreatedAt, r.CreatedAt)
	}
	if !reGot.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("UpdatedAt = %v, want the caller's own stamp %v", reGot.UpdatedAt, updated.UpdatedAt)
	}

	// Updating an id that does not exist in this org is ErrNotFound.
	ghost := hostRule(org, host)
	if err := st.Update(scoped, ghost); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("update unknown id = %v, want ErrNotFound", err)
	}

	if err := st.Delete(scoped, org, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(scoped, org, r.ID); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("deleting twice = %v, want ErrNotFound", err)
	}
	if _, err := st.Get(scoped, org, r.ID); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}

// TestRuleStoreJSONBRoundTrip is the review focus the brief calls out by
// name: a tree containing every node Kind, jsonb-encoded and decoded back
// byte-for-byte equal, including a non-empty clear_ast. This bypasses
// Service (which would refuse most of this tree as ill-typed for a single
// metric) because the store's job is encoding, not validation — TypeCheck
// already has its own exhaustive coverage in catalog_test.go.
func TestRuleStoreJSONBRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-jsonb-roundtrip")
	host := seedHost(t, d, org, "h1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	tree := rules.Node{
		Kind: rules.KindAnd,
		Args: []rules.Node{
			{
				Kind: rules.KindOr,
				Args: []rules.Node{
					{Kind: rules.KindCompare, Op: ">", Args: []rules.Node{
						{Kind: rules.KindMetric, Name: "cpu.used_pct"},
						{Kind: rules.KindNumber, Num: 90.5},
					}},
					{Kind: rules.KindCompare, Op: "<=", Args: []rules.Node{
						{Kind: rules.KindWindow, Name: "cpu.used_pct", Agg: "avg", Seconds: 600},
						{Kind: rules.KindArith, Op: "*", Args: []rules.Node{
							{Kind: rules.KindFact, Name: "cpu_count"},
							{Kind: rules.KindNumber, Num: 0.8},
						}},
					}},
				},
			},
			{Kind: rules.KindNot, Args: []rules.Node{
				{Kind: rules.KindLabel, Name: "service.state", Op: "in", Values: []string{"active", "activating"}},
			}},
		},
	}
	clear := rules.Node{Kind: rules.KindLabel, Name: "service.state", Op: "!=", Str: "failed"}

	r := hostRule(org, host)
	r.Metric, r.CheckKey = "service.state", "service"
	r.Trigger, r.Clear = tree, clear
	if err := st.Create(scoped, r); err != nil {
		t.Fatal(err)
	}

	got, err := st.Get(scoped, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !nodeEqual(got.Trigger, tree) {
		t.Fatalf("trigger round trip = %+v, want %+v", got.Trigger, tree)
	}
	if !nodeEqual(got.Clear, clear) {
		t.Fatalf("clear round trip = %+v, want %+v", got.Clear, clear)
	}
}

// nodeEqual compares two Nodes ignoring Pos (jsonb never carries it — see
// Node.Pos's own "parser only" doc comment), recursing into Args.
func nodeEqual(a, b rules.Node) bool {
	if a.Kind != b.Kind || a.Name != b.Name || a.Agg != b.Agg || a.Seconds != b.Seconds ||
		a.Op != b.Op || a.Num != b.Num || a.Str != b.Str || len(a.Values) != len(b.Values) || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Values {
		if a.Values[i] != b.Values[i] {
			return false
		}
	}
	for i := range a.Args {
		if !nodeEqual(a.Args[i], b.Args[i]) {
			return false
		}
	}
	return true
}

// TestRuleStoreDecodeErrors pins toRule's error path for a row whose ast or
// clear_ast jsonb, while syntactically valid JSON, does not unmarshal into
// a Node (e.g. a bare JSON string instead of an object) — reachable only by
// writing the row outside SQLStore, which validates nothing about jsonb
// shape beyond "this is what json.Marshal(Node) produced".
func TestRuleStoreDecodeErrors(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-decode-errors")
	host := seedHost(t, d, org, "h1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	badAst := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(context.Background(), `
		INSERT INTO check_rules (id, org_id, name, scope, host_id, check_key, metric, severity, mode, ast, source)
		VALUES ($1, $2, 'bad ast', 'host', $3, 'cpu', 'cpu.used_pct', 'warn', 'expression', '"not an object"'::jsonb, 'x')`,
		badAst, org, host); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(scoped, org, badAst); err == nil || errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("get with an unparseable ast = %v, want a plain decode error", err)
	}

	// A valid ast but an unparseable clear_ast hits the same path one level
	// further down toRule.
	badClear := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(context.Background(), `
		INSERT INTO check_rules (id, org_id, name, scope, host_id, check_key, metric, severity, mode, ast, clear_ast, source)
		VALUES ($1, $2, 'bad clear', 'host', $3, 'cpu', 'cpu.used_pct', 'warn', 'expression',
		        '{"kind":"compare","op":">","args":[{"kind":"metric","name":"cpu.used_pct"},{"kind":"number","num":90}]}'::jsonb,
		        '"not an object"'::jsonb, 'x')`,
		badClear, org, host); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(scoped, org, badClear); err == nil || errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("get with an unparseable clear_ast = %v, want a plain decode error", err)
	}
}

// TestRuleStoreScopeCheckConstraint exercises the CHECK constraint directly
// against SQLStore (bypassing the service, which would refuse the same
// input itself, exactly like checks.TestDefinitionStoreBindingCheck):
// exactly the columns matching scope may be set, tested against every wrong
// combination the CHECK is built to reject.
func TestRuleStoreScopeCheckConstraint(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-scope-check")
	host := seedHost(t, d, org, "h1")
	group := seedGroup(t, d, org, "g1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	base := hostRule(org, host)

	// Valid: each scope with exactly its own columns set.
	orgRule := base
	orgRule.ID, orgRule.Scope, orgRule.HostID = uuid.Must(uuid.NewV7()), rules.ScopeOrg, nil
	if err := st.Create(scoped, orgRule); err != nil {
		t.Fatalf("org scope: %v", err)
	}
	groupRule := base
	groupRule.ID, groupRule.Scope, groupRule.HostID, groupRule.GroupID = uuid.Must(uuid.NewV7()), rules.ScopeGroup, nil, &group
	if err := st.Create(scoped, groupRule); err != nil {
		t.Fatalf("group scope: %v", err)
	}
	hostRuleValid := base
	hostRuleValid.ID = uuid.Must(uuid.NewV7())
	if err := st.Create(scoped, hostRuleValid); err != nil {
		t.Fatalf("host scope: %v", err)
	}
	itemRule := base
	itemRule.ID, itemRule.Scope, itemRule.Item = uuid.Must(uuid.NewV7()), rules.ScopeItem, "sda1"
	if err := st.Create(scoped, itemRule); err != nil {
		t.Fatalf("item scope: %v", err)
	}

	// Invalid: scope 'org' but host_id set anyway.
	bad := base
	bad.ID, bad.Scope = uuid.Must(uuid.NewV7()), rules.ScopeOrg
	if err := st.Create(scoped, bad); !isCheckViolation(err) {
		t.Fatalf("org scope with a host set = %v, want a check_violation", err)
	}
	// Invalid: scope 'group' but no group_id set.
	bad2 := base
	bad2.ID, bad2.Scope = uuid.Must(uuid.NewV7()), rules.ScopeGroup
	if err := st.Create(scoped, bad2); !isCheckViolation(err) {
		t.Fatalf("group scope without a group = %v, want a check_violation", err)
	}
	// Invalid: scope 'host' but group_id set instead of host_id.
	bad3 := base
	bad3.ID, bad3.Scope, bad3.HostID, bad3.GroupID = uuid.Must(uuid.NewV7()), rules.ScopeHost, nil, &group
	if err := st.Create(scoped, bad3); !isCheckViolation(err) {
		t.Fatalf("host scope with a group set instead of a host = %v, want a check_violation", err)
	}
	// Invalid: scope 'item' but item left empty.
	bad4 := base
	bad4.ID, bad4.Scope = uuid.Must(uuid.NewV7()), rules.ScopeItem
	if err := st.Create(scoped, bad4); !isCheckViolation(err) {
		t.Fatalf("item scope with an empty item = %v, want a check_violation", err)
	}
}

// isCheckViolation reports SQLSTATE 23514 (check_violation) — db package
// only exposes helpers for unique (23505) and foreign key (23503)
// violations, neither of which is what these deliberately-mismatched rows
// hit.
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// TestRuleStoreForDurationCheckConstraint pins the [0, 24h] bound the
// migration's own CHECK enforces in the database, independent of
// Service.buildRule's identical (but bypassable-by-going-straight-to-the-
// store) validation.
func TestRuleStoreForDurationCheckConstraint(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-for-duration-check")
	host := seedHost(t, d, org, "h1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	tooLong := hostRule(org, host)
	tooLong.For = 25 * time.Hour
	if err := st.Create(scoped, tooLong); !isCheckViolation(err) {
		t.Fatalf("for_duration > 24h = %v, want a check_violation", err)
	}

	negative := hostRule(org, host)
	negative.For = -time.Minute
	if err := st.Create(scoped, negative); !isCheckViolation(err) {
		t.Fatalf("negative for_duration = %v, want a check_violation", err)
	}

	exactly24h := hostRule(org, host)
	exactly24h.For = 24 * time.Hour
	if err := st.Create(scoped, exactly24h); err != nil {
		t.Fatalf("for_duration == 24h must be accepted (inclusive bound): %v", err)
	}
}

// TestRuleStoreCascadesOnGroupDelete and ...OnHostDelete pin "deleting a
// group or host takes its rules with it" (migration 00045's own comment)
// against the real database, mirroring
// checks.TestDefinitionStoreCascadesOnGroupDelete/...OnHostDelete.
func TestRuleStoreCascadesOnGroupDelete(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-cascade-group")
	group := seedGroup(t, d, org, "g1")
	host := seedHost(t, d, org, "h1") // only to build a valid base Rule to mutate
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	r := hostRule(org, host)
	r.Scope, r.HostID, r.GroupID = rules.ScopeGroup, nil, &group
	if err := st.Create(scoped, r); err != nil {
		t.Fatal(err)
	}

	q := gen.New(d.Pool())
	if _, err := q.DeleteMonitoringGroup(context.Background(), gen.DeleteMonitoringGroupParams{OrgID: org, ID: group}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(scoped, org, r.ID); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("a rule bound to a deleted group must be gone too, got %v", err)
	}
}

func TestRuleStoreCascadesOnHostDelete(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-cascade-host")
	host := seedHost(t, d, org, "h1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	r := hostRule(org, host)
	if err := st.Create(scoped, r); err != nil {
		t.Fatal(err)
	}

	q := gen.New(d.Pool())
	if _, err := q.DeleteHost(context.Background(), gen.DeleteHostParams{OrgID: org, ID: host}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(scoped, org, r.ID); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("a rule bound to a deleted host must be gone too, got %v", err)
	}
}

// TestRuleStoreOrgIsolation is the app-level counterpart of the RLS test
// below: it exercises SQLStore's own WHERE org_id filtering, independent of
// whatever row-level security additionally enforces (the same split
// checks.TestGroupStoreOrgIsolation documents for its own table).
func TestRuleStoreOrgIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "rules-org-a")
	orgB := seedOrg(t, d, "rules-org-b")
	hostA := seedHost(t, d, orgA, "h1")
	hostB := seedHost(t, d, orgB, "h1")
	st := rules.NewSQLStore(d)

	a := hostRule(orgA, hostA)
	if err := st.Create(db.WithOrgScope(context.Background(), orgA), a); err != nil {
		t.Fatal(err)
	}
	b := hostRule(orgB, hostB)
	if err := st.Create(db.WithOrgScope(context.Background(), orgB), b); err != nil {
		t.Fatal(err)
	}

	if _, err := st.Get(db.WithOrgScope(context.Background(), orgB), orgB, a.ID); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("cross-org get = %v, want ErrNotFound", err)
	}
	if err := st.Delete(db.WithOrgScope(context.Background(), orgB), orgB, a.ID); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("cross-org delete = %v, want ErrNotFound", err)
	}
	listA, err := st.List(db.WithOrgScope(context.Background(), orgA), orgA, rules.Filter{})
	if err != nil || len(listA) != 1 || listA[0].ID != a.ID {
		t.Fatalf("orgA's rule must survive orgB's attempts: %+v, %v", listA, err)
	}
}

// TestRulesAreIsolatedPerOrgByRLS mirrors
// audit.TestAuditEventsAreIsolatedPerOrgByRLS: it exercises the
// org_isolation RLS policy directly, via a raw query over d.WithOrg's
// transaction rather than through SQLStore/gen.Queries (which always adds
// its own WHERE org_id — see TestRuleStoreOrgIsolation above). testdb.Open
// connects as pulsix_app without ever setting pulsix.org_id, so a plain
// SQLStore-based test only ever exercises org_isolation_bypass
// (current_setting('pulsix.org_id', true) IS NULL) — never org_isolation's
// own USING clause. This test would fail if that clause were replaced with
// USING (true).
func TestRulesAreIsolatedPerOrgByRLS(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "rules-rls-org-a")
	orgB := seedOrg(t, d, "rules-rls-org-b")
	hostA := seedHost(t, d, orgA, "h1")
	hostB := seedHost(t, d, orgB, "h1")
	st := rules.NewSQLStore(d)

	if err := st.Create(context.Background(), hostRule(orgA, hostA)); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(context.Background(), hostRule(orgB, hostB)); err != nil {
		t.Fatal(err)
	}

	var seen []uuid.UUID
	err := d.WithOrg(context.Background(), orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT org_id FROM check_rules`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen = append(seen, id)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != orgA {
		t.Fatalf("RLS leak: saw %v, want only [%s]", seen, orgA)
	}
}

// TestRuleStoreForeignKeyViolation: binding a rule to a group or host id
// that does not exist in the organization is refused as ErrInvalidInput
// (not a bare database error) on both Create and Update — the composite
// FKs at work, mirroring checks.TestDefinitionStoreForeignKeyViolation.
func TestRuleStoreForeignKeyViolation(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-fk-violation")
	host := seedHost(t, d, org, "h1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)
	ghost := uuid.Must(uuid.NewV7())

	bad := hostRule(org, host)
	bad.Scope, bad.HostID, bad.GroupID = rules.ScopeGroup, nil, &ghost
	if err := st.Create(scoped, bad); !errors.Is(err, rules.ErrInvalidInput) {
		t.Fatalf("create with a nonexistent group = %v, want ErrInvalidInput", err)
	}

	created := hostRule(org, host)
	if err := st.Create(scoped, created); err != nil {
		t.Fatal(err)
	}
	created.HostID = &ghost
	if err := st.Update(scoped, created); !errors.Is(err, rules.ErrInvalidInput) {
		t.Fatalf("update onto a nonexistent host = %v, want ErrInvalidInput", err)
	}
}

// TestRuleStoreGenericErrorsSurface mirrors
// checks.TestDefinitionStoreGenericErrorsSurface: a store failure that is
// neither "no rows" nor a constraint violation (a context already canceled
// before the statement runs) must surface as a plain wrapped error, not be
// mistaken for one of this package's sentinels.
func TestRuleStoreGenericErrorsSurface(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-generic-errors")
	host := seedHost(t, d, org, "h1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	r := hostRule(org, host)
	if err := st.Create(scoped, r); err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(scoped)
	cancel()

	isSentinel := func(err error) bool {
		return errors.Is(err, rules.ErrNotFound) || errors.Is(err, rules.ErrInvalidInput)
	}
	if err := st.Create(canceled, hostRule(org, host)); err == nil || isSentinel(err) {
		t.Fatalf("Create on a canceled context = %v, want a plain wrapped error", err)
	}
	if _, err := st.Get(canceled, org, r.ID); err == nil || isSentinel(err) {
		t.Fatalf("Get on a canceled context = %v, want a plain wrapped error", err)
	}
	if _, err := st.List(canceled, org, rules.Filter{}); err == nil {
		t.Fatalf("List on a canceled context must fail")
	}
	if err := st.Update(canceled, r); err == nil || isSentinel(err) {
		t.Fatalf("Update on a canceled context = %v, want a plain wrapped error", err)
	}
	if err := st.Delete(canceled, org, r.ID); err == nil || isSentinel(err) {
		t.Fatalf("Delete on a canceled context = %v, want a plain wrapped error", err)
	}
}
