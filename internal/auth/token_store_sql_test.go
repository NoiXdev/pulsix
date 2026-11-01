package auth_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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
	store := auth.NewSQLTokenStore(d)

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
	store := auth.NewSQLTokenStore(d)
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
	store := auth.NewSQLTokenStore(d)
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
	store := auth.NewSQLTokenStore(d)
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
// TestSQLTokenStoreGetByHashExcludesDisabledUser is the query-level
// backstop half of Finding 2's fix for bearer tokens (Task 10 fix round
// 1): GetAPITokenByHash's own WHERE clause now excludes a token whose
// owning user is disabled (tokens.sql), so a token stops resolving the
// instant its owner is disabled, not only through DeactivateUser's
// explicit RevokeTokensForUser call.
func TestSQLTokenStoreGetByHashExcludesDisabledUser(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "disabled-token-owner@example.test")
	org := seedOrg(t, q, "disabled-token-org")
	store := auth.NewSQLTokenStore(d)

	tok := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "ci", Role: "operator"}
	hash := hashOf("disabled-owner-secret")
	if err := store.Insert(ctx, tok, hash); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, _, err := store.GetByHash(ctx, hash, time.Now()); err != nil {
		t.Fatalf("GetByHash before disabling: %v", err)
	}

	if _, err := q.DisableUser(ctx, gen.DisableUserParams{ID: uid, DisabledAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetByHash(ctx, hash, time.Now()); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("GetByHash(disabled owner) err = %v, want ErrNotFound", err)
	}
}

func TestSQLTokenStoreRevokeIsScopedToOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "scoped-revoke@example.test")
	org := seedOrg(t, q, "scoped-revoke-org")
	otherOrg := seedOrg(t, q, "scoped-revoke-other-org")
	store := auth.NewSQLTokenStore(d)
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
	store := auth.NewSQLTokenStore(d)

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
	store := auth.NewSQLTokenStore(d)
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

// TestSQLTokenStoreTouchLastUsedThrottlesToOncePerMinute pins backlog
// 1.22's write-amplification fix: TouchLastUsed must write at most once per
// minute per token, and the SQL predicate inside the UPDATE — not any
// in-process state — is the source of truth, so a repeated touch inside the
// window is a plain no-op that leaves the row untouched. A touch after the
// window has passed applies again: the test drives the clock side by
// backdating the row through the raw pool, because the predicate reads the
// database's own now(), which no test can move.
func TestSQLTokenStoreTouchLastUsedThrottlesToOncePerMinute(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "throttled-touch@example.test")
	org := seedOrg(t, q, "throttled-touch-org")
	store := auth.NewSQLTokenStore(d)
	tok := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "ci", Role: "viewer"}
	hash := hashOf("throttled-touch")
	if err := store.Insert(ctx, tok, hash); err != nil {
		t.Fatal(err)
	}

	first := time.Now().Truncate(time.Microsecond)
	if err := store.TouchLastUsed(ctx, tok.ID, first); err != nil {
		t.Fatalf("first TouchLastUsed: %v", err)
	}

	// A second touch well inside the window must leave the row untouched:
	// exactly one write lands no matter how many requests resolve the
	// token within the minute.
	if err := store.TouchLastUsed(ctx, tok.ID, first.Add(30*time.Second)); err != nil {
		t.Fatalf("second TouchLastUsed: %v", err)
	}
	got, _, err := store.GetByHash(ctx, hash, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(first) {
		t.Fatalf("LastUsedAt after a touch inside the window = %v, want %v unchanged (the second touch must be a no-op)", got.LastUsedAt, first)
	}

	// Once the stored last_used_at is older than the window the predicate
	// admits the write again. now() - interval '2 minutes' backdates the
	// row past the window on the database's own clock.
	if _, err := d.Pool().Exec(ctx, `UPDATE api_tokens SET last_used_at = now() - interval '2 minutes' WHERE id = $1`, tok.ID); err != nil {
		t.Fatal(err)
	}
	third := time.Now().Truncate(time.Microsecond)
	if err := store.TouchLastUsed(ctx, tok.ID, third); err != nil {
		t.Fatalf("TouchLastUsed after the window: %v", err)
	}
	got, _, err = store.GetByHash(ctx, hash, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(third) {
		t.Fatalf("LastUsedAt after the window passed = %v, want %v (the touch must apply again once the minute has passed)", got.LastUsedAt, third)
	}

	// The boundary itself, from both sides: the throttle is "at most one
	// write per minute", so 59 seconds ago must still be throttled and 61
	// seconds ago must write. Everything above only proves the far ends.
	for _, tc := range []struct {
		name  string
		age   string
		wrote bool
	}{
		{"just inside the window", "59 seconds", false},
		{"just outside the window", "61 seconds", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.Pool().Exec(ctx, `UPDATE api_tokens SET last_used_at = now() - $2::interval WHERE id = $1`, tok.ID, tc.age); err != nil {
				t.Fatal(err)
			}
			before, _, err := store.GetByHash(ctx, hash, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().Truncate(time.Microsecond)
			if err := store.TouchLastUsed(ctx, tok.ID, at); err != nil {
				t.Fatalf("TouchLastUsed: %v", err)
			}
			after, _, err := store.GetByHash(ctx, hash, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if wrote := after.LastUsedAt.Equal(at); wrote != tc.wrote {
				t.Errorf("last_used_at %s old: wrote = %v, want %v (before %v, after %v)",
					tc.age, wrote, tc.wrote, before.LastUsedAt, after.LastUsedAt)
			}
		})
	}
}

// TestSQLTokenStoreGetByHashUsesInjectedNow proves GetByHash evaluates
// expiry against the now argument it is given, not the database's own
// now() (Finding 3, fix round 1): a token expiring one hour from the real
// wall clock is looked up once with a real "now" (still valid) and once
// with a caller-supplied "now" two hours in the future (must be rejected).
// If the query still used the database's now() instead of $2, the second
// lookup would wrongly succeed — the database's real clock has not moved,
// only the argument this test passes has — so this fails if that
// regresses, the same way TestTokenExpiry proves the property for the
// in-memory fake's injected clock.
func TestSQLTokenStoreGetByHashUsesInjectedNow(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "clock-token@example.test")
	org := seedOrg(t, q, "clock-token-org")
	store := auth.NewSQLTokenStore(d)
	exp := time.Now().Add(time.Hour)
	tok := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "ci", Role: "viewer", ExpiresAt: &exp}
	hash := hashOf("clock-driven")
	if err := store.Insert(ctx, tok, hash); err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.GetByHash(ctx, hash, time.Now()); err != nil {
		t.Fatalf("GetByHash with the real now: %v (token expires an hour from now, should still be usable)", err)
	}

	future := time.Now().Add(2 * time.Hour)
	if _, _, err := store.GetByHash(ctx, hash, future); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("GetByHash with an injected future now: err = %v, want ErrNotFound (the database's own now() has not moved, so this only fails correctly if the injected clock is actually used)", err)
	}
}

