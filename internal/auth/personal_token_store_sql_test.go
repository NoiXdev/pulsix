package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// insertPersonal stores a personal token through the generated query on the
// raw pool and returns its id.
func insertPersonal(t *testing.T, q *gen.Queries, userID uuid.UUID, hash []byte, expiresAt *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exp := pgtype.Timestamptz{}
	if expiresAt != nil {
		exp = pgtype.Timestamptz{Time: *expiresAt, Valid: true}
	}
	if err := q.InsertPersonalToken(context.Background(), gen.InsertPersonalTokenParams{
		ID: id, UserID: userID, Name: "laptop", SecretHash: hash, ExpiresAt: exp,
		CreatedAt: time.Now().Truncate(time.Microsecond), Kind: "assistant", Areas: []string{"jobs.read"},
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSQLTokenStorePersonalByHash(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := auth.NewSQLTokenStore(d)
	uid := seedUser(t, q, "personal-store@example.test")
	// The owner is an instance administrator; the lookup never reads it.
	if _, err := d.Pool().Exec(ctx, `UPDATE users SET instance_admin = true WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	exp := now.Add(time.Hour)
	hash := hashOf("personal-secret")
	id := insertPersonal(t, q, uid, hash, &exp)

	got, err := store.GetPersonalByHash(ctx, hash, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.UserID != uid || got.Name != "laptop" || got.Kind != auth.TokenKindAssistant ||
		len(got.Areas) != 1 || got.ExpiresAt == nil || got.LastUsedAt != nil {
		t.Fatalf("GetPersonalByHash = %+v", got)
	}
	if usable, err := store.PersonalUsable(ctx, id, now); err != nil || !usable {
		t.Fatalf("PersonalUsable = %v, %v", usable, err)
	}
	// An organization token's lookup never finds a personal token.
	if _, _, err := store.GetByHash(ctx, hash, now); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("GetByHash(personal hash) err = %v, want ErrNotFound", err)
	}

	// Expired as of the injected clock.
	if _, err := store.GetPersonalByHash(ctx, hash, exp.Add(time.Second)); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("expired: err = %v, want ErrNotFound", err)
	}
	if usable, err := store.PersonalUsable(ctx, id, exp.Add(time.Second)); err != nil || usable {
		t.Fatalf("expired: PersonalUsable = %v, %v", usable, err)
	}
	// A deactivated owner.
	if _, err := d.Pool().Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPersonalByHash(ctx, hash, now); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("disabled owner: err = %v, want ErrNotFound", err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE users SET disabled_at = NULL WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	// Revoked.
	if _, err := d.Pool().Exec(ctx, `UPDATE personal_tokens SET revoked_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPersonalByHash(ctx, hash, now); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("revoked: err = %v, want ErrNotFound", err)
	}
	if usable, err := store.PersonalUsable(ctx, id, now); err != nil || usable {
		t.Fatalf("revoked: PersonalUsable = %v, %v", usable, err)
	}
	if _, err := store.GetPersonalByHash(ctx, hashOf("never-inserted"), now); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("unknown: err = %v, want ErrNotFound", err)
	}
}

// TestSQLTokenStoreTouchPersonalThrottled: the first touch writes the
// database's own now(); a second one inside the minute is a no-op; an aged
// stamp is written again.
func TestSQLTokenStoreTouchPersonalThrottled(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := auth.NewSQLTokenStore(d)
	uid := seedUser(t, q, "personal-touch@example.test")
	id := insertPersonal(t, q, uid, hashOf("touch-secret"), nil)

	lastUsed := func() time.Time {
		t.Helper()
		var at pgtype.Timestamptz
		if err := d.Pool().QueryRow(ctx, `SELECT last_used_at FROM personal_tokens WHERE id = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		if !at.Valid {
			t.Fatal("last_used_at not set")
		}
		return at.Time
	}
	if err := store.TouchPersonalLastUsed(ctx, id); err != nil {
		t.Fatal(err)
	}
	first := lastUsed()
	if err := store.TouchPersonalLastUsed(ctx, id); err != nil {
		t.Fatal(err)
	}
	if again := lastUsed(); !again.Equal(first) {
		t.Fatalf("touch inside the minute moved last_used_at from %v to %v", first, again)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE personal_tokens SET last_used_at = now() - interval '2 minutes' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	aged := lastUsed()
	if err := store.TouchPersonalLastUsed(ctx, id); err != nil {
		t.Fatal(err)
	}
	if fresh := lastUsed(); !fresh.After(aged) {
		t.Fatalf("aged touch: last_used_at %v not after %v", fresh, aged)
	}
}
