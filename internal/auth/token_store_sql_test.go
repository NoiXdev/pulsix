package auth_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func hashOf(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func TestSQLTokenStoreInsertAndGetByHash(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "token-store@example.test")
	org := seedOrg(t, q, "token-store-org")
	store := auth.NewSQLTokenStore(q)

	tok := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "ci", Role: "operator"}
	hash := hashOf("secret-a")
	if err := store.Insert(ctx, tok, hash); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, admin, err := store.GetByHash(ctx, hash, time.Now())
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.ID != tok.ID || got.UserID != uid || got.OrgID != org || got.Name != "ci" || got.Role != "operator" || got.ExpiresAt != nil {
		t.Fatalf("GetByHash row = %+v", got)
	}
	if admin {
		t.Fatal("instance_admin = true for a non-admin user")
	}
	if got.LastUsedAt != nil {
		t.Fatal("LastUsedAt set before any TouchLastUsed call")
	}

	if _, _, err := store.GetByHash(ctx, hashOf("never-inserted"), time.Now()); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("GetByHash(unknown) err = %v, want ErrNotFound", err)
	}
}

// TestSQLTokenStoreGetByHashJoinsInstanceAdmin asserts the query's join
// against users.instance_admin (mirroring GetSession's own join) actually
// reflects the owning user's flag, not a hardcoded false.
func TestSQLTokenStoreGetByHashJoinsInstanceAdmin(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "admin-token@example.test", DisplayName: "Admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, "UPDATE users SET instance_admin = true WHERE id = $1", uid); err != nil {
		t.Fatal(err)
	}
	org := seedOrg(t, q, "admin-token-org")
	store := auth.NewSQLTokenStore(q)
	tok := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "ci", Role: "owner"}
	hash := hashOf("admin-secret")
	if err := store.Insert(ctx, tok, hash); err != nil {
		t.Fatal(err)
	}
	_, admin, err := store.GetByHash(ctx, hash, time.Now())
	if err != nil || !admin {
		t.Fatalf("GetByHash instanceAdmin = %v, %v, want true, nil", admin, err)
	}
}

// TestSQLTokenStoreGetByHashExcludesRevoked asserts the query's WHERE
// revoked_at IS NULL clause behaves exactly like RevokeAPIToken says it
// should, end to end through the real store.
func TestSQLTokenStoreGetByHashExcludesRevoked(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "revoked-token@example.test")
	org := seedOrg(t, q, "revoked-token-org")
	store := auth.NewSQLTokenStore(q)
	tok := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "ci", Role: "viewer"}
	hash := hashOf("revoke-me")
	if err := store.Insert(ctx, tok, hash); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, org, tok.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, _, err := store.GetByHash(ctx, hash, time.Now()); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("GetByHash(revoked) err = %v, want ErrNotFound", err)
	}
	// A second Revoke of the same, already-revoked row must report
	// ErrNotFound rather than silently succeeding again — the row it
	// matched the first time no longer satisfies revoked_at IS NULL.
	if err := store.Revoke(ctx, org, tok.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("second Revoke err = %v, want ErrNotFound", err)
	}
}

// TestSQLTokenStoreGetByHashExcludesExpired mirrors the revoked case for
// the query's expires_at filter.
func TestSQLTokenStoreGetByHashExcludesExpired(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "expired-token@example.test")
	org := seedOrg(t, q, "expired-token-org")
	store := auth.NewSQLTokenStore(q)
	past := time.Now().Add(-time.Hour)
	tok := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "ci", Role: "viewer", ExpiresAt: &past}
	hash := hashOf("expired")
	if err := store.Insert(ctx, tok, hash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetByHash(ctx, hash, time.Now()); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("GetByHash(expired) err = %v, want ErrNotFound", err)
	}
}

// TestSQLTokenStoreRevokeIsScopedToOrg asserts Revoke cannot be used to
// revoke a token through an org it does not belong to — the same isolation
// RLS enforces at the row-visibility level, asserted here at the
// application query's own WHERE clause.
func TestSQLTokenStoreRevokeIsScopedToOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "scoped-revoke@example.test")
	org := seedOrg(t, q, "scoped-revoke-org")
	otherOrg := seedOrg(t, q, "scoped-revoke-other-org")
	store := auth.NewSQLTokenStore(q)
	tok := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "ci", Role: "viewer"}
	hash := hashOf("scoped")
	if err := store.Insert(ctx, tok, hash); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, otherOrg, tok.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("Revoke(wrong org) err = %v, want ErrNotFound", err)
	}
	if _, _, err := store.GetByHash(ctx, hash, time.Now()); err != nil {
		t.Fatalf("token was revoked through the wrong org: %v", err)
	}
}

func TestSQLTokenStoreListForOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "list-token@example.test")
	org := seedOrg(t, q, "list-token-org")
	otherOrg := seedOrg(t, q, "list-token-other-org")
	store := auth.NewSQLTokenStore(q)

	inOrg := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "in-org", Role: "viewer"}
	if err := store.Insert(ctx, inOrg, hashOf("in-org")); err != nil {
		t.Fatal(err)
	}
	revoked := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "revoked", Role: "viewer"}
	if err := store.Insert(ctx, revoked, hashOf("revoked-list")); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, org, revoked.ID); err != nil {
		t.Fatal(err)
	}
	elsewhere := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: otherOrg, Name: "elsewhere", Role: "viewer"}
	if err := store.Insert(ctx, elsewhere, hashOf("elsewhere")); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListForOrg(ctx, org)
	if err != nil {
		t.Fatalf("ListForOrg: %v", err)
	}
	if len(list) != 1 || list[0].ID != inOrg.ID {
		t.Fatalf("ListForOrg(org) = %+v, want exactly [%v]", list, inOrg.ID)
	}
}

func TestSQLTokenStoreTouchLastUsed(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "touch-token@example.test")
	org := seedOrg(t, q, "touch-token-org")
	store := auth.NewSQLTokenStore(q)
	tok := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "ci", Role: "viewer"}
	hash := hashOf("touch")
	if err := store.Insert(ctx, tok, hash); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Microsecond)
	if err := store.TouchLastUsed(ctx, tok.ID, at); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	got, _, err := store.GetByHash(ctx, hash, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(at) {
		t.Fatalf("LastUsedAt = %v, want %v", got.LastUsedAt, at)
	}
}
