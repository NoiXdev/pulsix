package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestTenancyQueries exercises all eight hand-written queries in
// internal/db/queries/tenancy.sql against a real database and asserts on
// what comes back, not merely that the call succeeds — a wrong column, a
// wrong WHERE clause or a wrong join would fail one of these assertions.
func TestTenancyQueries(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())

	orgID := uuid.Must(uuid.NewV7())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: orgID, Slug: "acme", Name: "Acme Inc"})
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	if org.ID != orgID || org.Slug != "acme" || org.Name != "Acme Inc" {
		t.Fatalf("CreateOrganization = %+v, want id=%s slug=acme name=%q", org, orgID, "Acme Inc")
	}

	userID := uuid.Must(uuid.NewV7())
	user, err := q.CreateUser(ctx, gen.CreateUserParams{ID: userID, Email: "alice@example.test", DisplayName: "Alice"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID != userID || user.Email != "alice@example.test" || user.DisplayName != "Alice" {
		t.Fatalf("CreateUser = %+v, want id=%s email=alice@example.test display_name=Alice", user, userID)
	}

	if err := q.AddMembership(ctx, gen.AddMembershipParams{OrgID: orgID, UserID: userID, Role: gen.OrgRoleAdmin}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	t.Run("GetOrganizationBySlug", func(t *testing.T) {
		got, err := q.GetOrganizationBySlug(ctx, "acme")
		if err != nil {
			t.Fatalf("GetOrganizationBySlug: %v", err)
		}
		if got.ID != orgID || got.Name != "Acme Inc" {
			t.Fatalf("GetOrganizationBySlug = %+v, want id=%s name=%q", got, orgID, "Acme Inc")
		}

		_, err = q.GetOrganizationBySlug(ctx, "no-such-slug")
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("GetOrganizationBySlug(absent) error = %v, want pgx.ErrNoRows", err)
		}
	})

	t.Run("GetUserByEmail", func(t *testing.T) {
		got, err := q.GetUserByEmail(ctx, "alice@example.test")
		if err != nil {
			t.Fatalf("GetUserByEmail: %v", err)
		}
		if got.ID != userID || got.DisplayName != "Alice" {
			t.Fatalf("GetUserByEmail = %+v, want id=%s display_name=Alice", got, userID)
		}

		_, err = q.GetUserByEmail(ctx, "nobody@example.test")
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("GetUserByEmail(absent) error = %v, want pgx.ErrNoRows", err)
		}
	})

	t.Run("GetUserByID", func(t *testing.T) {
		got, err := q.GetUserByID(ctx, userID)
		if err != nil {
			t.Fatalf("GetUserByID: %v", err)
		}
		if got.Email != "alice@example.test" {
			t.Fatalf("GetUserByID = %+v, want email=alice@example.test", got)
		}

		_, err = q.GetUserByID(ctx, uuid.Must(uuid.NewV7()))
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("GetUserByID(absent) error = %v, want pgx.ErrNoRows", err)
		}
	})

	t.Run("GetMembership", func(t *testing.T) {
		got, err := q.GetMembership(ctx, gen.GetMembershipParams{OrgID: orgID, UserID: userID})
		if err != nil {
			t.Fatalf("GetMembership: %v", err)
		}
		if got.Role != gen.OrgRoleAdmin {
			t.Fatalf("GetMembership.Role = %q, want %q", got.Role, gen.OrgRoleAdmin)
		}

		_, err = q.GetMembership(ctx, gen.GetMembershipParams{OrgID: orgID, UserID: uuid.Must(uuid.NewV7())})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("GetMembership(absent) error = %v, want pgx.ErrNoRows", err)
		}
	})

	t.Run("ListMembershipsForUser", func(t *testing.T) {
		// A second org and membership for the same user makes the join and
		// row count meaningful: a wrong JOIN condition or WHERE clause would
		// return zero, one-of-the-wrong-org, or duplicated rows here.
		orgID2 := uuid.Must(uuid.NewV7())
		if _, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: orgID2, Slug: "beta", Name: "Beta LLC"}); err != nil {
			t.Fatalf("CreateOrganization (second org): %v", err)
		}
		if err := q.AddMembership(ctx, gen.AddMembershipParams{OrgID: orgID2, UserID: userID, Role: gen.OrgRoleViewer}); err != nil {
			t.Fatalf("AddMembership (second org): %v", err)
		}

		rows, err := q.ListMembershipsForUser(ctx, userID)
		if err != nil {
			t.Fatalf("ListMembershipsForUser: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("ListMembershipsForUser returned %d rows, want 2: %+v", len(rows), rows)
		}
		byOrg := map[uuid.UUID]gen.ListMembershipsForUserRow{}
		for _, r := range rows {
			byOrg[r.OrgID] = r
		}
		if r, ok := byOrg[orgID]; !ok || r.Slug != "acme" || r.Role != gen.OrgRoleAdmin {
			t.Fatalf("ListMembershipsForUser missing/wrong row for orgID: %+v", byOrg[orgID])
		}
		if r, ok := byOrg[orgID2]; !ok || r.Slug != "beta" || r.Role != gen.OrgRoleViewer {
			t.Fatalf("ListMembershipsForUser missing/wrong row for orgID2: %+v", byOrg[orgID2])
		}

		// Another user with no memberships must see none.
		other := uuid.Must(uuid.NewV7())
		if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: other, Email: "bob@example.test", DisplayName: "Bob"}); err != nil {
			t.Fatalf("CreateUser (other): %v", err)
		}
		none, err := q.ListMembershipsForUser(ctx, other)
		if err != nil {
			t.Fatalf("ListMembershipsForUser(other): %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("ListMembershipsForUser(other) = %+v, want none", none)
		}
	})
}
