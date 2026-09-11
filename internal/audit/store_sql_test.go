package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func seedUser(t *testing.T, q *gen.Queries, email string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(context.Background(), gen.CreateUserParams{ID: id, Email: email, DisplayName: email}); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// TestSQLStoreInsertAndList round-trips every field, including a non-empty
// Meta, through the real database and back.
func TestSQLStoreInsertAndList(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	org := seedOrg(t, q, "audit-store-org")
	actor := seedUser(t, q, "audit-store@example.test")

	at := time.Now().UTC().Truncate(time.Microsecond)
	ev := audit.Event{
		ID: uuid.Must(uuid.NewV7()), OrgID: &org, ActorID: &actor, ActorKind: audit.ActorKindUser,
		Action: "PATCH updateOrganization", ObjectType: "organization", ObjectID: org.String(),
		Summary: "renamed org to Acme", Meta: map[string]any{"from": "Old", "to": "Acme"}, At: at,
	}
	if err := store.Insert(ctx, ev); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	items, cursor, err := store.List(ctx, org, audit.Filter{}, "", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if cursor != "" {
		t.Fatalf("cursor = %q, want empty (fewer rows than the limit)", cursor)
	}
	if len(items) != 1 {
		t.Fatalf("List returned %d items, want 1", len(items))
	}
	got := items[0]
	if got.ID != ev.ID || got.OrgID == nil || *got.OrgID != org || got.ActorID == nil || *got.ActorID != actor {
		t.Fatalf("List row identity = %+v", got)
	}
	if got.ActorKind != audit.ActorKindUser || got.Action != ev.Action || got.ObjectType != ev.ObjectType ||
		got.ObjectID != ev.ObjectID || got.Summary != ev.Summary {
		t.Fatalf("List row fields = %+v, want %+v", got, ev)
	}
	if !got.At.Equal(at) {
		t.Fatalf("At = %v, want %v", got.At, at)
	}
	if got.Meta["from"] != "Old" || got.Meta["to"] != "Acme" {
		t.Fatalf("Meta = %+v, want the inserted map round-tripped through jsonb", got.Meta)
	}
}

// TestSQLStoreInsertWithNoOrgOrActor covers a system-generated event: no
// org (an instance-level action), no actor (nothing that reads as a user
// caused it). Neither audit_events.org_id nor .actor_id is NOT NULL for
// exactly this case.
func TestSQLStoreInsertWithNoOrgOrActor(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := audit.NewSQLStore(d)

	ev := audit.Event{
		ID: uuid.Must(uuid.NewV7()), ActorKind: audit.ActorKindSystem,
		Action: "system sweep", ObjectType: "host", ObjectID: "h1", At: time.Now().UTC(),
	}
	if err := store.Insert(ctx, ev); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if _, err := d.Pool().Exec(ctx, `SELECT 1 FROM audit_events WHERE id = $1 AND org_id IS NULL AND actor_id IS NULL AND meta = '{}'::jsonb`, ev.ID); err != nil {
		t.Fatalf("row not stored as expected: %v", err)
	}
}

// TestSQLStoreListFiltersByActorObjectTypeSince exercises Filter's three
// fields against the real query, one at a time, against a fixture built so
// each filter excludes a different subset.
func TestSQLStoreListFiltersByActorObjectTypeSince(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	org := seedOrg(t, q, "audit-filter-org")
	alice := seedUser(t, q, "alice-audit@example.test")
	bob := seedUser(t, q, "bob-audit@example.test")

	base := time.Now().UTC().Truncate(time.Microsecond)
	events := []audit.Event{
		{ID: uuid.Must(uuid.NewV7()), OrgID: &org, ActorID: &alice, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "organization", ObjectID: "o", At: base},
		{ID: uuid.Must(uuid.NewV7()), OrgID: &org, ActorID: &bob, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "member", ObjectID: "m", At: base.Add(time.Second)},
		{ID: uuid.Must(uuid.NewV7()), OrgID: &org, ActorID: &alice, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "token", ObjectID: "t", At: base.Add(2 * time.Second)},
	}
	for _, e := range events {
		if err := store.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	items, _, err := store.List(ctx, org, audit.Filter{ActorID: &alice}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("Filter{ActorID: alice} = %d items, want 2", len(items))
	}

	items, _, err = store.List(ctx, org, audit.Filter{ObjectType: "member"}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ObjectType != "member" {
		t.Fatalf("Filter{ObjectType: member} = %+v, want exactly the member event", items)
	}

	since := base.Add(time.Second)
	items, _, err = store.List(ctx, org, audit.Filter{Since: &since}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("Filter{Since: base+1s} = %d items, want 2 (excludes the first event)", len(items))
	}
}

// TestSQLStoreListScopesToOrg is the app-level counterpart of the RLS test
// below: List's own WHERE org_id = $1 must not return another org's rows,
// independent of whatever row-level security additionally enforces.
func TestSQLStoreListScopesToOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	orgA := seedOrg(t, q, "audit-scope-a")
	orgB := seedOrg(t, q, "audit-scope-b")

	idA := uuid.Must(uuid.NewV7())
	if err := store.Insert(ctx, audit.Event{ID: idA, OrgID: &orgA, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "x", ObjectID: "1", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	idB := uuid.Must(uuid.NewV7())
	if err := store.Insert(ctx, audit.Event{ID: idB, OrgID: &orgB, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "x", ObjectID: "2", At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	items, _, err := store.List(ctx, orgB, audit.Filter{}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != idB {
		t.Fatalf("List(orgB) = %+v, want exactly orgB's own event [%s]", items, idB)
	}
}

// TestSQLStoreListForActorSpansOrgs proves ListForActor is not scoped to
// one org — the point of GET /me/activity — while still excluding another
// actor's events in the same org.
func TestSQLStoreListForActorSpansOrgs(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	orgA := seedOrg(t, q, "audit-actor-org-a")
	orgB := seedOrg(t, q, "audit-actor-org-b")
	actor := seedUser(t, q, "actor-spans@example.test")
	other := seedUser(t, q, "other-spans@example.test")

	for _, e := range []audit.Event{
		{ID: uuid.Must(uuid.NewV7()), OrgID: &orgA, ActorID: &actor, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "x", ObjectID: "1", At: time.Now()},
		{ID: uuid.Must(uuid.NewV7()), OrgID: &orgB, ActorID: &actor, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "x", ObjectID: "2", At: time.Now().Add(time.Second)},
		{ID: uuid.Must(uuid.NewV7()), OrgID: &orgA, ActorID: &other, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "x", ObjectID: "3", At: time.Now().Add(2 * time.Second)},
	} {
		if err := store.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	items, _, err := store.ListForActor(ctx, actor, audit.Filter{}, "", 10)
	if err != nil {
		t.Fatalf("ListForActor: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("ListForActor = %d items, want 2 (one per org, none of the other actor's)", len(items))
	}
	seenOrgs := map[uuid.UUID]bool{}
	for _, it := range items {
		seenOrgs[*it.OrgID] = true
	}
	if !seenOrgs[orgA] || !seenOrgs[orgB] {
		t.Fatalf("ListForActor did not span both orgs: %+v", items)
	}
}

// TestSQLStoreListPaginatesByCursor mirrors TestListPaginatesByCursor
// (audit_test.go's in-memory fake) against the real database, proving
// store_sql.go's own cursor plumbing (DecodeCursor -> query params ->
// EncodeCursor of the last row) behaves the same way the fake's hand-rolled
// pagination does.
func TestSQLStoreListPaginatesByCursor(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	org := seedOrg(t, q, "audit-page-org")

	base := time.Now().UTC().Truncate(time.Microsecond)
	var ids []uuid.UUID
	for i := 0; i < 3; i++ {
		id := uuid.Must(uuid.NewV7())
		ids = append(ids, id)
		ev := audit.Event{ID: id, OrgID: &org, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "x", ObjectID: "y", At: base.Add(time.Duration(i) * time.Second)}
		if err := store.Insert(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	want := []uuid.UUID{ids[2], ids[1], ids[0]} // most recent first

	page1, cursor1, err := store.List(ctx, org, audit.Filter{}, "", 2)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 2 || page1[0].ID != want[0] || page1[1].ID != want[1] {
		t.Fatalf("page 1 = %+v, want IDs %v", page1, want[:2])
	}
	if cursor1 == "" {
		t.Fatal("page 1's cursor is empty, want a cursor pointing past the second event")
	}

	page2, cursor2, err := store.List(ctx, org, audit.Filter{}, cursor1, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != want[2] {
		t.Fatalf("page 2 = %+v, want exactly [%v]", page2, want[2])
	}
	if cursor2 != "" {
		t.Fatalf("page 2's cursor = %q, want empty", cursor2)
	}
}

// TestSQLStoreListPaginationStableWithSharedTimestamp is
// TestListPaginationStableWithSharedTimestamp against the real database:
// several events inserted with the exact same At must still page without
// repeats or gaps, proving the id tiebreaker in audit.sql's
// ORDER BY at DESC, id DESC actually works against Postgres, not only
// against the in-memory fake's Go-side sort.
func TestSQLStoreListPaginationStableWithSharedTimestamp(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	org := seedOrg(t, q, "audit-tie-org")
	at := time.Now().UTC().Truncate(time.Microsecond)

	ids := map[uuid.UUID]bool{}
	for i := 0; i < 3; i++ {
		id := uuid.Must(uuid.NewV7())
		ids[id] = true
		if err := store.Insert(ctx, audit.Event{ID: id, OrgID: &org, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "x", ObjectID: "y", At: at}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[uuid.UUID]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		items, next, err := store.List(ctx, org, audit.Filter{}, cursor, 1)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(items) == 0 {
			break
		}
		if seen[items[0].ID] {
			t.Fatalf("event %s returned twice across pages", items[0].ID)
		}
		seen[items[0].ID] = true
		cursor = next
		if cursor == "" {
			break
		}
	}
	if len(seen) != len(ids) {
		t.Fatalf("saw %d distinct events across all pages, want %d", len(seen), len(ids))
	}
}

// TestAuditEventsAreIsolatedPerOrgByRLS mirrors
// TestAPITokensAreIsolatedPerOrgByRLS (internal/auth/token_store_sql_test.go)
// and TestMembershipsAreIsolatedPerOrg: it exercises the org_isolation RLS
// policy directly, via d.WithOrg (which sets the pulsix.org_id setting
// org_isolation's USING clause reads), rather than through SQLStore/
// gen.Queries the way every other test in this file does. testdb.Open
// connects as pulsix_app without ever setting pulsix.org_id, so those tests
// only ever exercise the org_isolation_bypass policy (current_setting
// ('pulsix.org_id', true) IS NULL) — replacing org_isolation's USING
// clause with USING (true) would not fail a single one of them. This test
// would.
func TestAuditEventsAreIsolatedPerOrgByRLS(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	orgA := seedOrg(t, q, "audit-rls-org-a")
	orgB := seedOrg(t, q, "audit-rls-org-b")

	if err := store.Insert(ctx, audit.Event{ID: uuid.Must(uuid.NewV7()), OrgID: &orgA, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "x", ObjectID: "1", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, audit.Event{ID: uuid.Must(uuid.NewV7()), OrgID: &orgB, ActorKind: audit.ActorKindUser, Action: "a", ObjectType: "x", ObjectID: "2", At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	var seen []uuid.UUID
	err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id FROM audit_events`)
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
