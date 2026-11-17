package accounts_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func seedLockPIN(t *testing.T, d *db.DB, uid uuid.UUID) {
	t.Helper()
	if _, err := d.Pool().Exec(context.Background(),
		`UPDATE users SET lock_pin_hash = 'hash', lock_pin_prompted_at = now() WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
}

func lockPINState(t *testing.T, d *db.DB, uid uuid.UUID) (hashSet, prompted bool) {
	t.Helper()
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT lock_pin_hash IS NOT NULL, lock_pin_prompted_at IS NOT NULL FROM users WHERE id = $1`, uid).Scan(&hashSet, &prompted); err != nil {
		t.Fatal(err)
	}
	return hashSet, prompted
}

// TestAdminFactorResetClearsLockPIN: resetting the second factor starts the
// account's security over, so the lock PIN and its "already offered" mark go
// with it; another account keeps its own.
func TestAdminFactorResetClearsLockPIN(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	hash := "$argon2id$h"
	uid := seedUser(t, d, "reset-pin@example.test", &hash)
	other := seedUser(t, d, "reset-pin-other@example.test", &hash)
	seedLockPIN(t, d, uid)
	seedLockPIN(t, d, other)

	svc := accounts.NewService(accounts.NewSQLStore(d), &fakeMailer{unconfigured: true}, "https://pulsix.example", time.Now, &cyclingReader{})
	if _, err := svc.ResetSecondFactor(ctx, true, uuid.Must(uuid.NewV7()), uid); err != nil {
		t.Fatal(err)
	}
	if set, prompted := lockPINState(t, d, uid); set || prompted {
		t.Errorf("after the reset: hash set %v, prompted %v, want both cleared", set, prompted)
	}
	if set, prompted := lockPINState(t, d, other); !set || !prompted {
		t.Errorf("another account: hash set %v, prompted %v, want both kept", set, prompted)
	}
}

// TestTemporaryPasswordKeepsLockPIN: a temporary password changes how the
// person signs in, not their lock PIN.
func TestTemporaryPasswordKeepsLockPIN(t *testing.T) {
	d := testdb.Open(t)
	hash := "$argon2id$h"
	uid := seedUser(t, d, "temp-pin@example.test", &hash)
	seedLockPIN(t, d, uid)

	svc := accounts.NewService(accounts.NewSQLStore(d), &fakeMailer{unconfigured: true}, "https://pulsix.example", time.Now, &cyclingReader{})
	if _, _, err := svc.SetTemporaryPassword(context.Background(), true, uuid.Must(uuid.NewV7()), uid); err != nil {
		t.Fatal(err)
	}
	if set, prompted := lockPINState(t, d, uid); !set || !prompted {
		t.Errorf("after the temporary password: hash set %v, prompted %v, want both kept", set, prompted)
	}
}
