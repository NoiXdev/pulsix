package personaltokens_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/personaltokens"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type fixture struct {
	d     *db.DB
	q     *gen.Queries // the raw pool: fixtures only
	store *personaltokens.SQLStore
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := testdb.Open(t)
	return fixture{d: d, q: gen.New(d.Pool()), store: personaltokens.NewSQLStore(d)}
}

func (f fixture) user(t *testing.T, email string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := f.q.CreateUser(context.Background(), gen.CreateUserParams{ID: id, Email: email, DisplayName: email}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f fixture) org(t *testing.T, slug string, members map[uuid.UUID]tenancy.Role) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := f.q.CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: id, Slug: slug, Name: slug}); err != nil {
		t.Fatal(err)
	}
	for u, r := range members {
		if err := f.q.AddMembership(context.Background(), gen.AddMembershipParams{OrgID: id, UserID: u, Role: gen.OrgRole(r)}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// token inserts a personal token of userID through the store.
func (f fixture) token(t *testing.T, userID uuid.UUID) uuid.UUID {
	t.Helper()
	_, hash, err := auth.MintPersonalSecret()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := f.store.InsertToken(context.Background(), auth.PersonalToken{
		ID: uuid.Must(uuid.NewV7()), UserID: userID, Name: "laptop", Kind: auth.TokenKindAPI, CreatedAt: time.Now(),
	}, hash)
	if err != nil {
		t.Fatal(err)
	}
	return tok.ID
}

func (f fixture) grant(t *testing.T, tokenID, userID, orgID uuid.UUID, role tenancy.Role) {
	t.Helper()
	if err := f.store.InsertGrant(context.Background(), tokenID, userID, orgID, role); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) grantCount(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	if err := f.d.QueryRow(ctx, `SELECT count(*) FROM personal_token_grants`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestInsertTokenStoresMicroseconds: the token InsertToken returns is the
// token as stored, so a clock with nanoseconds never makes the two differ.
func TestInsertTokenStoresMicroseconds(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "micro@example.test")
	created := time.Date(2026, 11, 17, 10, 0, 0, 123456789, time.UTC)
	expires := created.Add(48*time.Hour + 987654321)
	_, hash, err := auth.MintPersonalSecret()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := f.store.InsertToken(context.Background(), auth.PersonalToken{
		ID: uuid.Must(uuid.NewV7()), UserID: u, Name: "ci", Kind: auth.TokenKindAssistant, Areas: []string{"jobs.read"},
		CreatedAt: created, ExpiresAt: &expires,
	}, hash)
	if err != nil {
		t.Fatal(err)
	}
	if tok.CreatedAt.Nanosecond()%1000 != 0 || tok.ExpiresAt == nil || tok.ExpiresAt.Nanosecond()%1000 != 0 {
		t.Fatalf("returned timestamps not truncated: %v %v", tok.CreatedAt, tok.ExpiresAt)
	}
	stored, err := auth.NewSQLTokenStore(f.d).GetPersonalByHash(context.Background(), hash, created)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.CreatedAt.Equal(tok.CreatedAt) || !stored.ExpiresAt.Equal(*tok.ExpiresAt) {
		t.Fatalf("stored %v / %v, returned %v / %v", stored.CreatedAt, stored.ExpiresAt, tok.CreatedAt, tok.ExpiresAt)
	}
}

// TestGrantRowsInvisibleToOtherOrg (controller ruling P7): bound to one
// organization, the store never sees another organization's grants.
func TestGrantRowsInvisibleToOtherOrg(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "rls@example.test")
	a := f.org(t, "rls-a", map[uuid.UUID]tenancy.Role{u: tenancy.RoleAdmin})
	b := f.org(t, "rls-b", map[uuid.UUID]tenancy.Role{u: tenancy.RoleOperator})
	tok := f.token(t, u)
	f.grant(t, tok, u, a, tenancy.RoleAdmin)
	f.grant(t, tok, u, b, tenancy.RoleViewer)

	inB := db.WithOrgScope(context.Background(), b)
	if _, _, err := f.store.Cap(inB, tok, a); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("Cap(A) bound to B: err = %v, want ErrNotFound", err)
	}
	if role, blocked, err := f.store.Cap(inB, tok, b); err != nil || role != tenancy.RoleViewer || blocked {
		t.Fatalf("Cap(B) bound to B = %q %v %v", role, blocked, err)
	}
	if n := f.grantCount(t, inB); n != 1 {
		t.Fatalf("bound to B: %d grant rows visible, want 1", n)
	}
	// A touch or a block through B's scope cannot reach A's grant.
	if _, err := f.store.BlockGrant(inB, tok, a, u); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("BlockGrant(A) bound to B: err = %v, want ErrNotFound", err)
	}
	// Writing a grant for A through B's scope is refused by the policy.
	other := f.token(t, u)
	if err := f.store.InsertGrant(inB, other, u, a, tenancy.RoleViewer); err == nil {
		t.Fatal("InsertGrant for A bound to B succeeded")
	}
	// Instance level (the owner's own view) sees both.
	if n := f.grantCount(t, context.Background()); n != 2 {
		t.Fatalf("instance level: %d grant rows, want 2", n)
	}
}

