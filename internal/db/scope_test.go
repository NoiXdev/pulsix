package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// seedTwoOrgs creates organizations A and B, one user who belongs to both,
// and returns their ids. It seeds through the raw pool, outside any org
// scope, where the bypass policy admits the writes.
func seedTwoOrgs(t *testing.T, d *db.DB) (orgA, orgB uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	q := gen.New(d.Pool())
	orgA, orgB = uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	for _, o := range []struct {
		id   uuid.UUID
		slug string
	}{{orgA, "scope-a"}, {orgB, "scope-b"}} {
		if _, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: o.id, Slug: o.slug, Name: o.slug}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: user, Email: "scope@example.test", DisplayName: "Scope"}); err != nil {
		t.Fatal(err)
	}
	for _, o := range []uuid.UUID{orgA, orgB} {
		if err := q.AddMembership(ctx, gen.AddMembershipParams{OrgID: o, UserID: user, Role: gen.OrgRoleOwner}); err != nil {
			t.Fatal(err)
		}
	}
	return orgA, orgB
}

// orgOwnedTables is every table that carries org_id and the
// org_isolation/org_isolation_bypass policy pair.
var orgOwnedTables = []string{"org_memberships", "invites", "api_tokens", "audit_events", "zones", "hosts", "host_tags"}

