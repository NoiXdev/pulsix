package tenancy_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
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
	store := tenancy.NewSQLStore(d)

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
	store := tenancy.NewSQLStore(d)

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
	store := tenancy.NewSQLStore(d)

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
	store := tenancy.NewSQLStore(d)

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
	store := tenancy.NewSQLStore(d)

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

// TestSQLStoreUpdateMembershipRoleBlocksLastOwnerDemotion and
// TestSQLStoreDeleteMembershipBlocksLastOwnerRemoval are the single-caller
// half of Finding 3's fix (Task 10 fix round 1): the atomic guard baked
// into the query itself (tenancy_users.sql) refuses to move the sole owner
// off the owner role, or to remove them, reporting ErrNotFound (0 rows
// affected — the same sentinel a missing row would produce; ChangeRole and
// RemoveMember are what reinterpret it as ErrLastOwner, tested at the
// service level). See TestSQLStoreConcurrentDemotionOfBothOwnersLeavesOneOwner
// below for the concurrency half this guard exists for.
func TestSQLStoreUpdateMembershipRoleBlocksLastOwnerDemotion(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(d)

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "guard-demote", Name: "Guard"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	owner := seedUser(t, q, "guard-demote-owner@example.test")
	if err := store.AddMembership(ctx, org.ID, owner, tenancy.RoleOwner); err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateMembershipRole(ctx, org.ID, owner, tenancy.RoleAdmin); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("demote sole owner: %v, want ErrNotFound (atomic guard refused it)", err)
	}
	role, err := store.GetMembership(ctx, org.ID, owner)
	if err != nil || role != tenancy.RoleOwner {
		t.Fatalf("role after blocked demotion = %s, %v, want unchanged owner", role, err)
	}

	// Granting owner (never demoting away from it) and demoting a non-owner
	// must both still work — the guard is scoped exactly to "the last owner
	// loses the owner role", not to every write on an owner's row.
	other := seedUser(t, q, "guard-demote-other@example.test")
	if err := store.AddMembership(ctx, org.ID, other, tenancy.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateMembershipRole(ctx, org.ID, other, tenancy.RoleOwner); err != nil {
		t.Fatalf("grant owner to a second member: %v", err)
	}
	if err := store.UpdateMembershipRole(ctx, org.ID, owner, tenancy.RoleAdmin); err != nil {
		t.Fatalf("demote the original owner now that a second owner exists: %v", err)
	}
}

func TestSQLStoreDeleteMembershipBlocksLastOwnerRemoval(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(d)

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "guard-remove", Name: "Guard"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	owner := seedUser(t, q, "guard-remove-owner@example.test")
	if err := store.AddMembership(ctx, org.ID, owner, tenancy.RoleOwner); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteMembership(ctx, org.ID, owner); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("remove sole owner: %v, want ErrNotFound (atomic guard refused it)", err)
	}
	if role, err := store.GetMembership(ctx, org.ID, owner); err != nil || role != tenancy.RoleOwner {
		t.Fatalf("membership after blocked removal = %s, %v, want unchanged owner", role, err)
	}
}

// TestSQLStoreConcurrentDemotionOfBothOwnersLeavesOneOwner is Finding 3's
// concurrency regression test (Task 10 fix round 1): the old guard
// (GetMembership, then a separate CountOwners, then UpdateMembershipRole —
// three round trips with no lock tying them together) let two concurrent
// demotions of an org's two owners each observe "two owners" and both
// commit, leaving the organization with none — unrecoverable through the
// API, since only an owner may grant the owner role. The fix folds the
// count into the UPDATE's own WHERE clause behind a row lock (see
// tenancy_users.sql's comment on UpdateMembershipRole), so exactly one of
// two truly concurrent demotions must succeed and the organization must
// still have an owner afterward. This only exercises the real SQL store —
// a Go map has no meaningful concurrent-transaction semantics to test this
// property against (see the fake's own doc comment in service_test.go).
func TestSQLStoreConcurrentDemotionOfBothOwnersLeavesOneOwner(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(d)
	svc := tenancy.NewService(store, &fakeMailer{}, testPublicURL, time.Now)

	owner1 := seedUser(t, q, "conc-owner1@example.test")
	owner2 := seedUser(t, q, "conc-owner2@example.test")
	org, err := svc.CreateOrg(ctx, owner1, "conc-last-owner", "Conc")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMembership(ctx, org.ID, owner2, tenancy.RoleOwner); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, actor := range []uuid.UUID{owner1, owner2} {
		wg.Add(1)
		go func(i int, actor uuid.UUID) {
			defer wg.Done()
			<-start
			errs[i] = svc.ChangeRole(ctx, org.ID, tenancy.RoleOwner, actor, actor, tenancy.RoleAdmin)
		}(i, actor)
	}
	close(start)
	wg.Wait()

	succeeded, lastOwner := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			succeeded++
		case errors.Is(e, tenancy.ErrLastOwner):
			lastOwner++
		default:
			t.Fatalf("unexpected error from a concurrent demotion: %v", e)
		}
	}
	if succeeded != 1 || lastOwner != 1 {
		t.Fatalf("succeeded=%d refused-as-last-owner=%d (errs=%v), want exactly one of each", succeeded, lastOwner, errs)
	}
	n, err := store.CountOwners(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("CountOwners after concurrent demotion = %d, want 1", n)
	}
}

