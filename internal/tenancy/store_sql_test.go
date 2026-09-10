package tenancy_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/tenancy"
)

func seedUser(t *testing.T, q *gen.Queries, email string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(context.Background(), gen.CreateUserParams{ID: id, Email: email, DisplayName: email}); err != nil {
		t.Fatal(err)
	}
	return id
}

func hashOf(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func TestSQLStoreCreateOrgAndGetBySlug(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := tenancy.NewSQLStore(gen.New(d.Pool()))

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	got, err := store.GetOrgBySlug(ctx, "acme")
	if err != nil || got.ID != org.ID || got.Name != "Acme" {
		t.Fatalf("GetOrgBySlug = %+v, %v", got, err)
	}
	byID, err := store.GetOrgByID(ctx, org.ID)
	if err != nil || byID.Slug != "acme" {
		t.Fatalf("GetOrgByID = %+v, %v", byID, err)
	}
	if _, err := store.GetOrgBySlug(ctx, "nope"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("unknown slug: %v, want ErrNotFound", err)
	}
	if _, err := store.GetOrgByID(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("unknown id: %v, want ErrNotFound", err)
	}
}

func TestSQLStoreCreateOrgDuplicateSlug(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := tenancy.NewSQLStore(gen.New(d.Pool()))

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	dup := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme Again"}
	if err := store.CreateOrg(ctx, dup); !errors.Is(err, tenancy.ErrSlugTaken) {
		t.Fatalf("duplicate slug: %v, want ErrSlugTaken", err)
	}
}

func TestSQLStoreUpdateOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := tenancy.NewSQLStore(gen.New(d.Pool()))

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	org.Name = "Acme Corp"
	if err := store.UpdateOrg(ctx, org); err != nil {
		t.Fatalf("UpdateOrg: %v", err)
	}
	got, err := store.GetOrgByID(ctx, org.ID)
	if err != nil || got.Name != "Acme Corp" {
		t.Fatalf("after update: %+v, %v", got, err)
	}
}

func TestSQLStoreMembershipLifecycle(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(q)

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	uid := seedUser(t, q, "member@example.test")

	if _, err := store.GetMembership(ctx, org.ID, uid); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("membership before AddMembership: %v, want ErrNotFound", err)
	}
	if err := store.AddMembership(ctx, org.ID, uid, tenancy.RoleViewer); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	role, err := store.GetMembership(ctx, org.ID, uid)
	if err != nil || role != tenancy.RoleViewer {
		t.Fatalf("GetMembership after add: %s, %v", role, err)
	}
	if err := store.UpdateMembershipRole(ctx, org.ID, uid, tenancy.RoleAdmin); err != nil {
		t.Fatalf("UpdateMembershipRole: %v", err)
	}
	role, err = store.GetMembership(ctx, org.ID, uid)
	if err != nil || role != tenancy.RoleAdmin {
		t.Fatalf("GetMembership after update: %s, %v", role, err)
	}
	if err := store.DeleteMembership(ctx, org.ID, uid); err != nil {
		t.Fatalf("DeleteMembership: %v", err)
	}
	if _, err := store.GetMembership(ctx, org.ID, uid); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("membership after delete: %v, want ErrNotFound", err)
	}
	// A second delete of an already-removed membership must report
	// ErrNotFound rather than silently succeeding again.
	if err := store.DeleteMembership(ctx, org.ID, uid); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("second DeleteMembership: %v, want ErrNotFound", err)
	}
}

