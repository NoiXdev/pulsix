package personaltokens_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/personaltokens"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// sqlService is the service on the real store and the real membership
// lookup, with a clock that has nanoseconds.
func (f fixture) sqlService(members personaltokens.Members) *personaltokens.Service {
	if members == nil {
		members = tenancy.NewService(tenancy.NewSQLStore(f.d), nil, "", time.Now)
	}
	return personaltokens.NewService(f.store, members, func() time.Time {
		return time.Now().Truncate(time.Microsecond).Add(789 * time.Nanosecond)
	})
}

func (f fixture) tokenRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.d.QueryRow(context.Background(), `SELECT count(*) FROM personal_tokens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestSQLManageRoundTrip drives the owner's and an organization admin's
// operations through the real store.
func TestSQLManageRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "round@example.test")
	admin := f.user(t, "round-admin@example.test")
	a := f.org(t, "round-a", map[uuid.UUID]tenancy.Role{u: tenancy.RoleAdmin, admin: tenancy.RoleOwner})
	b := f.org(t, "round-b", map[uuid.UUID]tenancy.Role{u: tenancy.RoleOperator})
	c := f.org(t, "round-c", map[uuid.UUID]tenancy.Role{u: tenancy.RoleViewer})
	svc := f.sqlService(nil)

	exp := time.Now().Add(72*time.Hour + 789)
	plain, created, err := svc.Create(ctx, u, personaltokens.NewToken{Name: "deploy", ExpiresAt: &exp,
		Grants: []personaltokens.GrantInput{{OrgID: a, Role: tenancy.RoleOperator}, {OrgID: b, Role: tenancy.RoleViewer}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.NewSQLTokenStore(f.d).GetPersonalByHash(ctx, hashOf(t, plain), time.Now()); err != nil {
		t.Fatalf("the minted secret does not resolve: %v", err)
	}
	list, err := svc.List(ctx, u)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if !list[0].CreatedAt.Equal(created.CreatedAt) || !list[0].ExpiresAt.Equal(*created.ExpiresAt) {
		t.Fatalf("list %v / %v, create %v / %v", list[0].CreatedAt, list[0].ExpiresAt, created.CreatedAt, created.ExpiresAt)
	}
	if len(list[0].Grants) != 2 || list[0].Grants[0].OrgSlug != "round-a" || list[0].Grants[0].State != personaltokens.GrantActive {
		t.Fatalf("grants = %+v", list[0].Grants)
	}

	// An admin of B blocks the grant there; the owner's view says so.
	inB := db.WithOrgScope(ctx, b)
	if _, err := svc.RevokeGrant(inB, b, created.ID, admin); err != nil {
		t.Fatal(err)
	}
	// A: cap changed; B: blocked, kept; C: added.
	grants := []personaltokens.GrantInput{{OrgID: a, Role: tenancy.RoleAdmin}, {OrgID: c, Role: tenancy.RoleViewer}}
	_, after, changes, err := svc.Update(ctx, u, created.ID, personaltokens.Change{Grants: &grants})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || len(after.Grants) != 3 || after.Grants[1].OrgID != b || after.Grants[1].State != personaltokens.GrantBlocked {
		t.Fatalf("changes %+v, grants %+v", changes, after.Grants)
	}
	if !after.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("created_at moved: %v → %v", created.CreatedAt, after.CreatedAt)
	}

	// The organization's view: owner, cap and block, its grants only.
	inA := db.WithOrgScope(ctx, a)
	entries, err := svc.ListForOrg(inA, a)
	if err != nil || len(entries) != 1 {
		t.Fatalf("A entries = %+v, %v", entries, err)
	}
	if e := entries[0]; e.OwnerID != u || e.OwnerEmail != "round@example.test" || e.Role != tenancy.RoleAdmin || e.Blocked || e.Name != "deploy" {
		t.Fatalf("A entry = %+v", e)
	}
	if entries, err := svc.ListForOrg(inB, b); err != nil || len(entries) != 1 || !entries[0].Blocked {
		t.Fatalf("B entries = %+v, %v", entries, err)
	}
	if org, n, err := svc.GrantCount(ctx, created.ID); err != nil || n != 2 || org != uuid.Nil {
		t.Fatalf("GrantCount = %v %d %v", org, n, err)
	}
	if _, err := svc.UnblockGrant(inB, b, created.ID); err != nil {
		t.Fatal(err)
	}
	if entries, _ := svc.ListForOrg(inB, b); len(entries) != 0 {
		t.Fatalf("B entries after unblock = %+v", entries)
	}

	// The switch shows on the owner's grants as off.
	if err := svc.SetAllowed(inA, a, false); err != nil {
		t.Fatal(err)
	}
	list, _ = svc.List(ctx, u)
	if list[0].Grants[0].OrgID != a || list[0].Grants[0].State != personaltokens.GrantOff {
		t.Fatalf("grants with A off = %+v", list[0].Grants)
	}
	only := []personaltokens.GrantInput{{OrgID: c, Role: tenancy.RoleViewer}}
	if _, _, _, err := svc.Update(ctx, u, created.ID, personaltokens.Change{Grants: &only}); err != nil {
		t.Fatal(err)
	}
	if org, n, err := svc.GrantCount(ctx, created.ID); err != nil || n != 1 || org != c {
		t.Fatalf("GrantCount = %v %d %v", org, n, err)
	}

	// Rename and clear the expiry, then revoke: gone from both views.
	name := "renamed"
	if _, after, _, err = svc.Update(ctx, u, created.ID, personaltokens.Change{Name: &name, Expiry: auth.ExpiryClear}); err != nil || after.Name != "renamed" || after.ExpiresAt != nil {
		t.Fatalf("rename: %+v, %v", after.PersonalToken, err)
	}
	if _, err := svc.Revoke(ctx, u, created.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.List(ctx, u); len(list) != 0 {
		t.Fatalf("list after revoke = %+v", list)
	}
	if entries, _ := svc.ListForOrg(db.WithOrgScope(ctx, c), c); len(entries) != 0 {
		t.Fatalf("C entries after revoke = %+v", entries)
	}
	if _, err := svc.Revoke(ctx, u, created.ID); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
}

func hashOf(t *testing.T, plain string) []byte {
	t.Helper()
	secret, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(plain, "pxp_"))
	if err != nil || !strings.HasPrefix(plain, "pxp_") {
		t.Fatalf("not a personal token: %q", plain)
	}
	sum := sha256.Sum256(secret)
	return sum[:]
}

// TestSQLAllowedFailsClosedUnderForeignScope (carried from Task 16's
// review): read under another organization's scope, the switch's row is
// hidden and "no row" would read as allowed, so Allowed refuses to answer.
func TestSQLAllowedFailsClosedUnderForeignScope(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "scope@example.test")
	a := f.org(t, "scope-a", map[uuid.UUID]tenancy.Role{u: tenancy.RoleOwner})
	b := f.org(t, "scope-b", map[uuid.UUID]tenancy.Role{u: tenancy.RoleOwner})
	if err := f.store.SetAllowed(db.WithOrgScope(ctx, a), a, false); err != nil {
		t.Fatal(err)
	}
	if on, err := f.store.Allowed(db.WithOrgScope(ctx, b), a); err == nil || on {
		t.Fatalf("Allowed(A) bound to B = %v, %v; want an error", on, err)
	}
	for name, c := range map[string]context.Context{"bound to A": db.WithOrgScope(ctx, a), "instance level": ctx} {
		if on, err := f.store.Allowed(c, a); err != nil || on {
			t.Fatalf("%s: Allowed(A) = %v, %v; want off", name, on, err)
		}
	}
}

// memberEverywhere claims every user is an owner everywhere: it lets a
// create pass the service's membership check where the database has none.
type memberEverywhere struct{}

func (memberEverywhere) MembershipOf(context.Context, uuid.UUID, uuid.UUID) (tenancy.Role, error) {
	return tenancy.RoleOwner, nil
}

// TestSQLCreateRollsBackOnGrantFailure: a grant the database refuses (the
// membership ended after the check) fails the whole create — no token is
// left behind without its grants — and reads as ErrNotMember.
func TestSQLCreateRollsBackOnGrantFailure(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "rollback@example.test")
	a := f.org(t, "rollback-a", map[uuid.UUID]tenancy.Role{u: tenancy.RoleOwner})
	gone := f.org(t, "rollback-gone", nil)
	_, _, err := f.sqlService(memberEverywhere{}).Create(context.Background(), u, personaltokens.NewToken{Name: "x",
		Grants: []personaltokens.GrantInput{{OrgID: a, Role: tenancy.RoleViewer}, {OrgID: gone, Role: tenancy.RoleViewer}}})
	var ge *personaltokens.GrantError
	if !errors.As(err, &ge) || ge.OrgID != gone || !errors.Is(err, personaltokens.ErrNotMember) {
		t.Fatalf("err = %v, want ErrNotMember for the gone organization", err)
	}
	if n := f.tokenRows(t); n != 0 {
		t.Fatalf("%d token rows left, want 0", n)
	}
	if n := f.grantCount(t, context.Background()); n != 0 {
		t.Fatalf("%d grant rows left, want 0", n)
	}
}

// TestSQLCreateLimitHoldsConcurrently: at 49 tokens, two creates at once
// cannot both pass the limit.
func TestSQLCreateLimitHoldsConcurrently(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "limit@example.test")
	a := f.org(t, "limit-a", map[uuid.UUID]tenancy.Role{u: tenancy.RoleViewer})
	svc := f.sqlService(nil)
	in := personaltokens.NewToken{Name: "n", Grants: []personaltokens.GrantInput{{OrgID: a, Role: tenancy.RoleViewer}}}
	for range personaltokens.MaxTokensPerUser - 1 {
		if _, _, err := svc.Create(ctx, u, in); err != nil {
			t.Fatal(err)
		}
	}
	// Both creates meet in the membership check, which runs after the
	// count: without the owner lock both would count 49 and pass. With it
	// the second waits in the count, so the first gives up waiting for it
	// and commits.
	racing := f.sqlService(&barrierMembers{inner: tenancy.NewService(tenancy.NewSQLStore(f.d), nil, "", time.Now), all: make(chan struct{})})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = racing.Create(ctx, u, in)
		}()
	}
	wg.Wait()
	limited := 0
	for _, err := range errs {
		switch {
		case errors.Is(err, personaltokens.ErrLimit):
			limited++
		case err != nil:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if limited != 1 || f.tokenRows(t) != personaltokens.MaxTokensPerUser {
		t.Fatalf("limited %d, rows %d; want 1 and %d", limited, f.tokenRows(t), personaltokens.MaxTokensPerUser)
	}
}

// barrierMembers holds each membership check until two have arrived, or
// half a second has passed.
type barrierMembers struct {
	inner personaltokens.Members
	mu    sync.Mutex
	n     int
	all   chan struct{}
}

func (b *barrierMembers) MembershipOf(ctx context.Context, orgID, userID uuid.UUID) (tenancy.Role, error) {
	b.mu.Lock()
	b.n++
	if b.n == 2 {
		close(b.all)
	}
	b.mu.Unlock()
	select {
	case <-b.all:
	case <-time.After(500 * time.Millisecond):
	}
	return b.inner.MembershipOf(ctx, orgID, userID)
}

// TestSQLOwnerCannotTouchBlockedGrant: the store's owner writes never reach
// a blocked grant.
func TestSQLOwnerCannotTouchBlockedGrant(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "blocked@example.test")
	a := f.org(t, "blocked-a", map[uuid.UUID]tenancy.Role{u: tenancy.RoleOwner})
	tok := f.token(t, u)
	f.grant(t, tok, u, a, tenancy.RoleViewer)
	if _, err := f.store.BlockGrant(db.WithOrgScope(ctx, a), tok, a, u); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetGrantRole(ctx, tok, a, tenancy.RoleOwner); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("SetGrantRole on a blocked grant: %v", err)
	}
	if err := f.store.DeleteGrant(ctx, tok, a); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("DeleteGrant on a blocked grant: %v", err)
	}
	if role, blocked, err := f.store.Cap(ctx, tok, a); err != nil || !blocked || role != tenancy.RoleViewer {
		t.Fatalf("grant after the owner's writes = %q %v %v", role, blocked, err)
	}
	if err := f.store.UnblockOwner(ctx, uuid.Must(uuid.NewV7()), u); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("UnblockOwner without a block: %v", err)
	}
	if blocked, err := f.store.OrgBlocked(db.WithOrgScope(ctx, uuid.Must(uuid.NewV7())), u, a); err == nil || blocked {
		t.Fatalf("OrgBlocked bound to another organization = %v, %v; want an error", blocked, err)
	}
	if _, err := f.store.UpdateToken(ctx, auth.PersonalToken{ID: tok, UserID: uuid.Must(uuid.NewV7()), Name: "x"}); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("UpdateToken of another user's token: %v", err)
	}
	if _, err := f.store.OrgEntry(db.WithOrgScope(ctx, a), a, uuid.Must(uuid.NewV7())); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("OrgEntry without a grant: %v", err)
	}
}

// TestSQLGrantCountFailsClosedUnderOrgScope (fix round 1): bound to one
// organization, row-level security would hide the token's other grants and
// turn two into one; the count refuses instead.
func TestSQLGrantCountFailsClosedUnderOrgScope(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "count@example.test")
	a := f.org(t, "count-a", map[uuid.UUID]tenancy.Role{u: tenancy.RoleOwner})
	b := f.org(t, "count-b", map[uuid.UUID]tenancy.Role{u: tenancy.RoleOwner})
	tok := f.token(t, u)
	f.grant(t, tok, u, a, tenancy.RoleViewer)
	f.grant(t, tok, u, b, tenancy.RoleViewer)
	svc := f.sqlService(nil)
	if org, n, err := svc.GrantCount(db.WithOrgScope(ctx, a), tok); err == nil {
		t.Fatalf("GrantCount bound to A = %v, %d; want an error", org, n)
	}
	if _, n, err := svc.GrantCount(ctx, tok); err != nil || n != 2 {
		t.Fatalf("GrantCount at instance level = %d, %v; want 2", n, err)
	}
}

// TestSQLAdminRevokeIsAtomic (fix round 1): two admins revoking the same
// grant at once get one success, which names the cap it blocked, and one
// grant_blocked — never a not-found.
func TestSQLAdminRevokeIsAtomic(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "atomic@example.test")
	a := f.org(t, "atomic-a", map[uuid.UUID]tenancy.Role{u: tenancy.RoleOwner})
	tok := f.token(t, u)
	f.grant(t, tok, u, a, tenancy.RoleOperator)
	svc := f.sqlService(nil)
	inA := db.WithOrgScope(ctx, a)
	type result struct {
		e   personaltokens.OrgEntry
		err error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := svc.RevokeGrant(inA, a, tok, uuid.Nil)
			results[i] = result{e, err}
		}()
	}
	wg.Wait()
	ok, blocked := 0, 0
	for _, r := range results {
		switch {
		case r.err == nil:
			ok++
			if !r.e.Blocked || r.e.Role != tenancy.RoleOperator || r.e.OwnerID != u || r.e.Name != "laptop" {
				t.Fatalf("entry = %+v", r.e)
			}
		case errors.Is(r.err, personaltokens.ErrBlocked):
			blocked++
		default:
			t.Fatalf("unexpected error: %v", r.err)
		}
	}
	if ok != 1 || blocked != 1 {
		t.Fatalf("ok %d, blocked %d; want 1 and 1", ok, blocked)
	}
}

// TestSQLBlockIsPerPerson: the block of one token's grant keeps every token
// of the user out of the organization, the revoked token's block included;
// lifting it removes every blocked row of the user there.
func TestSQLBlockIsPerPerson(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "person@example.test")
	a := f.org(t, "person-a", map[uuid.UUID]tenancy.Role{u: tenancy.RoleOwner})
	svc := f.sqlService(nil)
	in := personaltokens.NewToken{Name: "t", Grants: []personaltokens.GrantInput{{OrgID: a, Role: tenancy.RoleViewer}}}
	_, first, err := svc.Create(ctx, u, in)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := svc.Create(ctx, u, in)
	if err != nil {
		t.Fatal(err)
	}
	inA := db.WithOrgScope(ctx, a)
	for _, id := range []uuid.UUID{first.ID, second.ID} {
		if _, err := svc.RevokeGrant(inA, a, id, uuid.Nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Revoke(ctx, u, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Create(ctx, u, in); !errors.Is(err, personaltokens.ErrBlocked) {
		t.Fatalf("create while blocked: %v, want ErrBlocked", err)
	}
	entries, err := svc.ListForOrg(inA, a)
	if err != nil || len(entries) != 2 || !entries[0].TokenRevoked || entries[1].TokenRevoked {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	// Unblocking through the revoked token's entry lifts both blocks.
	if _, err := svc.UnblockGrant(inA, a, first.ID); err != nil {
		t.Fatal(err)
	}
	if entries, _ := svc.ListForOrg(inA, a); len(entries) != 0 {
		t.Fatalf("entries after the unblock = %+v", entries)
	}
	if _, _, err := svc.Create(ctx, u, in); err != nil {
		t.Fatalf("create after the unblock: %v", err)
	}
	// A revoked token's grant that was never blocked is not listed and
	// cannot be blocked.
	if _, err := svc.RevokeGrant(inA, a, first.ID, uuid.Nil); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("revoking a lifted grant of a revoked token: %v", err)
	}
}
