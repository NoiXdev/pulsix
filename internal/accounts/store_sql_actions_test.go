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

func countRows(t *testing.T, d *db.DB, table string, uid uuid.UUID) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM `+table+` WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestResetSecondFactorSQL: the reset removes the TOTP secret, the
// recovery codes and every passkey of the account and ends its sessions,
// in one transaction, and leaves every other account alone.
func TestResetSecondFactorSQL(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	sessions := auth.NewSessions(gen.New(d.Pool()), time.Now)
	hash := "$argon2id$h"
	uid := seedUser(t, d, "reset-factor@example.test", &hash)
	other := seedUser(t, d, "reset-bystander@example.test", &hash)
	seedFactors(t, d, uid)
	seedFactors(t, d, other)
	sid, _, err := sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	otherSid, _, err := sessions.Create(ctx, other, true)
	if err != nil {
		t.Fatal(err)
	}

	svc := accounts.NewService(store, &fakeMailer{unconfigured: true}, "https://pulsix.example", time.Now, &cyclingReader{})
	if _, err := svc.ResetSecondFactor(ctx, true, uuid.Must(uuid.NewV7()), uid); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"totp_secrets", "recovery_codes", "passkeys"} {
		if n := countRows(t, d, table, uid); n != 0 {
			t.Errorf("%s: %d rows left for the user, want 0", table, n)
		}
	}
	if !sessionRevoked(t, d, sid) {
		t.Error("the user's session survived the reset")
	}
	if countRows(t, d, "totp_secrets", other) != 1 || countRows(t, d, "recovery_codes", other) != 3 || countRows(t, d, "passkeys", other) != 1 {
		t.Error("the reset touched another user's factors")
	}
	if sessionRevoked(t, d, otherSid) {
		t.Error("the reset ended another user's session")
	}
}

func TestSQLStoreAccountActions(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	sessions := auth.NewSessions(gen.New(d.Pool()), time.Now)
	hash := "$argon2id$h"
	uid := seedUser(t, d, "actions@example.test", &hash)
	pending := seedUser(t, d, "actions-pending@example.test", nil)
	mustChange := seedUser(t, d, "actions-must-change@example.test", &hash)
	if _, err := d.Pool().Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE users SET must_change_password = true WHERE id = $1`, mustChange); err != nil {
		t.Fatal(err)
	}

	t.Run("LockAccount", func(t *testing.T) {
		err := store.InTx(ctx, func(ctx context.Context) error {
			got, err := store.LockAccount(ctx, uid)
			if want := (accounts.LockedAccount{HasPassword: true, Deactivated: true}); err != nil || got != want {
				t.Errorf("LockAccount(deactivated with a password) = %+v, %v; want %+v", got, err, want)
			}
			got, err = store.LockAccount(ctx, mustChange)
			if want := (accounts.LockedAccount{HasPassword: true, MustChangePassword: true}); err != nil || got != want {
				t.Errorf("LockAccount(must change password) = %+v, %v; want %+v", got, err, want)
			}
			got, err = store.LockAccount(ctx, pending)
			if want := (accounts.LockedAccount{}); err != nil || got != want {
				t.Errorf("LockAccount(invite pending) = %+v, %v; want %+v", got, err, want)
			}
			if _, err := store.LockAccount(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, accounts.ErrNotFound) {
				t.Errorf("LockAccount(unknown) err = %v, want ErrNotFound", err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("SetTemporaryPassword", func(t *testing.T) {
		expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
		if err := store.SetTemporaryPassword(ctx, uid, "$argon2id$temp", expires); err != nil {
			t.Fatal(err)
		}
		var (
			got        string
			mustChange bool
			until      *time.Time
		)
		if err := d.Pool().QueryRow(ctx,
			`SELECT password_hash, must_change_password, temp_password_expires_at FROM users WHERE id = $1`, uid).
			Scan(&got, &mustChange, &until); err != nil {
			t.Fatal(err)
		}
		if got != "$argon2id$temp" || !mustChange || until == nil || !until.Equal(expires) {
			t.Fatalf("after SetTemporaryPassword: hash %q, must change %v, until %v", got, mustChange, until)
		}
		if err := store.SetTemporaryPassword(ctx, uuid.Must(uuid.NewV7()), "$argon2id$x", expires); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("SetTemporaryPassword(unknown) err = %v, want ErrNotFound", err)
		}
	})

	t.Run("RevokeAllSessions", func(t *testing.T) {
		a, _, err := sessions.Create(ctx, pending, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.RevokeAllSessions(ctx, pending); err != nil {
			t.Fatal(err)
		}
		if !sessionRevoked(t, d, a) {
			t.Fatal("RevokeAllSessions left a session")
		}
	})

	t.Run("Reactivate", func(t *testing.T) {
		if err := store.Reactivate(ctx, uid); err != nil {
			t.Fatal(err)
		}
		var disabled bool
		if err := d.Pool().QueryRow(ctx, `SELECT disabled_at IS NOT NULL FROM users WHERE id = $1`, uid).Scan(&disabled); err != nil {
			t.Fatal(err)
		}
		if disabled {
			t.Fatal("Reactivate left disabled_at set")
		}
		if err := store.Reactivate(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("Reactivate(unknown) err = %v, want ErrNotFound", err)
		}
	})

	t.Run("NoticeRecipient", func(t *testing.T) {
		r, err := store.NoticeRecipient(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if r.Email != "actions@example.test" || r.Locale != nil || r.DefaultLocale != "" {
			t.Fatalf("NoticeRecipient = %+v, want the address and no languages", r)
		}
		if _, err := d.Pool().Exec(ctx, `UPDATE users SET locale = 'de' WHERE id = $1`, uid); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool().Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES ('default_locale', '"en"')`); err != nil {
			t.Fatal(err)
		}
		r, err = store.NoticeRecipient(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if r.Locale == nil || *r.Locale != "de" || r.DefaultLocale != "en" {
			t.Fatalf("NoticeRecipient = %+v, want locale de and default en", r)
		}
		if _, err := store.NoticeRecipient(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("NoticeRecipient(unknown) err = %v, want ErrNotFound", err)
		}
	})
}

