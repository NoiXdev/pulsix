package orgsecrets_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/orgsecrets"
)

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// TestSQLStore: an upsert round-trips, a second one replaces the sealed
// fields and field names, keeps created_at and moves updated_at; the list
// is ordered by name; a delete reports whether it removed a row; and
// inside another organization's scope the row is invisible and cannot be
// written or deleted.
func TestSQLStore(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := orgsecrets.NewSQLStore(d)
	org := seedOrg(t, q, "orgsecrets-org")
	other := seedOrg(t, q, "orgsecrets-other")
	scoped := db.WithOrgScope(ctx, org)
	otherScoped := db.WithOrgScope(ctx, other)
	t0 := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

	if _, err := store.Sealed(scoped, org, "sw"); !errors.Is(err, orgsecrets.ErrNotFound) {
		t.Fatalf("Sealed before any write = %v, want ErrNotFound", err)
	}
	if _, err := store.Upsert(scoped, org, "sw", []byte("sealed-1"), []string{"community", "extra"}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(scoped, org, "a-first", []byte("sealed-a"), []string{"user"}, t0); err != nil {
		t.Fatal(err)
	}
	row, err := store.Upsert(scoped, org, "sw", []byte("sealed-2"), []string{"community"}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if row.Name != "sw" || !row.CreatedAt.Equal(t0) || !row.UpdatedAt.Equal(t0.Add(time.Hour)) || !slices.Equal(row.FieldNames, []string{"community"}) {
		t.Fatalf("Upsert returned %+v; want the replaced row with created_at kept", row)
	}
	for name, want := range map[string]bool{"sw": true, "absent": false} {
		if ok, err := store.Exists(scoped, org, name); err != nil || ok != want {
			t.Fatalf("Exists(%s) = %v, %v; want %v", name, ok, err, want)
		}
	}
	if ok, err := store.Exists(otherScoped, org, "sw"); err != nil || ok {
		t.Fatalf("Exists of org's row inside the other scope = %v, %v; want false", ok, err)
	}
	got, err := store.Sealed(scoped, org, "sw")
	if err != nil || !bytes.Equal(got, []byte("sealed-2")) {
		t.Fatalf("Sealed = %q, %v; want the replacement", got, err)
	}
	list, err := store.List(scoped, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "a-first" || list[1].Name != "sw" {
		t.Fatalf("List = %+v, want a-first then sw", list)
	}
	if sw := list[1]; !sw.CreatedAt.Equal(t0) || !sw.UpdatedAt.Equal(t0.Add(time.Hour)) || !slices.Equal(sw.FieldNames, []string{"community"}) {
		t.Fatalf("sw after the replace = %+v; want created %v, updated an hour later, fields [community]", sw, t0)
	}

	// The other organization sees nothing of org's rows and cannot touch them.
	if list, err := store.List(otherScoped, org); err != nil || len(list) != 0 {
		t.Fatalf("org's list inside the other scope = %+v, %v; want none", list, err)
	}
	if _, err := store.Sealed(otherScoped, org, "sw"); !errors.Is(err, orgsecrets.ErrNotFound) {
		t.Fatalf("org's row read inside the other scope: %v, want ErrNotFound", err)
	}
	if _, err := store.Upsert(otherScoped, org, "sw", []byte("hijack"), []string{"x"}, t0); err == nil {
		t.Fatal("an upsert of org's row inside the other scope succeeded, want a row-level security refusal")
	}
	if ok, err := store.Delete(otherScoped, org, "sw"); err != nil || ok {
		t.Fatalf("Delete inside the other scope = %v, %v; want nothing deleted", ok, err)
	}

	if ok, err := store.Delete(scoped, org, "sw"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v; want deleted", ok, err)
	}
	if ok, err := store.Delete(scoped, org, "sw"); err != nil || ok {
		t.Fatalf("second Delete = %v, %v; want nothing deleted", ok, err)
	}
}

// TestServiceOverSQLStore: the service on the real store and the real box,
// and the unscoped read the server's probe runner does.
func TestServiceOverSQLStore(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "orgsecrets-svc")
	svc := orgsecrets.NewService(orgsecrets.Deps{
		Store: orgsecrets.NewSQLStore(d), Box: crypto.NewBox(testKey), Checks: &fakeRefs{},
	})
	in := map[string]string{"community": leak}
	if _, err := svc.Put(db.WithOrgScope(ctx, org), org, "sw", in); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := d.Pool().QueryRow(ctx, `SELECT fields_sealed FROM org_secrets WHERE org_id = $1 AND name = 'sw'`, org).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	assertNoLeak(t, "the stored column", string(raw))
	got, err := svc.Fields(ctx, org, "sw") // no org scope bound: the runner's read
	if err != nil || got["community"] != leak || len(got) != 1 {
		t.Fatalf("Fields: %v (fields %d)", err, len(got))
	}
}