// seedEveryOrgTable gives orgID one row in every org-owned table besides
// org_memberships (which seedTwoOrgs already fills), through the raw pool.
func seedEveryOrgTable(t *testing.T, d *db.DB, orgID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	user, zone, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Seed')`, []any{user, user.String() + "@example.test"}},
		{`INSERT INTO invites (id, org_id, email, role, token_hash, invited_by, expires_at)
		  VALUES ($1, $2, 'invitee@example.test', 'viewer', $3, $4, now() + interval '1 day')`, []any{uuid.Must(uuid.NewV7()), orgID, []byte(user.String()), user}},
		{`INSERT INTO api_tokens (id, user_id, org_id, name, role, secret_hash) VALUES ($1, $2, $3, 'seed', 'viewer', $4)`,
			[]any{uuid.Must(uuid.NewV7()), user, orgID, []byte(zone.String())}},
		{`INSERT INTO audit_events (id, org_id, actor_kind, action, object_type, object_id, summary, at)
		  VALUES ($1, $2, 'system', 'seed', 'host', 'h', 'seed', now())`, []any{uuid.Must(uuid.NewV7()), orgID}},
		{`INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'seed', 'Seed')`, []any{zone, orgID}},
		{`INSERT INTO hosts (id, org_id, zone_id, name) VALUES ($1, $2, $3, 'seed-host')`, []any{host, orgID, zone}},
		{`INSERT INTO host_tags (host_id, org_id, key, value, source) VALUES ($1, $2, 'role', 'db', 'manual')`, []any{host, orgID}},
	} {
		if _, err := d.Pool().Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// membershipOrgs runs a query with no org predicate at all through d — the
// exact shape of a forgotten WHERE clause — and returns every org id it saw.
func membershipOrgs(ctx context.Context, t *testing.T, d *db.DB) []uuid.UUID {
	t.Helper()
	rows, err := d.Query(ctx, `SELECT org_id FROM org_memberships ORDER BY org_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seen []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return seen
}

func TestOrgScopeRoundTrip(t *testing.T) {
	if _, ok := db.OrgScope(context.Background()); ok {
		t.Fatal("a bare context must carry no org scope")
	}
	orgID := uuid.New()
	got, ok := db.OrgScope(db.WithOrgScope(context.Background(), orgID))
	if !ok || got != orgID {
		t.Fatalf("OrgScope = %s, %v; want %s, true", got, ok, orgID)
	}
}

// TestScopedQueryHidesOtherOrganizations is the property the whole org
// scope exists for: a statement with no org predicate, run in a context
// bound to A, sees only A's rows — and the same statement unscoped sees
// both, so the assertion is not vacuous.
func TestScopedQueryHidesOtherOrganizations(t *testing.T) {
	d := testdb.Open(t)
	orgA, orgB := seedTwoOrgs(t, d)

	scoped := membershipOrgs(db.WithOrgScope(context.Background(), orgA), t, d)
	if len(scoped) != 1 || scoped[0] != orgA {
		t.Fatalf("scoped to A, saw %v; want only %s", scoped, orgA)
	}
	unscoped := membershipOrgs(context.Background(), t, d)
	if len(unscoped) != 2 {
		t.Fatalf("unscoped (instance-level) saw %v; want both %s and %s", unscoped, orgA, orgB)
	}

	seedEveryOrgTable(t, d, orgA)
	seedEveryOrgTable(t, d, orgB)
	for _, table := range orgOwnedTables {
		var seen []uuid.UUID
		rows, err := d.Query(db.WithOrgScope(context.Background(), orgA), `SELECT DISTINCT org_id FROM `+table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			seen = append(seen, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != orgA {
			t.Errorf("%s scoped to A saw organizations %v; want only %s", table, seen, orgA)
		}
	}
}

func TestScopedQueryRowSeesOnlyTheBoundOrganization(t *testing.T) {
	d := testdb.Open(t)
	orgA, _ := seedTwoOrgs(t, d)
	ctx := db.WithOrgScope(context.Background(), orgA)

	var n int
	if err := d.QueryRow(ctx, `SELECT count(*) FROM org_memberships`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("scoped count = %d, want 1", n)
	}
	if err := d.QueryRow(context.Background(), `SELECT count(*) FROM org_memberships`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("unscoped count = %d, want 2", n)
	}
}

func TestScopedQueryRowReportsNoRowsUnchanged(t *testing.T) {
	d := testdb.Open(t)
	_, orgB := seedTwoOrgs(t, d)
	// Scoped to an organization that exists but asking for B's row: RLS
	// hides it, and the caller must still see pgx.ErrNoRows (which every
	// store maps to its own ErrNotFound), not a wrapped or different error.
	ctx := db.WithOrgScope(context.Background(), uuid.Must(uuid.NewV7()))
	var id uuid.UUID
	err := d.QueryRow(ctx, `SELECT org_id FROM org_memberships WHERE org_id = $1`, orgB).Scan(&id)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want pgx.ErrNoRows", err)
	}
}

func TestScopedExecTouchesOnlyTheBoundOrganization(t *testing.T) {
	d := testdb.Open(t)
	orgA, orgB := seedTwoOrgs(t, d)

	tag, err := d.Exec(db.WithOrgScope(context.Background(), orgA), `UPDATE org_memberships SET role = 'viewer'`)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("scoped UPDATE without a predicate affected %d rows, want 1", tag.RowsAffected())
	}
	var role string
	if err := d.Pool().QueryRow(context.Background(), `SELECT role FROM org_memberships WHERE org_id = $1`, orgB).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "owner" {
		t.Fatalf("B's membership was changed to %q by a statement scoped to A", role)
	}
}

func TestScopedInsertIntoAnotherOrganizationIsRejected(t *testing.T) {
	d := testdb.Open(t)
	orgA, orgB := seedTwoOrgs(t, d)
	user := uuid.Must(uuid.NewV7())
	if _, err := gen.New(d.Pool()).CreateUser(context.Background(), gen.CreateUserParams{ID: user, Email: "other@example.test", DisplayName: "Other"}); err != nil {
		t.Fatal(err)
	}

	_, err := d.Exec(db.WithOrgScope(context.Background(), orgA),
		`INSERT INTO org_memberships (org_id, user_id, role) VALUES ($1, $2, 'viewer')`, orgB, user)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("err = %v, want a row-level-security violation (42501)", err)
	}
}

func TestNilScopeSeesNoOrganization(t *testing.T) {
	d := testdb.Open(t)
	seedTwoOrgs(t, d)

	if seen := membershipOrgs(db.WithOrgScope(context.Background(), uuid.Nil), t, d); len(seen) != 0 {
		t.Fatalf("a request bound to no organization saw %v, want nothing", seen)
	}
}

func TestScopedBeginBindsTheTransaction(t *testing.T) {
	d := testdb.Open(t)
	orgA := uuid.New()
	ctx := context.Background()

	tx, err := d.Begin(db.WithOrgScope(ctx, orgA))
	if err != nil {
		t.Fatal(err)
	}
	var setting string
	if err := tx.QueryRow(ctx, `SELECT current_setting('pulsix.org_id', true)`).Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if setting != orgA.String() {
		t.Fatalf("scoped transaction's pulsix.org_id = %q, want %s", setting, orgA)
	}

	tx, err = d.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var unscoped *string
	if err := tx.QueryRow(ctx, `SELECT NULLIF(current_setting('pulsix.org_id', true), '')`).Scan(&unscoped); err != nil {
		t.Fatal(err)
	}
	if unscoped != nil {
		t.Fatalf("unscoped transaction carries pulsix.org_id = %q", *unscoped)
	}
}

// TestUnscopedStatementsWorkOnAConnectionThatWasScoped pins the pool to a
// single connection so an instance-level statement is guaranteed to reuse
// the connection a scoped transaction just ran on. After that transaction
// the setting is the empty string rather than NULL; migration 00012 is what
// keeps org_isolation's uuid cast from failing on it. Every org-owned table
// is checked, since each carries its own copy of the policy.
func TestUnscopedStatementsWorkOnAConnectionThatWasScoped(t *testing.T) {
	url := testdb.URL(t)
	ctx := context.Background()
	d, err := db.OpenAsRole(ctx, url+"&pool_max_conns=1", "pulsix_app")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	orgA, orgB := seedTwoOrgs(t, d)
	seedEveryOrgTable(t, d, orgA)
	seedEveryOrgTable(t, d, orgB)

	if _, err := d.Exec(db.WithOrgScope(ctx, orgA), `SELECT 1 FROM org_memberships`); err != nil {
		t.Fatal(err)
	}
	for _, table := range orgOwnedTables {
		var n int
		if err := d.QueryRow(ctx, `SELECT count(DISTINCT org_id) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("unscoped count on %s after a scoped transaction: %v", table, err)
		}
		if n != 2 {
			t.Fatalf("unscoped read of %s saw %d organizations, want 2", table, n)
		}
	}
	if seen := membershipOrgs(ctx, t, d); len(seen) != 2 {
		t.Fatalf("instance-level query on a previously scoped connection saw %v, want both organizations", seen)
	}
}

