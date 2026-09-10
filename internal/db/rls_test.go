package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestMembershipsAreIsolatedPerOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	for _, o := range []struct {
		id   uuid.UUID
		slug string
	}{{orgA, "a"}, {orgB, "b"}} {
		if _, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: o.id, Slug: o.slug, Name: o.slug}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: user, Email: "a@example.test", DisplayName: "A"}); err != nil {
		t.Fatal(err)
	}
	for _, o := range []uuid.UUID{orgA, orgB} {
		if err := q.AddMembership(ctx, gen.AddMembershipParams{OrgID: o, UserID: user, Role: gen.OrgRoleOwner}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []uuid.UUID
	err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id FROM org_memberships`)
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
		t.Fatalf("RLS leak: saw %v, want only %s", seen, orgA)
	}
}
