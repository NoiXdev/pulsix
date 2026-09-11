package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// secondFactorsWith builds an auth.SecondFactors over in-memory fakes: a
// TOTP service whose account has a confirmed secret iff totpConfirmed, and a
// passkey service whose account holds exactly creds.
func secondFactorsWith(t *testing.T, totpStore *fakeTOTPStore, pkStore *memPasskeyStore) auth.SecondFactors {
	t.Helper()
	pk, _, _ := newPasskeysTwoUsers(t, pkStore, time.Now)
	return auth.SecondFactors{TOTP: auth.NewTOTP(totpStore, testTOTPBox(), "Pulsix Test", testTOTPNow), Passkeys: pk}
}

func confirmedTOTP() *fakeTOTPStore {
	return &fakeTOTPStore{hasSecret: true, secret: auth.TOTPSecretRow{Confirmed: true}}
}

func withPasskeys(creds ...auth.Credential) *memPasskeyStore {
	s := newMemPasskeyStore(time.Now)
	s.listResult = creds
	return s
}

func TestSecondFactorsEnrolled(t *testing.T) {
	ctx := context.Background()
	uid := uuid.Must(uuid.NewV7())
	cases := []struct {
		name string
		totp *fakeTOTPStore
		pk   *memPasskeyStore
		want bool
	}{
		{"nothing", &fakeTOTPStore{}, withPasskeys(), false},
		{"unconfirmed TOTP only", &fakeTOTPStore{hasSecret: true}, withPasskeys(), false},
		{"confirmed TOTP", confirmedTOTP(), withPasskeys(), true},
		{"passkey", &fakeTOTPStore{}, withPasskeys(auth.Credential{ID: uuid.Must(uuid.NewV7())}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := secondFactorsWith(t, tc.totp, tc.pk).Enrolled(ctx, uid)
			if err != nil || got != tc.want {
				t.Fatalf("Enrolled = %v, %v; want %v, nil", got, err, tc.want)
			}
		})
	}
	sentinel := errors.New("store down")
	if _, err := secondFactorsWith(t, &fakeTOTPStore{secretErr: sentinel}, withPasskeys()).Enrolled(ctx, uid); !errors.Is(err, sentinel) {
		t.Fatalf("Enrolled with a failing store: err = %v, want it to fail closed with %v", err, sentinel)
	}
}

func TestSecondFactorsDisableTOTP(t *testing.T) {
	ctx := context.Background()
	uid := uuid.Must(uuid.NewV7())

	t.Run("refused when no passkey remains", func(t *testing.T) {
		store := confirmedTOTP()
		if err := secondFactorsWith(t, store, withPasskeys()).DisableTOTP(ctx, uid); !errors.Is(err, auth.ErrLastSecondFactor) {
			t.Fatalf("err = %v, want ErrLastSecondFactor", err)
		}
		if store.deleteCalls != 0 {
			t.Fatal("TOTP was deleted although the removal was refused")
		}
	})
	t.Run("allowed with a passkey to fall back on", func(t *testing.T) {
		store := confirmedTOTP()
		if err := secondFactorsWith(t, store, withPasskeys(auth.Credential{ID: uuid.Must(uuid.NewV7())})).DisableTOTP(ctx, uid); err != nil {
			t.Fatal(err)
		}
		if store.deleteCalls != 1 {
			t.Fatalf("DeleteTOTP calls = %d, want 1", store.deleteCalls)
		}
	})
	t.Run("a failing passkey store fails closed", func(t *testing.T) {
		sentinel := errors.New("store down")
		pk := withPasskeys()
		pk.listErr = sentinel
		store := confirmedTOTP()
		if err := secondFactorsWith(t, store, pk).DisableTOTP(ctx, uid); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
		if store.deleteCalls != 0 {
			t.Fatal("TOTP was deleted although the check failed")
		}
	})
}

func TestSecondFactorsDeletePasskey(t *testing.T) {
	ctx := context.Background()
	uid := uuid.Must(uuid.NewV7())
	a, b := auth.Credential{ID: uuid.Must(uuid.NewV7())}, auth.Credential{ID: uuid.Must(uuid.NewV7())}

	t.Run("refused for the only passkey without TOTP", func(t *testing.T) {
		pk := withPasskeys(a)
		if err := secondFactorsWith(t, &fakeTOTPStore{}, pk).DeletePasskey(ctx, uid, a.ID); !errors.Is(err, auth.ErrLastSecondFactor) {
			t.Fatalf("err = %v, want ErrLastSecondFactor", err)
		}
		if pk.deleteArgs != nil {
			t.Fatal("the passkey was deleted although the removal was refused")
		}
	})
	t.Run("allowed for the only passkey with TOTP confirmed", func(t *testing.T) {
		pk := withPasskeys(a)
		if err := secondFactorsWith(t, confirmedTOTP(), pk).DeletePasskey(ctx, uid, a.ID); err != nil {
			t.Fatal(err)
		}
		if len(pk.deleteArgs) != 2 || pk.deleteArgs[1] != a.ID {
			t.Fatalf("delete args = %v, want the passkey %s", pk.deleteArgs, a.ID)
		}
	})
	t.Run("allowed for one of two passkeys", func(t *testing.T) {
		pk := withPasskeys(a, b)
		if err := secondFactorsWith(t, &fakeTOTPStore{}, pk).DeletePasskey(ctx, uid, b.ID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a passkey the account does not own is not found", func(t *testing.T) {
		pk := withPasskeys(a)
		if err := secondFactorsWith(t, confirmedTOTP(), pk).DeletePasskey(ctx, uid, b.ID); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("failing stores fail closed", func(t *testing.T) {
		sentinel := errors.New("store down")
		pk := withPasskeys(a)
		pk.listErr = sentinel
		if err := secondFactorsWith(t, confirmedTOTP(), pk).DeletePasskey(ctx, uid, a.ID); !errors.Is(err, sentinel) {
			t.Fatalf("list failure: err = %v, want %v", err, sentinel)
		}
		pk = withPasskeys(a)
		if err := secondFactorsWith(t, &fakeTOTPStore{secretErr: sentinel}, pk).DeletePasskey(ctx, uid, a.ID); !errors.Is(err, sentinel) {
			t.Fatalf("TOTP check failure: err = %v, want %v", err, sentinel)
		}
		if pk.deleteArgs != nil {
			t.Fatal("the passkey was deleted although the check failed")
		}
	})
}