// TestScopedRowsReleaseTheirTransaction closes scoped rows without reading
// them. With a one-connection pool, the follow-up statement would block
// forever if the rows had kept their transaction (and so the connection)
// open.
func TestScopedRowsReleaseTheirTransaction(t *testing.T) {
	url := testdb.URL(t)
	ctx := context.Background()
	d, err := db.OpenAsRole(ctx, url+"&pool_max_conns=1", "pulsix_app")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	orgA, _ := seedTwoOrgs(t, d)
	scoped := db.WithOrgScope(ctx, orgA)

	rows, err := d.Query(scoped, `SELECT org_id FROM org_memberships`)
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()
	rows.Close() // idempotent
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen := membershipOrgs(scoped, t, d); len(seen) != 1 {
		t.Fatalf("saw %v after closing earlier rows, want only A", seen)
	}
}

func TestScopedQueryFailuresSurface(t *testing.T) {
	d := testdb.Open(t)
	ctx := db.WithOrgScope(context.Background(), uuid.New())

	// A client-side argument mismatch fails Query itself.
	if _, err := d.Query(ctx, `SELECT org_id FROM org_memberships`, 1); err == nil {
		t.Fatal("Query with a surplus argument succeeded")
	}
	// A server-side error surfaces through Err once the rows are read.
	rows, err := d.Query(ctx, `SELECT 1 / 0`)
	if err == nil {
		for rows.Next() {
			var v int
			_ = rows.Scan(&v)
		}
		err = rows.Err()
	}
	if err == nil {
		t.Fatal("division by zero did not surface as an error")
	}
	// The transaction was rolled back and the connection returned: a new
	// scoped statement works.
	var n int
	if err := d.QueryRow(ctx, `SELECT count(*) FROM org_memberships`).Scan(&n); err != nil {
		t.Fatal(err)
	}
}

func TestScopedStatementsFailWhenTheDatabaseIsGone(t *testing.T) {
	url := testdb.URL(t)
	ctx := context.Background()
	d, err := db.OpenAsRole(ctx, url, "pulsix_app")
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	scoped := db.WithOrgScope(ctx, uuid.New())

	if _, err := d.Exec(scoped, `SELECT 1`); err == nil {
		t.Error("Exec on a closed pool succeeded")
	}
	if _, err := d.Query(scoped, `SELECT 1`); err == nil {
		t.Error("Query on a closed pool succeeded")
	}
	if _, err := d.Begin(scoped); err == nil {
		t.Error("Begin on a closed pool succeeded")
	}
	var n int
	if err := d.QueryRow(scoped, `SELECT 1`).Scan(&n); err == nil {
		t.Error("QueryRow on a closed pool succeeded")
	}
}