// TestAPITokensAreIsolatedPerOrgByRLS mirrors db_test.TestMembershipsAreIsolatedPerOrg:
// it exercises the org_isolation RLS policy directly, via d.WithOrg (which
// sets the pulsix.org_id setting org_isolation's USING clause reads),
// rather than through SQLTokenStore/gen.Queries the way every other test in
// this file does. testdb.Open connects as pulsix_app without ever setting
// pulsix.org_id, so those tests only ever exercise the org_isolation_bypass
// policy (current_setting('pulsix.org_id', true) IS NULL) — replacing
// org_isolation's USING clause with USING (true) would not fail a single
// one of them. This test would (Finding 2, fix round 1).
func TestAPITokensAreIsolatedPerOrgByRLS(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "rls-token@example.test")
	orgA := seedOrg(t, q, "rls-token-org-a")
	orgB := seedOrg(t, q, "rls-token-org-b")
	store := auth.NewSQLTokenStore(d)
	tokA := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: orgA, Name: "a", Role: "viewer"}
	if err := store.Insert(ctx, tokA, hashOf("rls-a")); err != nil {
		t.Fatal(err)
	}
	tokB := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: orgB, Name: "b", Role: "viewer"}
	if err := store.Insert(ctx, tokB, hashOf("rls-b")); err != nil {
		t.Fatal(err)
	}

	var seen []uuid.UUID
	err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id FROM api_tokens`)
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

// TestSQLTokenStoreUsableAppliesTheResolveFilter is the by-id check a
// long-running request repeats (backlog 7.22): usable while the token
// resolves, not once it is revoked, expired as of now, or unknown.
func TestSQLTokenStoreUsableAppliesTheResolveFilter(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := seedUser(t, q, "usable-token@example.test")
	org := seedOrg(t, q, "usable-token-org")
	store := auth.NewSQLTokenStore(d)
	expires := time.Now().Add(time.Hour)
	tok := auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: uid, OrgID: org, Name: "ci", Role: "viewer", ExpiresAt: &expires}
	if err := store.Insert(ctx, tok, hashOf("usable")); err != nil {
		t.Fatal(err)
	}
	usable := func(id uuid.UUID, now time.Time) bool {
		t.Helper()
		ok, err := store.Usable(ctx, id, now)
		if err != nil {
			t.Fatalf("Usable: %v", err)
		}
		return ok
	}
	if !usable(tok.ID, time.Now()) {
		t.Fatal("a live token is not usable")
	}
	if usable(tok.ID, expires.Add(time.Minute)) {
		t.Fatal("a token past its expiry is usable")
	}
	if usable(uuid.Must(uuid.NewV7()), time.Now()) {
		t.Fatal("an unknown token id is usable")
	}
	if err := store.Revoke(ctx, org, tok.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if usable(tok.ID, time.Now()) {
		t.Fatal("a revoked token is usable")
	}
}