func TestGrantRequiresSameUserAsToken(t *testing.T) {
	f := newFixture(t)
	owner := f.user(t, "owner@example.test")
	other := f.user(t, "other@example.test")
	outsider := f.user(t, "outsider@example.test")
	org := f.org(t, "same-user", map[uuid.UUID]tenancy.Role{owner: tenancy.RoleOwner, other: tenancy.RoleAdmin})
	tok := f.token(t, owner)

	// Another member's grant on the owner's token.
	if err := f.store.InsertGrant(context.Background(), tok, other, org, tenancy.RoleViewer); err == nil {
		t.Fatal("grant with another user than the token's owner was stored")
	}
	// A grant for an organization the owner is not a member of.
	if err := f.store.InsertGrant(context.Background(), f.token(t, outsider), outsider, org, tenancy.RoleViewer); err == nil {
		t.Fatal("grant without a membership was stored")
	}
	if n := f.grantCount(t, context.Background()); n != 0 {
		t.Fatalf("%d grant rows, want 0", n)
	}
}

// TestRemovingMemberDeletesTheirGrants (spec rule 11): removing a member
// deletes their grants for that organization, the blocked one too, through
// the tenancy service as the application role; their grants elsewhere stay.
func TestRemovingMemberDeletesTheirGrants(t *testing.T) {
	f := newFixture(t)
	admin := f.user(t, "admin@example.test")
	member := f.user(t, "member@example.test")
	a := f.org(t, "remove-a", map[uuid.UUID]tenancy.Role{admin: tenancy.RoleOwner, member: tenancy.RoleOperator})
	b := f.org(t, "remove-b", map[uuid.UUID]tenancy.Role{member: tenancy.RoleOwner})
	live, blocked := f.token(t, member), f.token(t, member)
	f.grant(t, live, member, a, tenancy.RoleOperator)
	f.grant(t, live, member, b, tenancy.RoleOwner)
	f.grant(t, blocked, member, a, tenancy.RoleViewer)
	inA := db.WithOrgScope(context.Background(), a)
	if _, err := f.store.BlockGrant(inA, blocked, a, admin); err != nil {
		t.Fatal(err)
	}

	svc := tenancy.NewService(tenancy.NewSQLStore(f.d), nil, "", time.Now)
	if err := svc.RemoveMember(inA, a, tenancy.RoleOwner, admin, member); err != nil {
		t.Fatal(err)
	}
	if n := f.grantCount(t, inA); n != 0 {
		t.Fatalf("org A: %d grant rows left, want 0", n)
	}
	if _, _, err := f.store.Cap(context.Background(), blocked, a); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("blocked grant: err = %v, want ErrNotFound", err)
	}
	if role, _, err := f.store.Cap(context.Background(), live, b); err != nil || role != tenancy.RoleOwner {
		t.Fatalf("org B grant = %q, %v; want it kept", role, err)
	}
	// Rejoining does not revive a grant.
	if err := f.q.AddMembership(context.Background(), gen.AddMembershipParams{OrgID: a, UserID: member, Role: gen.OrgRoleOperator}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.Cap(context.Background(), live, a); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("after rejoining: err = %v, want ErrNotFound", err)
	}
}

func TestSQLStoreCapAllowedBlockTouch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "cap@example.test")
	org := f.org(t, "cap-org", map[uuid.UUID]tenancy.Role{u: tenancy.RoleAdmin})
	tok := f.token(t, u)
	f.grant(t, tok, u, org, tenancy.RoleOperator)
	scoped := db.WithOrgScope(ctx, org)

	if role, blocked, err := f.store.Cap(scoped, tok, org); err != nil || role != tenancy.RoleOperator || blocked {
		t.Fatalf("Cap = %q %v %v", role, blocked, err)
	}
	if _, _, err := f.store.Cap(scoped, uuid.Must(uuid.NewV7()), org); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("unknown token: err = %v", err)
	}

	// The switch: no row means allowed; off and on again.
	if on, err := f.store.Allowed(scoped, org); err != nil || !on {
		t.Fatalf("Allowed without a row = %v, %v", on, err)
	}
	for _, want := range []bool{false, true, false} {
		if err := f.store.SetAllowed(scoped, org, want); err != nil {
			t.Fatal(err)
		}
		if on, err := f.store.Allowed(scoped, org); err != nil || on != want {
			t.Fatalf("Allowed = %v, %v; want %v", on, err, want)
		}
	}

	// The touch writes the database's now() once a minute at most.
	lastUsed := func() *time.Time {
		t.Helper()
		var at *time.Time
		if err := f.d.Pool().QueryRow(ctx, `SELECT last_used_at FROM personal_token_grants WHERE token_id = $1`, tok).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	if err := f.store.TouchGrant(scoped, tok, org); err != nil {
		t.Fatal(err)
	}
	first := lastUsed()
	if first == nil {
		t.Fatal("last_used_at not set")
	}
	if err := f.store.TouchGrant(scoped, tok, org); err != nil {
		t.Fatal(err)
	}
	if again := lastUsed(); !again.Equal(*first) {
		t.Fatalf("touch inside the minute moved last_used_at %v → %v", first, again)
	}

	// Blocking sticks and reads back as blocked; a second block is not found.
	if _, err := f.store.BlockGrant(scoped, tok, org, u); err != nil {
		t.Fatal(err)
	}
	if _, blocked, err := f.store.Cap(scoped, tok, org); err != nil || !blocked {
		t.Fatalf("after block: blocked = %v, %v", blocked, err)
	}
	if _, err := f.store.BlockGrant(scoped, tok, org, u); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("second block: err = %v, want ErrNotFound", err)
	}
}
