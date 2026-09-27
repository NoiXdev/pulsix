package checks_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// seedOrg creates a bare organization (monitoring groups need nothing
// else: no zone, no host).
func seedOrg(t *testing.T, d *db.DB, slug string) uuid.UUID {
	t.Helper()
	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// TestGroupStoreRoundTrip exercises SQLStore directly (no Service): create,
// list, update (name, selector, priority all change; created_at survives),
// delete, and the sentinel errors a missing row and a duplicate name
// produce.
func TestGroupStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-groups")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	created, err := st.Create(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "web", Selector: "role=web", Priority: 3})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "web" || created.Selector != "role=web" || created.Priority != 3 {
		t.Fatalf("created = %+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created/updated_at must be stamped by the database, got %+v", created)
	}

	// A second group with the same name in the same org collides with the
	// unique (org_id, name) constraint.
	_, err = st.Create(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "web", Selector: "role=web2"})
	if !errors.Is(err, checks.ErrNameTaken) {
		t.Fatalf("duplicate name = %v, want ErrNameTaken", err)
	}

	list, err := st.List(scoped, org)
	if err != nil || len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}

	updated, err := st.Update(scoped, checks.Group{ID: created.ID, OrgID: org, Name: "web-servers", Selector: "role=web and env=prod", Priority: 7})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "web-servers" || updated.Selector != "role=web and env=prod" || updated.Priority != 7 {
		t.Fatalf("updated = %+v", updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want unchanged %v", updated.CreatedAt, created.CreatedAt)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("UpdatedAt = %v, want after %v", updated.UpdatedAt, created.UpdatedAt)
	}

	// Updating an id that does not exist in this org is ErrNotFound.
	_, err = st.Update(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "ghost", Selector: "role=x"})
	if !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("update unknown id = %v, want ErrNotFound", err)
	}

	if err := st.Delete(scoped, org, updated.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(scoped, org, updated.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("deleting twice = %v, want ErrNotFound", err)
	}
	list, err = st.List(scoped, org)
	if err != nil || len(list) != 0 {
		t.Fatalf("list after delete = %+v, %v", list, err)
	}
}

// TestGroupStoreOrgIsolation: two organizations may reuse the same group
// name (the unique constraint is (org_id, name), not name alone), and
// neither can list, update or delete the other's group — the store filters
// by org_id in the WHERE clause independently of row-level security, the
// same guarantee hosts.sql's GetHost/GetZone document.
func TestGroupStoreOrgIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "checks-org-a")
	orgB := seedOrg(t, d, "checks-org-b")
	st := checks.NewSQLStore(d)

	a, err := st.Create(db.WithOrgScope(ctx, orgA), checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: orgA, Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatal(err)
	}
	// Same name, different org: no conflict.
	b, err := st.Create(db.WithOrgScope(ctx, orgB), checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: orgB, Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatalf("a name taken in another org must not collide: %v", err)
	}

	listA, err := st.List(db.WithOrgScope(ctx, orgA), orgA)
	if err != nil || len(listA) != 1 || listA[0].ID != a.ID {
		t.Fatalf("orgA's list must show only its own group: %+v, %v", listA, err)
	}
	listB, err := st.List(db.WithOrgScope(ctx, orgB), orgB)
	if err != nil || len(listB) != 1 || listB[0].ID != b.ID {
		t.Fatalf("orgB's list must show only its own group: %+v, %v", listB, err)
	}

	// orgB may not update or delete orgA's group by id, even when it
	// (incorrectly) supplies its own org id — the row belongs to orgA.
	if _, err := st.Update(db.WithOrgScope(ctx, orgB), checks.Group{ID: a.ID, OrgID: orgB, Name: "hijacked", Selector: "role=x"}); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("cross-org update = %v, want ErrNotFound", err)
	}
	if err := st.Delete(db.WithOrgScope(ctx, orgB), orgB, a.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("cross-org delete = %v, want ErrNotFound", err)
	}
	// orgA's group survived both attempts.
	listA, err = st.List(db.WithOrgScope(ctx, orgA), orgA)
	if err != nil || len(listA) != 1 {
		t.Fatalf("orgA's group must survive orgB's attempts: %+v, %v", listA, err)
	}
}

// TestGroupStoreUpdateNameTaken: renaming a group onto a name already used
// by another group of the same organization collides with the unique
// (org_id, name) constraint exactly as Create does.
func TestGroupStoreUpdateNameTaken(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-update-name-taken")
	st := checks.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)

	a, err := st.Create(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "a", Selector: "role=a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Create(scoped, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "b", Selector: "role=b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update(scoped, checks.Group{ID: b.ID, OrgID: org, Name: a.Name, Selector: "role=b"}); !errors.Is(err, checks.ErrNameTaken) {
		t.Fatalf("renaming onto a taken name = %v, want ErrNameTaken", err)
	}
}

// TestGroupStoreGenericErrorsSurface: a store failure that is neither "no
// rows" nor a unique violation (here, a context already canceled before
// the statement runs) must surface as a wrapped error rather than being
// mistaken for ErrNotFound or ErrNameTaken.
func TestGroupStoreGenericErrorsSurface(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-generic-errors")
	st := checks.NewSQLStore(d)
	g, err := st.Create(db.WithOrgScope(ctx, org), checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(db.WithOrgScope(ctx, org))
	cancel()

	if _, err := st.Create(canceled, checks.Group{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "other", Selector: "role=x"}); err == nil ||
		errors.Is(err, checks.ErrNameTaken) || errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("Create on a canceled context = %v, want a plain wrapped error", err)
	}
	if _, err := st.List(canceled, org); err == nil {
		t.Fatalf("List on a canceled context must fail")
	}
	if _, err := st.Update(canceled, checks.Group{ID: g.ID, OrgID: org, Name: "web2", Selector: "role=web"}); err == nil ||
		errors.Is(err, checks.ErrNameTaken) || errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("Update on a canceled context = %v, want a plain wrapped error", err)
	}
	if err := st.Delete(canceled, org, g.ID); err == nil || errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("Delete on a canceled context = %v, want a plain wrapped error", err)
	}
}
