package accounts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func seedUser(t *testing.T, d *db.DB, email string, hash *string) uuid.UUID {
	t.Helper()
	uid := uuid.Must(uuid.NewV7())
	if _, err := gen.New(d.Pool()).CreateUser(context.Background(), gen.CreateUserParams{
		ID: uid, Email: email, DisplayName: "U", PasswordHash: hash,
	}); err != nil {
		t.Fatal(err)
	}
	return uid
}

func sessionRevoked(t *testing.T, d *db.DB, id uuid.UUID) bool {
	t.Helper()
	var revoked bool
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM sessions WHERE id = $1`, id).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	return revoked
}

func TestSQLStorePasswordRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	hash := "$argon2id$old"
	uid := seedUser(t, d, "store@example.test", &hash)
	expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := d.Pool().Exec(ctx,
		`UPDATE users SET must_change_password = true, temp_password_expires_at = $2 WHERE id = $1`, uid, expires); err != nil {
		t.Fatal(err)
	}

	var st accounts.PasswordState
	if err := store.InTx(ctx, func(ctx context.Context) error {
		var err error
		st, err = store.LockPassword(ctx, uid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if st.Hash == nil || *st.Hash != hash || st.TempPasswordExpiresAt == nil || !st.TempPasswordExpiresAt.Equal(expires) {
		t.Fatalf("LockPassword = %+v, want hash %q and expiry %v", st, hash, expires)
	}

	if err := store.SetPassword(ctx, uid, "$argon2id$new", true); err != nil {
		t.Fatal(err)
	}
	var (
		gotHash    string
		mustChange bool
		tempUntil  *time.Time
	)
	if err := d.Pool().QueryRow(ctx,
		`SELECT password_hash, must_change_password, temp_password_expires_at FROM users WHERE id = $1`, uid).
		Scan(&gotHash, &mustChange, &tempUntil); err != nil {
		t.Fatal(err)
	}
	if gotHash != "$argon2id$new" || mustChange || tempUntil != nil {
		t.Fatalf("after SetPassword(clear): hash %q, must_change %v, temp_until %v", gotHash, mustChange, tempUntil)
	}

	t.Run("without clearing, the flags stay", func(t *testing.T) {
		if _, err := d.Pool().Exec(ctx,
			`UPDATE users SET must_change_password = true, temp_password_expires_at = $2 WHERE id = $1`, uid, expires); err != nil {
			t.Fatal(err)
		}
		if err := store.SetPassword(ctx, uid, "$argon2id$newer", false); err != nil {
			t.Fatal(err)
		}
		if err := d.Pool().QueryRow(ctx,
			`SELECT must_change_password, temp_password_expires_at FROM users WHERE id = $1`, uid).
			Scan(&mustChange, &tempUntil); err != nil {
			t.Fatal(err)
		}
		if !mustChange || tempUntil == nil {
			t.Fatalf("SetPassword(no clear) touched the flags: must_change %v, temp_until %v", mustChange, tempUntil)
		}
	})

	t.Run("unknown and deactivated accounts are not found", func(t *testing.T) {
		if err := store.SetPassword(ctx, uuid.Must(uuid.NewV7()), "$argon2id$x", true); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("SetPassword(unknown) err = %v, want ErrNotFound", err)
		}
		if _, err := store.LockPassword(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("LockPassword(unknown) err = %v, want ErrNotFound", err)
		}
		disabled := seedUser(t, d, "disabled@example.test", &hash)
		if _, err := d.Pool().Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, disabled); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LockPassword(ctx, disabled); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("LockPassword(deactivated) err = %v, want ErrNotFound", err)
		}
	})
}

func TestSQLStoreRevokeSessionsExcept(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	sessions := auth.NewSessions(gen.New(d.Pool()), time.Now)
	hash := "$argon2id$h"
	uid := seedUser(t, d, "revoke@example.test", &hash)
	other := seedUser(t, d, "bystander@example.test", &hash)

	keep, _, err := sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	gone, _, err := sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	bystander, _, err := sessions.Create(ctx, other, true)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.RevokeSessionsExcept(ctx, uid, keep); err != nil {
		t.Fatal(err)
	}
	if sessionRevoked(t, d, keep) {
		t.Fatal("the kept session was revoked")
	}
	if !sessionRevoked(t, d, gone) {
		t.Fatal("another session of the user survived")
	}
	if sessionRevoked(t, d, bystander) {
		t.Fatal("a session of another user was revoked")
	}
}

// TestSQLStoreInTxRollsBack: a failure after the writes inside InTx leaves
// neither the password nor the revocation behind.
func TestSQLStoreInTxRollsBack(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	sessions := auth.NewSessions(gen.New(d.Pool()), time.Now)
	hash := "$argon2id$before"
	uid := seedUser(t, d, "rollback@example.test", &hash)
	sid, _, err := sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	err = store.InTx(ctx, func(ctx context.Context) error {
		if err := store.SetPassword(ctx, uid, "$argon2id$after", true); err != nil {
			return err
		}
		if err := store.RevokeSessionsExcept(ctx, uid, uuid.Nil); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx err = %v, want %v", err, boom)
	}
	var got string
	if err := d.Pool().QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, uid).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != hash {
		t.Fatalf("password_hash = %q after a rolled-back transaction, want %q", got, hash)
	}
	if sessionRevoked(t, d, sid) {
		t.Fatal("the session revocation survived a rolled-back transaction")
	}
}