func TestSQLStoreListMembersIncludesDisabledAt(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(d)

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
	if err := store.DisableUser(ctx, uuid.Must(uuid.NewV7()), at); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("DisableUser for an unknown user: %v, want ErrNotFound", err)
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
	store := tenancy.NewSQLStore(d)

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
	store := tenancy.NewSQLStore(d)

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
	store := tenancy.NewSQLStore(d)

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
	store := tenancy.NewSQLStore(d)

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	inviter := seedUser(t, q, "inviter@example.test")
	if err := store.AddMembership(ctx, org.ID, inviter, tenancy.RoleAdmin); err != nil {
		t.Fatal(err)
	}
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

	// DeleteInvite removes an undelivered invitation for good — and only
	// within its own organization.
	undelivered := tenancy.Invite{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Email: "undelivered@example.test", Role: tenancy.RoleViewer,
		InvitedBy: inviter, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.InsertInvite(ctx, undelivered, hashOf("undelivered")); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteInvite(ctx, uuid.Must(uuid.NewV7()), undelivered.ID); err != nil {
		t.Fatalf("DeleteInvite for another organization: %v", err)
	}
	if _, err := store.TakeInvite(ctx, hashOf("undelivered"), now); err != nil {
		t.Fatalf("an invitation was deleted through another organization: %v", err)
	}
	gone := tenancy.Invite{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Email: "gone@example.test", Role: tenancy.RoleViewer,
		InvitedBy: inviter, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.InsertInvite(ctx, gone, hashOf("gone")); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteInvite(ctx, org.ID, gone.ID); err != nil {
		t.Fatalf("DeleteInvite: %v", err)
	}
	if _, err := store.TakeInvite(ctx, hashOf("gone"), now); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("TakeInvite after DeleteInvite: %v, want ErrNotFound", err)
	}
}

// TestSQLStoreInviteNeedsItsIssuersAuthority is backlog 7.29: an
// invitation carries its issuer's authority to add members, so it lapses
// the moment that authority does — the issuer removed from the
// organization, demoted below admin, or deactivated. Before, a removed
// admin could keep an unaccepted invitation to an address of their own
// and walk back in as admin with it.
func TestSQLStoreInviteNeedsItsIssuersAuthority(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(d)
	now := time.Now()

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	owner := seedUser(t, q, "owner@example.test")
	if err := store.AddMembership(ctx, org.ID, owner, tenancy.RoleOwner); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		lapse func(t *testing.T, issuer uuid.UUID)
	}{
		{"removed", func(t *testing.T, issuer uuid.UUID) {
			if err := store.DeleteMembership(ctx, org.ID, issuer); err != nil {
				t.Fatal(err)
			}
		}},
		{"demoted", func(t *testing.T, issuer uuid.UUID) {
			if err := store.UpdateMembershipRole(ctx, org.ID, issuer, tenancy.RoleOperator); err != nil {
				t.Fatal(err)
			}
		}},
		{"deactivated", func(t *testing.T, issuer uuid.UUID) {
			if err := store.DisableUser(ctx, issuer, now); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issuer := seedUser(t, q, c.name+"-admin@example.test")
			if err := store.AddMembership(ctx, org.ID, issuer, tenancy.RoleAdmin); err != nil {
				t.Fatal(err)
			}
			inv := tenancy.Invite{
				ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Email: c.name + "-alt@example.test", Role: tenancy.RoleAdmin,
				InvitedBy: issuer, ExpiresAt: now.Add(time.Hour),
			}
			hash := hashOf(c.name + "-invite")
			if err := store.InsertInvite(ctx, inv, hash); err != nil {
				t.Fatal(err)
			}
			if _, err := store.PeekInvite(ctx, hash, now); err != nil {
				t.Fatalf("PeekInvite while the issuer is an admin: %v", err)
			}

			c.lapse(t, issuer)

			if _, err := store.PeekInvite(ctx, hash, now); !errors.Is(err, tenancy.ErrNotFound) {
				t.Fatalf("PeekInvite after the issuer was %s: %v, want ErrNotFound", c.name, err)
			}
			if _, err := store.TakeInvite(ctx, hash, now); !errors.Is(err, tenancy.ErrNotFound) {
				t.Fatalf("TakeInvite after the issuer was %s: %v, want ErrNotFound", c.name, err)
			}
		})
	}

	// An owner's invitation is as good as an admin's.
	inv := tenancy.Invite{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Email: "by-owner@example.test", Role: tenancy.RoleViewer,
		InvitedBy: owner, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.InsertInvite(ctx, inv, hashOf("by-owner")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TakeInvite(ctx, hashOf("by-owner"), now); err != nil {
		t.Fatalf("TakeInvite of an owner's invitation: %v", err)
	}
}

func TestSQLStoreTakeInviteExcludesExpired(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(d)

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	inviter := seedUser(t, q, "inviter2@example.test")
	if err := store.AddMembership(ctx, org.ID, inviter, tenancy.RoleAdmin); err != nil {
		t.Fatal(err)
	}
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
	store := tenancy.NewSQLStore(d)

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

// TestSQLStoreDisableUserKeepsOneInstanceAdmin covers M6's atomic guard
// against the real database: the last active instance administrator cannot
// be disabled, one of two can, and two administrators disabling each other
// concurrently can never both succeed.
func TestSQLStoreDisableUserKeepsOneInstanceAdmin(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(d)
	makeAdmin := func(email string) uuid.UUID {
		id := seedUser(t, q, email)
		if _, err := d.Pool().Exec(ctx, `UPDATE users SET instance_admin = true WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	first := makeAdmin("first-admin@example.test")
	if err := store.DisableUser(ctx, first, time.Now()); !errors.Is(err, tenancy.ErrLastInstanceAdmin) {
		t.Fatalf("disabling the only instance admin: %v, want ErrLastInstanceAdmin", err)
	}
	second := makeAdmin("second-admin@example.test")

	errs := make(chan error, 2)
	for _, pair := range [][2]uuid.UUID{{first, second}, {second, first}} {
		go func(target uuid.UUID) { errs <- store.DisableUser(ctx, target, time.Now()) }(pair[0])
	}
	var ok, refused int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, tenancy.ErrLastInstanceAdmin):
			refused++
		default:
			t.Fatalf("concurrent DisableUser: %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("concurrent deactivation of both admins: %d succeeded, %d refused; want exactly one of each", ok, refused)
	}
	var active int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM users WHERE instance_admin AND disabled_at IS NULL`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active instance admins = %d, want 1", active)
	}
}

// TestSQLStorePendingInvitesListAndRevoke (backlog 7.29): the list holds
// exactly the invitations TakeInvite would still take — not an accepted
// one, an expired one, another organization's, or one whose sender lost
// the right to send it — and a revoke removes an open invitation once.
func TestSQLStorePendingInvitesListAndRevoke(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(d)
	now := time.Now()

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme"}
	other := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "other", Name: "Other"}
	for _, o := range []tenancy.Org{org, other} {
		if err := store.CreateOrg(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	admin := seedUser(t, q, "pending-admin@example.test")
	demoted := seedUser(t, q, "pending-demoted@example.test")
	for _, m := range []struct {
		org  uuid.UUID
		user uuid.UUID
		role tenancy.Role
	}{{org.ID, admin, tenancy.RoleAdmin}, {other.ID, admin, tenancy.RoleAdmin}, {org.ID, demoted, tenancy.RoleOperator}} {
		if err := store.AddMembership(ctx, m.org, m.user, m.role); err != nil {
			t.Fatal(err)
		}
	}
	invite := func(orgID, by uuid.UUID, email string, expires time.Time) tenancy.Invite {
		t.Helper()
		inv := tenancy.Invite{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Email: email, Role: tenancy.RoleViewer, InvitedBy: by, ExpiresAt: expires}
		if err := store.InsertInvite(ctx, inv, hashOf(email)); err != nil {
			t.Fatal(err)
		}
		return inv
	}
	open := invite(org.ID, admin, "open@example.test", now.Add(time.Hour))
	invite(org.ID, admin, "expired@example.test", now.Add(-time.Minute))
	invite(org.ID, demoted, "lapsed@example.test", now.Add(time.Hour))
	invite(other.ID, admin, "elsewhere@example.test", now.Add(time.Hour))
	accepted := invite(org.ID, admin, "accepted@example.test", now.Add(time.Hour))
	if _, err := store.TakeInvite(ctx, hashOf(accepted.Email), now); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListPendingInvites(ctx, org.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != open.ID || list[0].InvitedByName != "pending-admin@example.test" || list[0].Role != tenancy.RoleViewer {
		t.Fatalf("pending invites = %+v, want only the open one", list)
	}

	if err := store.RevokeInvite(ctx, other.ID, open.ID); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("revoking through another organization = %v, want ErrNotFound", err)
	}
	if err := store.RevokeInvite(ctx, org.ID, accepted.ID); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("revoking an accepted invitation = %v, want ErrNotFound", err)
	}
	if err := store.RevokeInvite(ctx, org.ID, open.ID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if err := store.RevokeInvite(ctx, org.ID, open.ID); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("revoking twice = %v, want ErrNotFound", err)
	}
	if _, err := store.TakeInvite(ctx, hashOf(open.Email), now); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("a revoked invitation was taken: %v", err)
	}
}