func TestSQLStoreCountOwners(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(q)

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	owner1 := seedUser(t, q, "owner1@example.test")
	owner2 := seedUser(t, q, "owner2@example.test")
	viewer := seedUser(t, q, "viewer@example.test")
	for _, m := range []struct {
		id   uuid.UUID
		role tenancy.Role
	}{{owner1, tenancy.RoleOwner}, {owner2, tenancy.RoleOwner}, {viewer, tenancy.RoleViewer}} {
		if err := store.AddMembership(ctx, org.ID, m.id, m.role); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.CountOwners(ctx, org.ID)
	if err != nil || n != 2 {
		t.Fatalf("CountOwners = %d, %v, want 2", n, err)
	}
}

func TestSQLStoreListMembersIncludesDisabledAt(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(q)

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	active := seedUser(t, q, "active@example.test")
	disabled := seedUser(t, q, "disabled@example.test")
	if err := store.AddMembership(ctx, org.ID, active, tenancy.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMembership(ctx, org.ID, disabled, tenancy.RoleViewer); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Microsecond)
	if err := store.DisableUser(ctx, disabled, at); err != nil {
		t.Fatal(err)
	}

	members, err := store.ListMembers(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("ListMembers = %d members, want 2", len(members))
	}
	byID := map[uuid.UUID]tenancy.Member{}
	for _, m := range members {
		byID[m.UserID] = m
	}
	if byID[active].DisabledAt != nil {
		t.Fatalf("active member has DisabledAt = %v, want nil", byID[active].DisabledAt)
	}
	if byID[disabled].DisabledAt == nil || !byID[disabled].DisabledAt.Equal(at) {
		t.Fatalf("disabled member DisabledAt = %v, want %v", byID[disabled].DisabledAt, at)
	}
}

func TestSQLStoreListOrgsForUser(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(q)

	uid := seedUser(t, q, "multi-org@example.test")
	orgA := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "org-a", Name: "Org A"}
	orgB := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "org-b", Name: "Org B"}
	for _, o := range []tenancy.Org{orgA, orgB} {
		if err := store.CreateOrg(ctx, o); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMembership(ctx, o.ID, uid, tenancy.RoleOwner); err != nil {
			t.Fatal(err)
		}
	}
	memberships, err := store.ListOrgsForUser(ctx, uid)
	if err != nil {
		t.Fatalf("ListOrgsForUser: %v", err)
	}
	if len(memberships) != 2 {
		t.Fatalf("ListOrgsForUser = %d, want 2", len(memberships))
	}
}

func TestSQLStoreUserLifecycle(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := tenancy.NewSQLStore(gen.New(d.Pool()))

	if _, err := store.GetUserByEmail(ctx, "nobody@example.test"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("unknown user: %v, want ErrNotFound", err)
	}
	u := tenancy.User{ID: uuid.Must(uuid.NewV7()), Email: "invited@example.test", DisplayName: "Invited"}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	got, err := store.GetUserByEmail(ctx, "invited@example.test")
	if err != nil || got.ID != u.ID || got.PasswordHash != nil {
		t.Fatalf("GetUserByEmail after create: %+v, %v", got, err)
	}
	if err := store.SetUserPasswordAndName(ctx, u.ID, "$argon2id$fake", "Invited User"); err != nil {
		t.Fatalf("SetUserPasswordAndName: %v", err)
	}
	got, err = store.GetUserByEmail(ctx, "invited@example.test")
	if err != nil || got.PasswordHash == nil || *got.PasswordHash != "$argon2id$fake" || got.DisplayName != "Invited User" {
		t.Fatalf("GetUserByEmail after SetUserPasswordAndName: %+v, %v", got, err)
	}
}