// TestSQLStoreResetTokens: the reset token queries against the real
// schema — finding an account by address, the per-address count, voiding,
// and the lookup a reset makes.
func TestSQLStoreResetTokens(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	hash := "$argon2id$h"
	uid := seedUser(t, d, "Reset.Person@example.test", &hash)
	other := seedUser(t, d, "reset-other@example.test", &hash)
	now := time.Now().UTC().Truncate(time.Microsecond)

	if got, err := store.AccountByEmail(ctx, "reset.person@EXAMPLE.test"); err != nil || got != uid {
		t.Fatalf("AccountByEmail(other case) = %v, %v; want %v", got, err, uid)
	}
	if _, err := store.AccountByEmail(ctx, "nobody@example.test"); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("AccountByEmail(unknown) err = %v, want ErrNotFound", err)
	}

	insert := func(user uuid.UUID, h string, created, expires time.Time) {
		t.Helper()
		if err := store.InsertResetToken(ctx, accounts.ResetToken{
			ID: uuid.Must(uuid.NewV7()), UserID: user, Hash: []byte(h), CreatedAt: created, ExpiresAt: expires,
		}); err != nil {
			t.Fatal(err)
		}
	}
	insert(uid, "old", now.Add(-2*time.Hour), now.Add(-time.Hour))
	insert(uid, "recent", now.Add(-30*time.Minute), now.Add(30*time.Minute))
	insert(uid, "expired", now.Add(-61*time.Minute), now.Add(-time.Minute))
	insert(other, "bystander", now, now.Add(time.Hour))

	if n, err := store.CountResetTokensSince(ctx, uid, now.Add(-time.Hour)); err != nil || n != 1 {
		t.Fatalf("CountResetTokensSince(1 h) = %d, %v; want 1", n, err)
	}

	lock := func(h string, at time.Time) (uuid.UUID, error) {
		owner, err := store.ResetTokenOwner(ctx, []byte(h))
		if err != nil {
			return uuid.Nil, err
		}
		err = store.InTx(ctx, func(ctx context.Context) error {
			return store.LockResetToken(ctx, []byte(h), owner, at)
		})
		return owner, err
	}
	if got, err := lock("recent", now); err != nil || got != uid {
		t.Fatalf("LockResetToken(recent) = %v, %v; want %v", got, err, uid)
	}
	if _, err := lock("expired", now); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("LockResetToken(expired) err = %v, want ErrNotFound", err)
	}
	if _, err := lock("recent", now.Add(30*time.Minute)); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("LockResetToken(at its expiry) err = %v, want ErrNotFound", err)
	}
	if _, err := lock("unknown", now); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("LockResetToken(unknown) err = %v, want ErrNotFound", err)
	}

	if err := store.InTx(ctx, func(ctx context.Context) error {
		return store.LockResetToken(ctx, []byte("recent"), other, now)
	}); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("LockResetToken(another account's token) err = %v, want ErrNotFound", err)
	}

	if err := store.VoidResetTokens(ctx, uid, now); err != nil {
		t.Fatal(err)
	}
	// The owner is still found unlocked, used or not; the locked re-check
	// is what refuses a token used in between.
	if got, err := store.ResetTokenOwner(ctx, []byte("recent")); err != nil || got != uid {
		t.Fatalf("ResetTokenOwner(used) = %v, %v; want %v", got, err, uid)
	}
	if _, err := lock("recent", now); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("LockResetToken(voided) err = %v, want ErrNotFound", err)
	}
	if got, err := lock("bystander", now); err != nil || got != other {
		t.Fatalf("voiding one account's tokens touched another's: %v, %v", got, err)
	}
	if n, err := store.CountResetTokensSince(ctx, uid, now.Add(-time.Hour)); err != nil || n != 1 {
		t.Fatalf("CountResetTokensSince after voiding = %d, %v; want used tokens still counted (1)", n, err)
	}
}