func TestSQLStoreRevokeSessionsAndTokensForUser(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(q)

	uid := seedUser(t, q, "revoke-me@example.test")
	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateSession(ctx, gen.CreateSessionParams{
		ID: uuid.Must(uuid.NewV7()), UserID: uid, SecretHash: hashOf("session-secret"),
		MfaComplete: true, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.InsertAPIToken(ctx, gen.InsertAPITokenParams{
		ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org.ID, Name: "ci", Role: gen.OrgRoleViewer,
		SecretHash: hashOf("token-secret"),
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.RevokeSessionsForUser(ctx, uid); err != nil {
		t.Fatalf("RevokeSessionsForUser: %v", err)
	}
	if err := store.RevokeTokensForUser(ctx, uid); err != nil {
		t.Fatalf("RevokeTokensForUser: %v", err)
	}

	var revokedSessions, revokedTokens int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NOT NULL`, uid).Scan(&revokedSessions); err != nil {
		t.Fatal(err)
	}
	if revokedSessions != 1 {
		t.Fatalf("revoked sessions = %d, want 1", revokedSessions)
	}
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE user_id = $1 AND revoked_at IS NOT NULL`, uid).Scan(&revokedTokens); err != nil {
		t.Fatal(err)
	}
	if revokedTokens != 1 {
		t.Fatalf("revoked tokens = %d, want 1", revokedTokens)
	}
}

func TestSQLStoreInviteLifecycle(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(q)

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	inviter := seedUser(t, q, "inviter@example.test")
	now := time.Now()
	inv := tenancy.Invite{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Email: "invitee@example.test", Role: tenancy.RoleOperator,
		InvitedBy: inviter, ExpiresAt: now.Add(time.Hour),
	}
	hash := hashOf("invite-secret")
	if err := store.InsertInvite(ctx, inv, hash); err != nil {
		t.Fatalf("InsertInvite: %v", err)
	}

	if _, err := store.TakeInvite(ctx, hashOf("never-inserted"), now); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("unknown hash: %v, want ErrNotFound", err)
	}

	got, err := store.TakeInvite(ctx, hash, now)
	if err != nil {
		t.Fatalf("TakeInvite: %v", err)
	}
	if got.ID != inv.ID || got.OrgID != org.ID || got.Email != "invitee@example.test" || got.Role != tenancy.RoleOperator {
		t.Fatalf("TakeInvite row = %+v", got)
	}

	// Single-use: a second TakeInvite for the same token must fail, since
	// accepted_at is now set.
	if _, err := store.TakeInvite(ctx, hash, now); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("second TakeInvite: %v, want ErrNotFound", err)
	}
}

func TestSQLStoreTakeInviteExcludesExpired(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(q)

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	inviter := seedUser(t, q, "inviter2@example.test")
	now := time.Now()
	inv := tenancy.Invite{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Email: "late@example.test", Role: tenancy.RoleViewer,
		InvitedBy: inviter, ExpiresAt: now.Add(-time.Minute), // already expired
	}
	hash := hashOf("expired-invite")
	if err := store.InsertInvite(ctx, inv, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TakeInvite(ctx, hash, now); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("expired invite: %v, want ErrNotFound", err)
	}
}

// TestInvitesAreIsolatedPerOrgByRLS mirrors
// auth_test.TestAPITokensAreIsolatedPerOrgByRLS: it exercises the
// org_isolation RLS policy on the invites table directly, via d.WithOrg,
// rather than through SQLStore/gen.Queries the way every other test in this
// file does (those only ever exercise org_isolation_bypass, since testdb.Open
// never sets pulsix.org_id). This is the one test in the suite that would
// fail if org_isolation's USING clause on invites were ever weakened to
// USING (true).
func TestInvitesAreIsolatedPerOrgByRLS(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(q)

	orgA := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "rls-invite-a", Name: "A"}
	orgB := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "rls-invite-b", Name: "B"}
	if err := store.CreateOrg(ctx, orgA); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrg(ctx, orgB); err != nil {
		t.Fatal(err)
	}
	inviter := seedUser(t, q, "rls-inviter@example.test")
	now := time.Now()
	for _, o := range []tenancy.Org{orgA, orgB} {
		inv := tenancy.Invite{
			ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, Email: "rls-" + o.Slug + "@example.test",
			Role: tenancy.RoleViewer, InvitedBy: inviter, ExpiresAt: now.Add(time.Hour),
		}
		if err := store.InsertInvite(ctx, inv, hashOf("rls-"+o.Slug)); err != nil {
			t.Fatal(err)
		}
	}

	var seen []uuid.UUID
	err := d.WithOrg(ctx, orgA.ID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id FROM invites`)
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
	if len(seen) != 1 || seen[0] != orgA.ID {
		t.Fatalf("RLS leak: saw %v, want only [%s]", seen, orgA.ID)
	}
}
